// Package webhook verifies native GitHub and Forgejo webhook deliveries and
// normalizes the supported ones into reactor inputs. It reads no network
// and keeps no state; the caller accepts the result.
package webhook

import (
	"cmp"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
)

var (
	// ErrSignature means the signature header is missing or does not match
	// the raw body under the source's secret.
	ErrSignature = errors.New("webhook: missing or invalid signature")
	// ErrMalformed means a verified delivery lacks required headers or its
	// body is not the expected JSON.
	ErrMalformed = errors.New("webhook: malformed delivery")
	// ErrMismatch means a verified delivery came from another kind of forge
	// or a repository the source does not allow.
	ErrMismatch = errors.New("webhook: delivery does not match the source")
)

// maxDeliveryID caps native delivery IDs.
const maxDeliveryID = 128

// Source is a trusted webhook configuration. The source, never the payload,
// selects the forge kind and connection of the events it produces.
type Source struct {
	// ID names the source; deliveries are deduplicated per source.
	ID string
	// Forge is forgeauth.GitHub or forgeauth.Forgejo.
	Forge forgeauth.Kind
	// Connection is the forge-connector connection ID events carry, if any.
	Connection string
	// Secret is the webhook secret. It is separate from forge credentials.
	Secret []byte
	// Repositories, if not empty, are the owner/name paths allowed.
	Repositories []string
}

// Result is a verified delivery.
type Result struct {
	// Input is the event to accept, or nil when the delivery is ignored.
	Input *reactor.Input
	// Event is the native event name.
	Event string
	// Delivery is the native delivery ID.
	Delivery string
	// Ignored says why a verified delivery produced no event.
	Ignored string
}

// Parse verifies a delivery against src's secret over the raw body, then
// normalizes it. Pushes that move an existing branch and newly opened pull
// requests produce an input; other verified deliveries, such as pings, tag
// pushes, branch creation or deletion and other pull request actions, are
// ignored with a reason. Any error means the delivery must produce no event.
func Parse(src Source, h http.Header, body []byte) (Result, error) {
	if !reactor.ValidID(src.ID) || len(src.Secret) == 0 || (src.Forge != forgeauth.GitHub && src.Forge != forgeauth.Forgejo) {
		return Result{}, reactor.ErrInvalid
	}
	if !verify(src, h, body) {
		return Result{}, ErrSignature
	}

	forge, event, delivery := headers(h)
	switch {
	case forge != src.Forge:
		return Result{}, ErrMismatch
	case event == "" || delivery == "" || len(delivery) > maxDeliveryID || strings.ContainsFunc(delivery, isControl):
		return Result{}, ErrMalformed
	}

	payload, err := sanitize(body)
	if err != nil {
		return Result{}, err
	}

	res := Result{Event: event, Delivery: delivery}
	var e *reactor.Event
	switch event {
	case "push":
		e, res.Ignored, err = push(body)
	case "pull_request":
		e, res.Ignored, err = pullRequest(body)
	default:
		res.Ignored = "unsupported event " + event
	}
	if err != nil || e == nil {
		return res, err
	}
	if len(src.Repositories) > 0 && !slices.ContainsFunc(src.Repositories, func(r string) bool { return strings.EqualFold(r, e.Repository) }) {
		return Result{}, ErrMismatch
	}

	e.Origin, e.Source, e.Forge, e.Connection = reactor.OriginWebhook, src.ID, src.Forge, src.Connection
	e.Native = &reactor.Native{Event: event, Delivery: delivery, Payload: payload}
	res.Input = &reactor.Input{Event: *e, Key: "webhook:" + src.ID + ":" + delivery}

	return res, nil
}

// verify checks the HMAC-SHA256 signature for src's forge.
func verify(src Source, h http.Header, body []byte) bool {
	var sig string
	if src.Forge == forgeauth.GitHub {
		v, ok := strings.CutPrefix(h.Get("X-Hub-Signature-256"), "sha256=")
		if !ok {
			return false
		}
		sig = v
	} else {
		sig = cmp.Or(h.Get("X-Forgejo-Signature"), h.Get("X-Gitea-Signature"))
	}

	got, err := hex.DecodeString(sig)
	if err != nil || len(got) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, src.Secret)
	mac.Write(body)

	return hmac.Equal(got, mac.Sum(nil))
}

// headers reads the forge kind, event name and delivery ID. Forgejo also
// sends GitHub-style headers, so its own headers decide.
func headers(h http.Header) (forgeauth.Kind, string, string) {
	if event := cmp.Or(h.Get("X-Forgejo-Event"), h.Get("X-Gitea-Event")); event != "" {
		return forgeauth.Forgejo, event, cmp.Or(h.Get("X-Forgejo-Delivery"), h.Get("X-Gitea-Delivery"))
	}
	if event := h.Get("X-GitHub-Event"); event != "" {
		return forgeauth.GitHub, event, h.Get("X-GitHub-Delivery")
	}

	return "", "", ""
}

// sanitize returns body as a JSON object without a top-level secret field,
// which older Gitea-compatible forges included.
func sanitize(body []byte) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return nil, ErrMalformed
	}
	delete(object, "secret")

	return json.Marshal(object)
}

// isControl reports a control character.
func isControl(r rune) bool { return r < 0x20 || r == 0x7f }
