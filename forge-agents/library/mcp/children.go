package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
)

// Spawn describes a child session. Pointer fields distinguish an omitted
// setting from an explicit client default or permission setting.
type Spawn struct {
	Prompt        string  `json:"prompt"`
	Workspace     string  `json:"workspace"`
	Client        string  `json:"client"`
	Model         *string `json:"model"`
	Effort        *string `json:"effort"`
	Mode          string  `json:"mode"`
	Ask           *bool   `json:"ask"`
	AllowedModels []Model `json:"allowed_models"`
}

// Model names one allowed client and model pair.
type Model struct {
	Client string `json:"client"`
	Name   string `json:"model"`
}

// Child is the state and bounded last result of a direct child.
type Child struct {
	ID     string  `json:"id"`
	State  string  `json:"state"`
	Result string  `json:"result"`
	Status string  `json:"status,omitempty"`
	Inputs []Input `json:"inputs,omitempty"`
}

// Input is a pending question or permission request for the API supervisor.
type Input struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Text  string `json:"text,omitempty"`
	Tool  string `json:"tool,omitempty"`
	Input string `json:"input,omitempty"`
}

// Wait contains the selected children's current results.
type Wait struct {
	Children []Child `json:"children"`
	TimedOut bool    `json:"timed_out"`
}

// Children binds the child tools to the caller's agent.
type Children struct {
	Spawn func(context.Context, Spawn) (string, error)
	List  func(context.Context) ([]Child, error)
	Send  func(ctx context.Context, id, text string) (Child, error)
	Wait  func(ctx context.Context, ids []string, timeoutMS int64) (Wait, error)
	Stop  func(ctx context.Context, id string) (Child, error)
}

func childTools() []map[string]any {
	text := textSchema()
	id := map[string]any{"id": text}
	return []map[string]any{
		tool(adapter.ToolSpawn, "Start a child with a prompt. Use checkout for writes and none for an empty scratch directory. "+
			"Shared uses your directory and concurrent writes can conflict.", map[string]any{
			"prompt": text, "workspace": map[string]any{"type": "string", "enum": []string{"shared", "checkout", "none"}},
			"client": text, "model": text, "effort": text, "mode": text, "ask": map[string]any{"type": "boolean"},
			"allowed_models": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "properties": map[string]any{"client": text, "model": text}, "required": []string{"client"},
			}},
		}, "prompt", "workspace"),
		tool(adapter.ToolList, "List your direct children, their last results and any pending input.", map[string]any{}),
		tool(adapter.ToolSend, "Start a new turn on one of your idle children.", map[string]any{"id": text, "text": text}, "id", "text"),
		tool(adapter.ToolWait, "Wait for the specified children to finish, or return their current state at the timeout. "+
			"Timeout defaults to 30000 milliseconds.", map[string]any{
			"ids":        map[string]any{"type": "array", "minItems": 1, "items": text},
			"timeout_ms": map[string]any{"type": "integer", "minimum": 0},
		}, "ids"),
		tool(adapter.ToolStop, "Stop a direct child and its descendants.", id, "id"),
	}
}

func tool(name, description string, properties map[string]any, required ...string) map[string]any {
	return map[string]any{"name": name, "description": description, "inputSchema": objectSchema(properties, required...)}
}

func textSchema() map[string]any {
	return map[string]any{"type": "string"}
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) != 0 {
		schema["required"] = required
	}
	return schema
}

func (b Children) call(ctx context.Context, name string, input json.RawMessage) (any, *rpcError) {
	switch name {
	case adapter.ToolSpawn:
		return childCall(input, func(args Spawn) (any, error) {
			id, err := b.Spawn(ctx, args)
			return map[string]string{"id": id}, err
		})
	case adapter.ToolList:
		children, err := b.List(ctx)
		return jsonResult(children, err), nil
	case adapter.ToolSend:
		return childCall(input, func(args childMessage) (Child, error) { return b.Send(ctx, args.ID, args.Text) })
	case adapter.ToolWait:
		return childCall(input, func(args waitArgs) (Wait, error) { return b.Wait(ctx, args.IDs, args.TimeoutMS) })
	case adapter.ToolStop:
		return childCall(input, func(args childMessage) (Child, error) { return b.Stop(ctx, args.ID) })
	}
	return nil, &rpcError{codeParams, "unknown tool: " + name}
}

type childMessage struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type waitArgs struct {
	IDs       []string `json:"ids"`
	TimeoutMS int64    `json:"timeout_ms"`
}

func childCall[A, R any](input json.RawMessage, run func(A) (R, error)) (any, *rpcError) {
	var args A
	if err := json.Unmarshal(input, &args); err != nil {
		return nil, &rpcError{codeParams, "invalid tool arguments"}
	}
	output, err := run(args)
	return jsonResult(output, err), nil
}

func jsonResult(output any, err error) map[string]any {
	if err != nil {
		return toolResult("", err)
	}
	text, err := json.Marshal(output)
	if err != nil {
		err = fmt.Errorf("encode tool result: %w", err)
	}
	return toolResult(string(text), err)
}
