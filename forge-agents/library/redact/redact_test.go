package redact_test

import (
	"testing"

	"github.com/r59q/forge-tools/forge-agents/library/redact"
)

func TestRedactor(t *testing.T) {
	r := redact.FromEnv([]string{
		"ANTHROPIC_API_KEY=sk-ant-0123456789",
		"OPENAI_API_KEY=short",
		"HOME=/home/agent-user",
		"GH_TOKEN=ghp_abcdefgh",
		"MALFORMED",
	}, "turn-token-1234", "turn-token-1234-longer")
	in := "key sk-ant-0123456789 home /home/agent-user gh ghp_abcdefgh short turn-token-1234-longer turn-token-1234"
	want := "key [redacted] home /home/agent-user gh [redacted] short [redacted] [redacted]"
	if got := r.String(in); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	plain := []byte("nothing here")
	if got := r.Bytes(plain); &got[0] != &plain[0] {
		t.Error("clean input was copied")
	}
}
