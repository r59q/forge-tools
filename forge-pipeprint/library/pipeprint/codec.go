package pipeprint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"
)

// ErrInvalid wraps every error for input that is not a valid pipeline.
var ErrInvalid = errors.New("pipeprint: invalid pipeline")

// Parse reads a Pipeprint file in YAML or JSON and validates it. Unknown
// fields are errors, so typos do not pass silently.
func Parse(data []byte) (*Pipeline, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	var p Pipeline
	if err := decoder.Decode(&p); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: empty file", ErrInvalid)
		}

		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	if err := p.Validate(); err != nil {
		return nil, err
	}

	return &p, nil
}

// YAML returns p as Pipeprint YAML with two-space indents.
func (p *Pipeline) YAML() ([]byte, error) {
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)

	if err := encoder.Encode(p); err != nil {
		return nil, err
	}

	if err := encoder.Close(); err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

// JSON returns p as indented Pipeprint JSON.
func (p *Pipeline) JSON() ([]byte, error) {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}

	return append(data, '\n'), nil
}
