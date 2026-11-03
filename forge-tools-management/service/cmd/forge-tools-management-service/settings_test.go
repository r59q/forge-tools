package main

import (
	"io"
	"testing"
)

func TestBackendTransportFlagPrecedence(t *testing.T) {
	backends := []struct {
		name    string
		env     string
		enabled func(settings) bool
	}{
		{"repositories", "REPOSITORIES", func(s settings) bool { return s.repositoriesInsecure }},
		{"issues", "ISSUES", func(s settings) bool { return s.issuesInsecure }},
		{"actions", "ACTIONS", func(s settings) bool { return s.actionsInsecure }},
		{"connector", "CONNECTOR", func(s settings) bool { return s.connectorInsecure }},
	}

	for _, backend := range backends {
		t.Run(backend.name, func(t *testing.T) {
			getenv := func(key string) string {
				if key == "FORGE_MANAGEMENT_"+backend.env+"_INSECURE" {
					return "false"
				}

				return ""
			}
			args := []string{
				"--" + backend.name + "-address=localhost:9090",
				"--" + backend.name + "-insecure=true",
			}

			s, err := parse(args, getenv, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if !backend.enabled(s) {
				t.Fatal("the command-line transport flag did not override the environment")
			}
		})
	}
}
