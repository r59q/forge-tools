package gitlab

import "strings"

// regexSpecial are characters escaped in regular expressions.
const regexSpecial = `\.^$|()[]{}+/`

// globRegex translates a branch or tag glob with *, ** and ? into an
// anchored regular expression.
func globRegex(glob string) (string, bool) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case strings.HasPrefix(glob[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString(".")
		case strings.IndexByte("[]!+", c) >= 0:
			return "", false
		case strings.IndexByte(regexSpecial, c) >= 0:
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}

	b.WriteString("$")

	return b.String(), true
}

// regexGlob reverses globRegex. It fails on any other expression.
func regexGlob(re string) (string, bool) {
	inner, ok := strings.CutPrefix(re, "^")
	if !ok || !strings.HasSuffix(inner, "$") {
		return "", false
	}

	inner = strings.TrimSuffix(inner, "$")

	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		switch {
		case strings.HasPrefix(inner[i:], ".*"):
			b.WriteString("**")
			i++
		case strings.HasPrefix(inner[i:], "[^/]*"):
			b.WriteString("*")
			i += len("[^/]*") - 1
		case c == '.':
			b.WriteString("?")
		case c == '\\' && i+1 < len(inner):
			b.WriteByte(inner[i+1])
			i++
		case strings.IndexByte(regexSpecial, c) >= 0 || c == '*' || c == '?':
			return "", false
		default:
			b.WriteByte(c)
		}
	}

	return b.String(), true
}
