package pipeprint

import "slices"

// Warning reports a setting that a compiler dropped, moved or defaulted
// because the target cannot express it.
type Warning struct {
	// Path locates the setting, such as jobs.build.native.github.permissions.
	Path string `json:"path"`
	// Message says what happened.
	Message string `json:"message"`
}

// String returns "path: message".
func (w Warning) String() string {
	return w.Path + ": " + w.Message
}

// Warnings collects warnings in order.
type Warnings []Warning

// Add appends a warning.
func (w *Warnings) Add(path, message string) {
	*w = append(*w, Warning{Path: path, Message: message})
}

// Foreign adds a warning for every native setting outside dialect, which
// the target forge cannot use.
func (w *Warnings) Foreign(path string, n Native, dialect Dialect) {
	for _, other := range Dialects() {
		if other == dialect {
			continue
		}

		for _, key := range SortedKeys(n[other]) {
			w.Add(path+".native."+string(other)+"."+key, string(other)+"-only setting dropped")
		}
	}
}

// SortedKeys returns m's keys in order.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	return keys
}

// Set stores value under key in n's dialect block, creating it as needed.
func (n *Native) Set(dialect Dialect, key string, value any) {
	if *n == nil {
		*n = Native{}
	}

	if (*n)[dialect] == nil {
		(*n)[dialect] = map[string]any{}
	}

	(*n)[dialect][key] = value
}
