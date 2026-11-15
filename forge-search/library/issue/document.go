package issue

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/r59q/forge-tools/forge-issues/library/issues"
	"github.com/r59q/forge-tools/forge-search/library/search"
)

// numberDigits is the width of an issue number in a document ID, so that
// IDs sort by number. It fits every positive int64.
const numberDigits = 19

// home is where the documents of an issue live: its source ID and
// repository path.
type home struct {
	source     string
	repository string
}

func (h home) valid() bool {
	return validName(h.source) && validRepository(h.repository)
}

// version is the metadata a document version carries, so that a match in an
// index can tell its issue's title, state and URL.
type version struct {
	State   issues.State `json:"state"`
	Title   string       `json:"title"`
	URL     string       `json:"url,omitempty"`
	Content string       `json:"content"`
}

// document returns the search document of an issue. It reports false when
// the issue is over a limit, and returns search.ErrInvalid when it has no
// valid number or state.
func document(at home, item issues.Issue, limits Limits) (search.Document, bool, error) {
	if item.Number <= 0 {
		return search.Document{}, false, fmt.Errorf("issue number: %w", search.ErrInvalid)
	}
	if item.State != issues.Open && item.State != issues.Closed {
		return search.Document{}, false, fmt.Errorf("issue %d state: %w", item.Number, search.ErrInvalid)
	}

	oversized := len(item.Body) > limits.BodyBytes ||
		len(item.Title) > MaxTitleBytes ||
		len(item.WebURL) > MaxURLBytes
	if oversized {
		return search.Document{}, false, nil
	}

	title := oneLine(item.Title)
	content := []byte(title + "\n" + item.Body)

	meta, fits := encodeVersion(version{
		State:   item.State,
		Title:   title,
		URL:     item.WebURL,
		Content: contentHash(content),
	})
	if !fits {
		return search.Document{}, false, nil
	}

	ref := search.Ref{Kind: search.KindIssue, Source: at.source, ID: documentID(at.repository, item.Number), Version: meta}
	return search.Document{Ref: ref, Content: content}, true, nil
}

// oneLine replaces line breaks in a title with spaces, byte for byte, so
// that the title is the first line of the document.
func oneLine(title string) string {
	return strings.Map(func(char rune) rune {
		if char == '\r' || char == '\n' {
			return ' '
		}
		return char
	}, title)
}

func encodeVersion(meta version) (string, bool) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(meta); err != nil {
		return "", false
	}
	encoded := strings.TrimSuffix(buffer.String(), "\n")
	return encoded, len(encoded) <= search.MaxNameBytes
}

func contentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func documentID(repository string, number int64) string {
	return fmt.Sprintf("%s#%0*d", repository, numberDigits, number)
}

// parseID returns the repository and number of a document ID.
func parseID(id string) (string, int64, bool) {
	at := strings.LastIndexByte(id, '#')
	if at < 0 || len(id)-at-1 != numberDigits {
		return "", 0, false
	}

	number, err := strconv.ParseInt(id[at+1:], 10, 64)
	if err != nil || number <= 0 {
		return "", 0, false
	}
	return id[:at], number, true
}

// toMatch turns a search match in an issue document into an issue match.
func toMatch(found search.Match) (Match, error) {
	ref := found.Document

	repository, number, parsed := parseID(ref.ID)
	var meta version
	if !parsed || json.Unmarshal([]byte(ref.Version), &meta) != nil {
		return Match{}, fmt.Errorf("issue document: %w", search.ErrInvalid)
	}

	result := Match{
		Issue: Info{
			Source:     ref.Source,
			Repository: repository,
			Number:     number,
			Title:      meta.Title,
			State:      meta.State,
			WebURL:     meta.URL,
		},
		Field:         FieldTitle,
		Range:         found.Range,
		Preview:       found.Preview,
		PreviewColumn: found.PreviewColumn,
	}

	if found.Range.StartLine > 1 {
		result.Field = FieldBody
		result.Range.StartLine--
		result.Range.EndLine--
	}
	return result, nil
}

// selection returns the search filter of an issue filter.
func selection(filter Filter) search.Filter {
	selected := search.Filter{Kind: search.KindIssue, Source: filter.Source}
	if filter.Repository != "" {
		selected.IDPrefix = filter.Repository + "#"
	}
	return selected
}

func checkFilter(filter Filter) error {
	validState := filter.State == "" || filter.State == issues.Open || filter.State == issues.Closed
	validRepo := filter.Repository == "" || validRepository(filter.Repository)

	if !validState || !validRepo || !validName(filter.Source) {
		return fmt.Errorf("issue filter: %w", search.ErrInvalid)
	}
	return nil
}

// validRepository reports whether a repository path can be part of a
// document ID. Forge paths never hold "#".
func validRepository(repository string) bool {
	fits := len(repository)+1+numberDigits <= search.MaxNameBytes

	return repository != "" && fits && validName(repository) && !strings.Contains(repository, "#")
}

func validName(name string) bool {
	return len(name) <= search.MaxNameBytes && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
}
