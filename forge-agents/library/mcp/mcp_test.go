package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-agents/library/mcp"
)

func post(t *testing.T, url, token, body string, header ...string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func text(t *testing.T, resp map[string]any) (string, bool) {
	t.Helper()
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", resp)
	}
	content := result["content"].([]any)[0].(map[string]any)
	return content["text"].(string), result["isError"].(bool)
}

func TestServer(t *testing.T) {
	var s mcp.Server
	srv := httptest.NewServer(&s)
	defer srv.Close()
	token, unregister := s.Register(mcp.Binding{
		Ask: func(_ context.Context, q string) (string, error) {
			if q == "fail" {
				return "", errors.New("turn ended")
			}
			return "answer to " + q, nil
		},
		Approve: func(_ context.Context, tool string, _ json.RawMessage) (mcp.Decision, error) {
			return mcp.Decision{Allow: tool == "Read"}, nil
		},
	})

	_, init := post(t, srv.URL, token, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if init["result"].(map[string]any)["protocolVersion"] != "2025-03-26" {
		t.Errorf("initialize = %v", init)
	}
	if code, _ := post(t, srv.URL, token, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != http.StatusAccepted {
		t.Errorf("notification status = %d", code)
	}
	_, list := post(t, srv.URL, token, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if n := len(list["result"].(map[string]any)["tools"].([]any)); n != 2 {
		t.Errorf("tools = %d", n)
	}

	_, ask := post(t, srv.URL, token, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"why"}}}`)
	if got, isErr := text(t, ask); got != "answer to why" || isErr {
		t.Errorf("ask = %q %v", got, isErr)
	}
	_, failed := post(t, srv.URL, token, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"fail"}}}`)
	if got, isErr := text(t, failed); got != "turn ended" || !isErr {
		t.Errorf("failed ask = %q %v", got, isErr)
	}
	_, allow := post(t, srv.URL, token, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"approve","arguments":{"tool_name":"Read","input":{"file":"a"}}}}`)
	if got, _ := text(t, allow); got != `{"behavior":"allow","updatedInput":{"file":"a"}}` {
		t.Errorf("allow = %s", got)
	}
	_, deny := post(t, srv.URL, token, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"approve","arguments":{"tool_name":"Bash","input":{}}}}`)
	if got, _ := text(t, deny); got != `{"behavior":"deny","message":"denied by the supervisor"}` {
		t.Errorf("deny = %s", got)
	}
	_, unknown := post(t, srv.URL, token, `{"jsonrpc":"2.0","id":7,"method":"resources/list"}`)
	if unknown["error"] == nil {
		t.Errorf("unknown method = %v", unknown)
	}

	if code, _ := post(t, srv.URL, token, `{}`, "Origin", "http://evil.example"); code != http.StatusForbidden {
		t.Errorf("origin status = %d", code)
	}
	unregister()
	if code, _ := post(t, srv.URL, token, `{"jsonrpc":"2.0","id":8,"method":"ping"}`); code != http.StatusUnauthorized {
		t.Errorf("unregistered status = %d", code)
	}
}

func TestNoApprove(t *testing.T) {
	var s mcp.Server
	srv := httptest.NewServer(&s)
	defer srv.Close()
	token, _ := s.Register(mcp.Binding{Ask: func(context.Context, string) (string, error) { return "", nil }})
	_, list := post(t, srv.URL, token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if n := len(list["result"].(map[string]any)["tools"].([]any)); n != 1 {
		t.Errorf("tools = %d", n)
	}
	_, call := post(t, srv.URL, token, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"approve","arguments":{"tool_name":"Bash","input":{}}}}`)
	if call["error"] == nil {
		t.Errorf("approve without binding = %v", call)
	}
}
