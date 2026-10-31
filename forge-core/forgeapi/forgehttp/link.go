package forgehttp

import (
	"net/http"
	"sort"
	"strings"
)

// Link is one entry of a Link header (RFC 8288), such as
// <https://api.github.com/user/repos?page=2>; rel="next".
type Link struct {
	// URL is the target, as sent. It may be relative.
	URL string
	// Rel is the rel parameter. It may hold several space-separated types.
	Rel string
	// Params holds other parameters by lower-case name, or is nil.
	Params map[string]string
}

// HasRel reports whether l has the relation type rel, ignoring case.
func (l Link) HasRel(rel string) bool {
	for _, r := range strings.Fields(l.Rel) {
		if strings.EqualFold(r, rel) {
			return true
		}
	}
	return false
}

// String formats l for a Link header, with rel first and other parameters
// sorted by name.
func (l Link) String() string {
	var b strings.Builder
	b.WriteByte('<')
	b.WriteString(l.URL)
	b.WriteByte('>')
	if l.Rel != "" {
		writeParam(&b, "rel", l.Rel)
	}
	names := make([]string, 0, len(l.Params))
	for name := range l.Params {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		writeParam(&b, name, l.Params[name])
	}
	return b.String()
}

func writeParam(b *strings.Builder, name, value string) {
	b.WriteString("; ")
	b.WriteString(name)
	b.WriteString(`="`)
	for i := 0; i < len(value); i++ {
		if value[i] == '"' || value[i] == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(value[i])
	}
	b.WriteByte('"')
}

// Links is a parsed Link header.
type Links []Link

// Get returns the URL of the first link with relation type rel.
func (ls Links) Get(rel string) (string, bool) {
	for _, l := range ls {
		if l.HasRel(rel) {
			return l.URL, true
		}
	}
	return "", false
}

// String formats ls as one Link header value.
func (ls Links) String() string {
	parts := make([]string, len(ls))
	for i, l := range ls {
		parts[i] = l.String()
	}
	return strings.Join(parts, ", ")
}

// ParseLinks parses all Link headers in h. Malformed entries are skipped.
func ParseLinks(h http.Header) Links {
	var links Links
	for _, v := range h.Values("Link") {
		links = append(links, parseLinkValue(v)...)
	}
	return links
}

// NextPage returns the URL of the "next" link in h, if any.
func NextPage(h http.Header) (string, bool) {
	return ParseLinks(h).Get("next")
}

// parseLinkValue parses one Link header value with comma-separated entries.
func parseLinkValue(s string) Links {
	var links Links
	p := linkParser{s: s}
	for {
		p.skipSpace()
		if p.done() {
			return links
		}
		if l, ok := p.link(); ok {
			links = append(links, l)
		} else {
			p.skipEntry()
		}
		p.skipSpace()
		if !p.done() && p.s[p.i] == ',' {
			p.i++
		}
	}
}

type linkParser struct {
	s string
	i int
}

func (p *linkParser) done() bool { return p.i >= len(p.s) }

func (p *linkParser) skipSpace() {
	for !p.done() && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

// skipEntry moves to the next top-level comma, skipping quoted strings and
// URLs.
func (p *linkParser) skipEntry() {
	for !p.done() {
		switch p.s[p.i] {
		case ',':
			return
		case '"':
			p.quoted()
			continue
		case '<':
			if j := strings.IndexByte(p.s[p.i:], '>'); j >= 0 {
				p.i += j + 1
				continue
			}
		}
		p.i++
	}
}

// link parses `<url> *(; name[=value])` and stops before a comma.
func (p *linkParser) link() (Link, bool) {
	if p.s[p.i] != '<' {
		return Link{}, false
	}
	end := strings.IndexByte(p.s[p.i:], '>')
	if end < 0 {
		return Link{}, false
	}
	l := Link{URL: strings.TrimSpace(p.s[p.i+1 : p.i+end])}
	p.i += end + 1
	for {
		p.skipSpace()
		if p.done() || p.s[p.i] == ',' {
			return l, true
		}
		if p.s[p.i] != ';' {
			return Link{}, false
		}
		p.i++
		p.skipSpace()
		name := strings.ToLower(p.token())
		if name == "" {
			return Link{}, false
		}
		var value string
		p.skipSpace()
		if !p.done() && p.s[p.i] == '=' {
			p.i++
			p.skipSpace()
			if !p.done() && p.s[p.i] == '"' {
				v, ok := p.quoted()
				if !ok {
					return Link{}, false
				}
				value = v
			} else {
				value = p.token()
			}
		}
		if name == "rel" {
			// RFC 8288: only the first rel parameter counts.
			if l.Rel == "" {
				l.Rel = value
			}
			continue
		}
		if l.Params == nil {
			l.Params = map[string]string{}
		}
		if _, dup := l.Params[name]; !dup {
			l.Params[name] = value
		}
	}
}

// token reads characters up to a delimiter.
func (p *linkParser) token() string {
	start := p.i
	for !p.done() && !strings.ContainsRune(" \t;,=\"", rune(p.s[p.i])) {
		p.i++
	}
	return p.s[start:p.i]
}

// quoted reads a quoted string starting at p.i, handling backslash escapes.
func (p *linkParser) quoted() (string, bool) {
	var b strings.Builder
	p.i++ // opening quote
	for !p.done() {
		c := p.s[p.i]
		p.i++
		switch {
		case c == '"':
			return b.String(), true
		case c == '\\' && !p.done():
			b.WriteByte(p.s[p.i])
			p.i++
		default:
			b.WriteByte(c)
		}
	}
	return "", false
}
