package pipeprint

import (
	"encoding/json"
	"errors"

	"go.yaml.in/yaml/v3"
)

// Commands are shell commands that run in order in one shell session. A
// single command is written as a string, several as a list.
type Commands []string

// errCommands means run is neither a string nor a list of strings.
var errCommands = errors.New("run must be a string or a list of strings")

// MarshalYAML writes one command as a string and several as a list.
func (c Commands) MarshalYAML() (any, error) {
	if len(c) == 1 {
		return c[0], nil
	}

	return []string(c), nil
}

// UnmarshalYAML reads a string or a list of strings.
func (c *Commands) UnmarshalYAML(n *yaml.Node) error {
	var one string
	if n.Kind == yaml.ScalarNode && n.Decode(&one) == nil {
		*c = Commands{one}
		return nil
	}

	var many []string
	if n.Kind != yaml.SequenceNode || n.Decode(&many) != nil {
		return errCommands
	}

	*c = many

	return nil
}

// MarshalJSON writes one command as a string and several as a list.
func (c Commands) MarshalJSON() ([]byte, error) {
	if len(c) == 1 {
		return json.Marshal(c[0])
	}

	return json.Marshal([]string(c))
}

// UnmarshalJSON reads a string or a list of strings.
func (c *Commands) UnmarshalJSON(data []byte) error {
	var one string
	if json.Unmarshal(data, &one) == nil {
		*c = Commands{one}
		return nil
	}

	var many []string
	if json.Unmarshal(data, &many) != nil {
		return errCommands
	}

	*c = many

	return nil
}
