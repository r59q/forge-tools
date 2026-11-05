// Package reactor turns forge activity and published messages into durable
// events and hands each event to every subscription it matches. It holds no
// business rules: consumers decide what an event means.
//
// The event model, storage and delivery rules do not depend on a message
// broker. A Broker hands one delivery to a backend such as RabbitMQ.
package reactor

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Origin says how an event entered the reactor.
type Origin string

const (
	// OriginWebhook is a verified native forge webhook delivery.
	OriginWebhook Origin = "webhook"
	// OriginPoll is a change seen by polling a forge.
	OriginPoll Origin = "poll"
	// OriginPublished is an event an authorized caller published directly.
	OriginPublished Origin = "published"
)

const (
	// TypeBranchUpdated is a new head commit on an existing branch. Its data
	// is a BranchUpdate.
	TypeBranchUpdated = "forge.branch.updated"
	// TypePullRequestOpened is a newly opened pull request. Its data is a
	// PullRequest.
	TypePullRequestOpened = "forge.pull_request.opened"
)

var (
	// ErrInvalid means a request or value is malformed.
	ErrInvalid = errors.New("reactor: invalid")
	// ErrNotFound means an event, subscription or delivery does not exist,
	// or is no longer retained.
	ErrNotFound = errors.New("reactor: not found")
	// ErrExists means a subscription with the same ID already exists.
	ErrExists = errors.New("reactor: already exists")
	// ErrOutOfRange means a replay asks for history that is not retained.
	ErrOutOfRange = errors.New("reactor: outside retained history")
	// ErrBusy means too many deliveries are waiting for the broker, so new
	// events are refused until the backlog drains.
	ErrBusy = errors.New("reactor: delivery backlog full")
)

// Event is the broker-independent envelope every consumer receives.
type Event struct {
	// ID is assigned on acceptance and never changes, across retries and
	// replay alike.
	ID string `json:"id"`
	// Type is a dotted name, such as TypeBranchUpdated.
	Type string `json:"type"`
	// Origin says how the event entered the reactor.
	Origin Origin `json:"origin"`
	// Source is the configured webhook source or poll target, if any.
	Source string `json:"source,omitempty"`
	// Forge is the forge kind, if the event has one.
	Forge forgeauth.Kind `json:"forge,omitempty"`
	// Connection is the forge-connector connection ID, if the event has one.
	Connection string `json:"connection,omitempty"`
	// Repository is the owner/name path, if the event has one.
	Repository string `json:"repository,omitempty"`
	// Subject is what changed inside the repository, such as refs/heads/main
	// or pulls/12.
	Subject string `json:"subject,omitempty"`
	// OccurredAt is when the forge says the change happened, if known.
	OccurredAt time.Time `json:"occurredAt,omitzero"`
	// AcceptedAt is when the reactor took durable responsibility.
	AcceptedAt time.Time `json:"acceptedAt"`
	// Data holds the normalized details as a JSON object.
	Data json.RawMessage `json:"data,omitempty"`
	// Native describes the native delivery, for webhook events.
	Native *Native `json:"native,omitempty"`
}

// Native preserves a native webhook delivery. It never holds request headers
// other than these, so authentication headers and signatures are not kept.
type Native struct {
	// Event is the native event name, such as push.
	Event string `json:"event"`
	// Delivery is the forge's delivery ID.
	Delivery string `json:"delivery"`
	// Payload is the native JSON body, less any top-level secret field.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// BranchUpdate is the data of TypeBranchUpdated.
type BranchUpdate struct {
	// Branch is the branch name, without refs/heads/.
	Branch string `json:"branch"`
	// Before is the previous head, if known. A poll reports its last
	// observation, which may skip several pushes.
	Before string `json:"before,omitempty"`
	// After is the new head commit.
	After string `json:"after"`
	// Forced reports a forced push, when the forge says so.
	Forced bool `json:"forced,omitempty"`
	// Pusher is the forge login that pushed, when known.
	Pusher string `json:"pusher,omitempty"`
}

// PullRequest is the data of TypePullRequestOpened.
type PullRequest struct {
	// Number is the repository-scoped pull request number.
	Number int64 `json:"number"`
	// Title is the title at opening.
	Title string `json:"title,omitempty"`
	// URL is the web page of the pull request.
	URL string `json:"url,omitempty"`
	// Author is the login that opened it.
	Author string `json:"author,omitempty"`
	// Draft reports a draft pull request.
	Draft bool `json:"draft,omitempty"`
	// Head is the proposed branch.
	Head Ref `json:"head"`
	// Base is the target branch.
	Base Ref `json:"base"`
}

// Ref is one side of a pull request.
type Ref struct {
	// Repository is the owner/name path of the side's repository.
	Repository string `json:"repository,omitempty"`
	// Branch is the branch name.
	Branch string `json:"branch,omitempty"`
	// Commit is the commit ID at opening.
	Commit string `json:"commit,omitempty"`
}

// Input is an event before acceptance. ID and AcceptedAt are assigned by
// the reactor.
type Input struct {
	// Event is the event to accept.
	Event Event
	// Key deduplicates repeated deliveries of the same input, such as a
	// webhook source and native delivery ID. Empty never deduplicates.
	Key string
}

var (
	namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	idPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

// ValidType reports whether t is a usable event type: lowercase letters,
// digits, dots, underscores and dashes, up to 128 characters.
func ValidType(t string) bool { return namePattern.MatchString(t) }

// ValidID reports whether id is a usable subscription or source ID:
// lowercase letters, digits, underscores and dashes, up to 63 characters.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// validText reports whether s is short printable text.
func validText(s string, limit int) bool {
	return len(s) <= limit && strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) < 0
}

// validData reports whether data is empty or a JSON object.
func validData(data json.RawMessage) bool {
	if len(data) == 0 {
		return true
	}
	var object map[string]json.RawMessage

	return json.Unmarshal(data, &object) == nil && object != nil
}

// validate checks the fields a caller supplies.
func (e Event) validate() error {
	switch {
	case !ValidType(e.Type),
		e.Origin != OriginWebhook && e.Origin != OriginPoll && e.Origin != OriginPublished,
		!validText(e.Source, 64), !validText(string(e.Forge), 32), !validText(e.Connection, 128),
		!validText(e.Repository, 512), !validText(e.Subject, 512), !validData(e.Data):
		return ErrInvalid
	}

	return nil
}

// newID returns a random UUID.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])

	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
