package adapter

import (
	"encoding/json"
	"strings"

	"github.com/r59q/forge-tools/forge-agents/library/transcript"
)

// Stream collects the events and session of one turn's output.
type Stream struct {
	session string
	events  []transcript.Event
	last    string
	result  bool
	role    string
	buffer  strings.Builder
	seen    map[string]bool
}

// SetSession records the client session the turn ran in.
func (s *Stream) SetSession(id string) {
	if id != "" {
		s.session = id
	}
}

// Session is the client session, for resuming the next turn.
func (s *Stream) Session() string {
	return s.session
}

// Emit adds an event after any buffered message.
func (s *Stream) Emit(e transcript.Event) {
	s.flush()
	s.add(e)
}

// Append adds streamed text to a message of role, emitted as one event
// before the next event.
func (s *Stream) Append(role, text string) {
	if s.role != role {
		s.flush()
		s.role = role
	}
	s.buffer.WriteString(text)
}

// Mark records id and reports whether it was new.
func (s *Stream) Mark(id string) bool {
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	if s.seen[id] {
		return false
	}
	s.seen[id] = true
	return true
}

// LastText is the latest assistant message.
func (s *Stream) LastText() string {
	s.flush()
	return s.last
}

// HasResult reports whether the client ended the turn with a result.
func (s *Stream) HasResult() bool {
	return s.result
}

// Take returns and forgets the events emitted so far. Close the stream
// first to include a buffered message.
func (s *Stream) Take() []transcript.Event {
	events := s.events
	s.events = nil
	return events
}

// Close emits a buffered message.
func (s *Stream) Close() {
	s.flush()
}

func (s *Stream) flush() {
	if s.buffer.Len() == 0 {
		return
	}
	text := s.buffer.String()
	s.buffer.Reset()
	s.add(transcript.Event{Kind: transcript.KindMessage, Role: s.role, Text: text})
}

func (s *Stream) add(e transcript.Event) {
	if e.Kind == transcript.KindMessage && e.Role == transcript.RoleAssistant {
		s.last = e.Text
	}
	if e.Kind == transcript.KindResult {
		s.result = true
	}
	s.events = append(s.events, e)
}

// JSON renders a value as compact JSON, or empty when it is absent.
func JSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// Text reads tool output that is a string, a list of content blocks with
// text, or other JSON.
func Text(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
		if len(parts) > 0 || len(blocks) == 0 {
			return strings.Join(parts, "\n")
		}
	}
	return JSON(raw)
}
