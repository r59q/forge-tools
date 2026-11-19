// Package mcp serves the tools forge-agents gives its clients: ask_user,
// which pauses a turn until a person answers, and approve, Claude Code's
// permission prompt tool, plus tools to manage child sessions. It speaks the Model Context Protocol over
// streamable HTTP, answering every request with a single JSON response.
package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
)

// maxBody bounds a request body.
const maxBody = 1 << 20

// latestVersion is offered to clients asking for an unknown protocol
// version.
const latestVersion = "2025-06-18"

// Decision answers a permission request.
type Decision struct {
	Allow   bool
	Message string
}

// Binding is what one turn's tools do. A nil Approve leaves the approve
// tool out.
type Binding struct {
	Children *Children
	Ask      func(ctx context.Context, question string) (string, error)
	Approve  func(ctx context.Context, tool string, input json.RawMessage) (Decision, error)
}

// Server routes tool calls to the turn whose token they carry. The zero
// value is ready to use.
type Server struct {
	mu    sync.Mutex
	turns map[string]Binding
}

// Register makes b reachable with a new token, which it returns, until
// the returned function is called.
func (s *Server) Register(b Binding) (string, func()) {
	return s.RegisterFor(func(string) Binding { return b })
}

// RegisterFor builds a binding that can also mask its own token in tool arguments.
func (s *Server) RegisterFor(bind func(token string) Binding) (string, func()) {
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	token := hex.EncodeToString(raw[:])
	b := bind(token)
	s.mu.Lock()
	if s.turns == nil {
		s.turns = map[string]Binding{}
	}
	s.turns[token] = b
	s.mu.Unlock()
	return token, func() {
		s.mu.Lock()
		delete(s.turns, token)
		s.mu.Unlock()
	}
}

func (s *Server) binding(r *http.Request) (Binding, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return Binding{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.turns[token]
	return b, ok
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitzero"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitzero"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitzero"`
	Error   *rpcError       `json:"error,omitzero"`
}

// JSON-RPC error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethod         = -32601
	codeParams         = -32602
)

// ServeHTTP answers one JSON-RPC message.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Browsers send Origin; clients do not. Refusing it blocks DNS rebinding.
	if r.Header.Get("Origin") != "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	b, ok := s.binding(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		reply(w, response{ID: json.RawMessage("null"), Error: &rpcError{codeParse, "parse error"}})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		reply(w, response{ID: idOrNull(req.ID), Error: &rpcError{codeInvalidRequest, "invalid request"}})
		return
	}
	if req.ID == nil {
		// Notifications need no answer.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rerr := handle(r.Context(), b, req)
	reply(w, response{ID: req.ID, Result: result, Error: rerr})
}

func idOrNull(id json.RawMessage) json.RawMessage {
	if id == nil {
		return json.RawMessage("null")
	}
	return id
}

func reply(w http.ResponseWriter, resp response) {
	resp.JSONRPC = "2.0"
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func handle(ctx context.Context, b Binding, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := latestVersion
		if slices.Contains([]string{"2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"}, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": adapter.ServerName, "version": "1"},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": tools(b)}, nil
	case "tools/call":
		return call(ctx, b, req.Params)
	}
	return nil, &rpcError{codeMethod, "method not found: " + req.Method}
}

// Tool descriptions shown to the model.
const (
	askDescription = "Ask the person supervising you a question and wait for their answer. " +
		"Use it when you need a decision or information you cannot find yourself."
	approveDescription = "Decide whether a tool call may run."
)

func tools(b Binding) []map[string]any {
	list := []map[string]any{
		tool(adapter.ToolAsk, askDescription, map[string]any{"question": textSchema()}, "question"),
	}
	if b.Approve != nil {
		list = append(list, tool(adapter.ToolApprove, approveDescription, map[string]any{
			"tool_name": textSchema(), "input": map[string]any{"type": "object"}, "tool_use_id": textSchema(),
		}, "tool_name", "input"))
	}
	if b.Children != nil {
		list = append(list, childTools()...)
	}
	return list
}

func call(ctx context.Context, b Binding, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{codeParams, "invalid params"}
	}
	switch {
	case p.Name == adapter.ToolAsk:
		var args struct {
			Question string `json:"question"`
		}
		if err := json.Unmarshal(p.Arguments, &args); err != nil || strings.TrimSpace(args.Question) == "" {
			return nil, &rpcError{codeParams, "ask_user needs a question"}
		}
		answer, err := b.Ask(ctx, args.Question)
		return toolResult(answer, err), nil
	case p.Name == adapter.ToolApprove && b.Approve != nil:
		var args struct {
			Tool  string          `json:"tool_name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(p.Arguments, &args); err != nil || args.Tool == "" {
			return nil, &rpcError{codeParams, "approve needs tool_name and input"}
		}
		d, err := b.Approve(ctx, args.Tool, args.Input)
		if err != nil {
			return toolResult("", err), nil
		}
		return toolResult(permission(d, args.Input), nil), nil
	}
	if b.Children != nil {
		return b.Children.call(ctx, p.Name, p.Arguments)
	}
	return nil, &rpcError{codeParams, "unknown tool: " + p.Name}
}

// permission is the answer format Claude Code expects from a permission
// prompt tool.
func permission(d Decision, input json.RawMessage) string {
	var v any
	if d.Allow {
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		v = map[string]any{"behavior": "allow", "updatedInput": input}
	} else {
		message := d.Message
		if message == "" {
			message = "denied by the supervisor"
		}
		v = map[string]any{"behavior": "deny", "message": message}
	}
	out, _ := json.Marshal(v)
	return string(out)
}

func toolResult(text string, err error) map[string]any {
	isError := err != nil
	if isError {
		text = err.Error()
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}
