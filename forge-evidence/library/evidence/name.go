package evidence

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxNameBytes is the longest entry or file name.
const MaxNameBytes = 64

// MaxPathBytes is the longest subject path.
const MaxPathBytes = 4096

// MaxTitleBytes is the longest title.
const MaxTitleBytes = 256

// MaxKindBytes is the longest kind.
const MaxKindBytes = 32

// Lengths of a full commit SHA in hex.
const (
	// SHA1Length is the length of a SHA-1 commit SHA.
	SHA1Length = 40

	// SHA256Length is the length of a SHA-256 commit SHA, and of a file
	// digest.
	SHA256Length = 64
)

// ValidName reports whether name is a valid entry or file name: 1 to
// MaxNameBytes bytes of a-z, A-Z, 0-9, '.', '-' and '_', starting with a
// letter or digit, without "..", not ending in '.' and not a Windows device
// name such as "con" or "nul.txt". Such a name is one safe path element on
// every file system and object store. Otherwise it returns ErrInvalid, without
// the name.
func ValidName(name string) error {
	switch {
	case name == "":
		return invalid("name", "is empty")
	case len(name) > MaxNameBytes:
		return invalid("name", "is too long")
	case strings.ContainsAny(name, `/\`):
		return invalid("name", "has a path separator")
	case hasControl(name):
		return invalid("name", "has a control character")
	case strings.Contains(name, ".."):
		return invalid("name", `has ".."`)
	case !onlyNameBytes(name):
		return invalid("name", "has a character that is not allowed")
	case !isAlphanumeric(name[0]):
		return invalid("name", "does not start with a letter or digit")
	case strings.HasSuffix(name, "."):
		return invalid("name", `ends with "."`)
	case isDeviceName(name):
		return invalid("name", "is a reserved device name")
	}
	return nil
}

// ValidFileName applies ValidName to an evidence file and reserves MetadataFile
// in every case. An invalid name returns ErrInvalid, without the name.
func ValidFileName(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if strings.EqualFold(name, MetadataFile) {
		return invalid("name", "is the metadata file name")
	}
	return nil
}

// ValidCommit reports whether commit is a full commit SHA: 40 or 64 lowercase
// hex digits. Otherwise it returns ErrInvalid, without the value.
func ValidCommit(commit string) error {
	if len(commit) != SHA1Length && len(commit) != SHA256Length {
		return invalid("commit", "is not a full SHA-1 or SHA-256")
	}
	if !isLowerHex(commit) {
		return invalid("commit", "is not lowercase hex")
	}
	return nil
}

// ValidPath reports whether path is a valid subject path: a repository path
// of 1 to MaxPathBytes bytes of valid UTF-8, with '/' separators, not starting
// or ending with '/', without empty, "." or ".." elements, backslashes or
// control characters. Otherwise it returns ErrInvalid, without the path.
func ValidPath(path string) error {
	switch {
	case path == "":
		return invalid("path", "is empty")
	case len(path) > MaxPathBytes:
		return invalid("path", "is too long")
	case !utf8.ValidString(path):
		return invalid("path", "is not valid UTF-8")
	case hasControl(path):
		return invalid("path", "has a control character")
	case strings.Contains(path, `\`):
		return invalid("path", "has a backslash")
	case strings.HasPrefix(path, "/"):
		return invalid("path", "is absolute")
	}

	for element := range strings.SplitSeq(path, "/") {
		if element == "" || element == "." || element == ".." {
			return invalid("path", `has an empty, "." or ".." element`)
		}
	}
	return nil
}

// ValidKind reports whether kind is valid: 1 to MaxKindBytes bytes of a-z,
// 0-9 and '-', starting with a letter. Otherwise it returns ErrInvalid.
func ValidKind(kind Kind) error {
	value := string(kind)

	switch {
	case value == "":
		return invalid("kind", "is empty")
	case len(value) > MaxKindBytes:
		return invalid("kind", "is too long")
	case value[0] < 'a' || value[0] > 'z':
		return invalid("kind", "does not start with a letter")
	}

	for index := range len(value) {
		if !isKindByte(value[index]) {
			return invalid("kind", "has a character that is not allowed")
		}
	}
	return nil
}

func validTitle(title string) error {
	switch {
	case strings.TrimSpace(title) == "":
		return invalid("title", "is empty")
	case len(title) > MaxTitleBytes:
		return invalid("title", "is too long")
	case !utf8.ValidString(title):
		return invalid("title", "is not valid UTF-8")
	case hasControl(title):
		return invalid("title", "has a control character")
	}
	return nil
}

func validDigest(digest string) error {
	if len(digest) != SHA256Length || !isLowerHex(digest) {
		return invalid("sha256", "is not 64 lowercase hex digits")
	}
	return nil
}

func invalid(field, reason string) error {
	return fmt.Errorf("%s %s: %w", field, reason, ErrInvalid)
}

func hasControl(value string) bool {
	return strings.ContainsFunc(value, unicode.IsControl)
}

func onlyNameBytes(name string) bool {
	for index := range len(name) {
		char := name[index]
		if !isAlphanumeric(char) && char != '.' && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func isAlphanumeric(char byte) bool {
	return isLower(char) || isDigit(char) || (char >= 'A' && char <= 'Z')
}

func isKindByte(char byte) bool {
	return isLower(char) || isDigit(char) || char == '-'
}

func isLowerHex(value string) bool {
	for index := range len(value) {
		char := value[index]
		if !isDigit(char) && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func isLower(char byte) bool {
	return char >= 'a' && char <= 'z'
}

func isDigit(char byte) bool {
	return char >= '0' && char <= '9'
}

// isDeviceName reports whether Windows reserves the name, with or without an
// extension, for a device.
func isDeviceName(name string) bool {
	base, _, _ := strings.Cut(strings.ToLower(name), ".")

	switch base {
	case "con", "prn", "aux", "nul":
		return true
	}

	isNumbered := len(base) == 4 && isDigit(base[3])
	return isNumbered && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt"))
}
