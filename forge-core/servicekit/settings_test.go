package servicekit

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
	"time"
)

var options = Options{
	Name:              "forge-test-service",
	Prefix:            "FORGE_TEST",
	GRPCListen:        "127.0.0.1:9000",
	HTTPListen:        "127.0.0.1:8000",
	Timeout:           "30s",
	ConnectorRequired: true,
}

func getenv(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestParseDefaults(t *testing.T) {
	s, err := Parse([]string{"--connector-url", "http://proxy"}, getenv(nil), nil, options, nil)
	if err != nil {
		t.Fatal(err)
	}

	want := Settings{
		GRPCAddress:       "127.0.0.1:9000",
		HTTPAddress:       "127.0.0.1:8000",
		TokenEnv:          "FORGE_TEST_TOKEN",
		ConnectorURL:      "http://proxy",
		ConnectorTokenEnv: "FORGE_TEST_CONNECTOR_TOKEN",
		Timeout:           30 * time.Second,
	}
	if s != want {
		t.Errorf("got %+v, want %+v", s, want)
	}
}

func TestParseEnvironmentAndExtra(t *testing.T) {
	vars := map[string]string{
		"FORGE_TEST_HTTP_LISTEN":     "0.0.0.0:80",
		"FORGE_TEST_CONNECTOR_URL":   "http://proxy",
		"FORGE_TEST_TOKEN_FILE":      "/run/token",
		"FORGE_TEST_DATA_DIR":        "/data",
		"FORGE_TEST_REQUEST_TIMEOUT": "5m",
	}

	var dir string
	extra := func(fs *flag.FlagSet, env Env) {
		fs.StringVar(&dir, "data-dir", env("DATA_DIR", "./data"), "data directory")
	}

	s, err := Parse(nil, getenv(vars), nil, options, extra)
	if err != nil {
		t.Fatal(err)
	}

	if s.HTTPAddress != "0.0.0.0:80" || s.TokenFile != "/run/token" || s.TokenEnv != "" || s.Timeout != 5*time.Minute || dir != "/data" {
		t.Errorf("got %+v, dir %q", s, dir)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		opts Options
		want string
	}{
		{"unknown flag", []string{"--secret=hunter2"}, options, "invalid startup flags; use --help"},
		{"extra argument", []string{"--connector-url", "x", "extra"}, options, "invalid startup settings"},
		{"bad timeout", []string{"--connector-url", "x", "--request-timeout", "soon"}, options, "invalid request timeout"},
		{"zero timeout", []string{"--connector-url", "x", "--request-timeout", "0s"}, options, "invalid startup settings"},
		{"empty listen", []string{"--connector-url", "x", "--grpc-listen", ""}, options, "invalid startup settings"},
		{"no connector", nil, options, "connector URL required: set --connector-url or FORGE_TEST_CONNECTOR_URL"},
		{
			"two service tokens",
			[]string{"--connector-url", "x", "--token-file", "f", "--token-env", "E"},
			options,
			"choose one service token source",
		},
		{
			"two connector tokens",
			[]string{"--connector-url", "x", "--connector-token-file", "f", "--connector-token-env", "E"},
			options,
			"choose one connector token source",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.args, getenv(nil), nil, tt.opts, nil)
			if err == nil || err.Error() != tt.want {
				t.Errorf("got %v, want %q", err, tt.want)
			}

			if err != nil && strings.Contains(err.Error(), "hunter2") {
				t.Error("error leaks a flag value")
			}
		})
	}
}

func TestParseOptionalConnector(t *testing.T) {
	opts := options
	opts.ConnectorRequired = false

	s, err := Parse(nil, getenv(nil), nil, opts, nil)
	if err != nil || s.ConnectorURL != "" {
		t.Fatalf("got %+v, %v", s, err)
	}
}

func TestParseHelp(t *testing.T) {
	var help bytes.Buffer
	_, err := Parse([]string{"--help"}, getenv(nil), &help, options, nil)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("got %v", err)
	}

	if !strings.Contains(help.String(), "FORGE_TEST_TOKEN") || !strings.Contains(help.String(), "(required)") {
		t.Errorf("help = %q", help.String())
	}
}
