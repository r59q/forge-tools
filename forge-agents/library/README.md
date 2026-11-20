# forge-agents library

Runs headless agent sessions over the Claude Code, Codex and Gemini CLIs,
as a Go library. It has no dependencies beyond the standard library.

| Package      | What it holds                                                                      |
| ------------ | ---------------------------------------------------------------------------------- |
| `agent`      | The manager: agents, their trees and turns, input requests, bounds and the store. |
| `adapter`    | The client interface, policies, and `claude`, `codex` and `gemini` adapters.       |
| `transcript` | The events every adapter produces.                                                 |
| `workspace`  | Scratch, path and checkout workspaces, behind a provider interface.                |
| `executor`   | The executor interface and `Local`, which runs processes on this host.             |
| `mcp`        | The scoped MCP tool server for input requests and child sessions.                   |
| `redact`     | Masks credential values in events, raw output and errors.                          |

## Build and test

Requires Go 1.27 or newer. From this directory:

```sh
go vet ./...
go test -race ./...
```

No test calls a model. Adapter tests replay hand-built client output from
`adapter/testdata/<client>/*.jsonl` and compare the events with the
`.golden.json` files next to them; run `go test ./adapter -update` to
rewrite those. Manager tests run fake CLI scripts, including scripts that
make MCP calls.

## Use

```go
m, err := agent.Open(ctx, agent.Config{
	Store:      agent.NewMemoryStore(),
	Clients:    map[string]agent.Client{"claude": {Adapter: claude.Adapter{}, Path: "/usr/local/bin/claude"}},
	Executor:   executor.Local{},
	Workspaces: workspace.Local{ScratchDir: "/var/lib/agents/scratch"},
	Env:        os.Environ(),
	RunDir:     "/var/lib/agents/run",
})
a, err := m.Create(ctx, agent.Spec{Client: "claude", Policy: adapter.Policy{Mode: adapter.ModeEdit}})
a, err = m.Send(ctx, a.ID, "Write a haiku to haiku.txt")
err = m.Watch(ctx, a.ID, 0, func(e transcript.Event) error { fmt.Println(e.Kind, e.Text); return nil })
```

`Watch` sends the transcript from a sequence number, then each new event,
until the agent is stopped or failed or the context ends. `Transcript`
reads a page, and `Output` the raw output of one of the latest turns.

## Agents

An agent is created in `created`. Each `Send` starts a turn: the agent is
`running` while the client process runs, `waiting_input` while it waits
for an answer, and `idle` once the turn ends. A turn ends with a `result`
event whose status is `success`, `error`, `cancelled`, `timeout`, `stopped`
or `limit`. Errors and timeouts leave the agent `idle`, with `Error` set,
so the conversation can go on. `Cancel` kills the process and returns the
agent to `idle`; `Stop` kills it and stops the agent for good. An agent
whose transcript is full ends `failed`.

When the manager opens, it ends turns a previous run left in progress with
an `error` result and returns their agents to `idle`. A parent already
in `waiting_children` can continue with those results. See the
[delegation tools](../README.md#delegating-work).

## Clients

Each turn runs one client process in the workspace, with the prompt on
standard input. Later turns resume the session the client reported.

| Client      | Command                                          | Permissions                                                 |
| ----------- | ------------------------------------------------ | ----------------------------------------------------------- |
| Claude Code | `claude -p --output-format stream-json --resume` | `read_only`, `edit` or `full`; with `ask`, the user decides |
| Codex       | `codex exec --json`, then `codex exec resume`    | The sandbox from the mode, decided up front                 |
| Gemini CLI  | `gemini --output-format stream-json --resume`    | The approval mode from the mode, decided up front           |

Every client gets the `ask_user` tool over MCP, which records an
`input_request` and waits for `Manager.Answer`. With `Policy.Ask`, Claude
Code's permission prompts go to the `approve` tool the same way. Codex and
Gemini CLI cannot pause for permission headlessly, so they reject `ask`.
The MCP server listens on loopback, and each turn has its own token.

## Workspaces

`workspace.Local` prepares:

- `scratch`: a new empty directory, removed with the agent.
- `path`: an existing directory inside one of `Roots`.
- `checkout`: a new worktree from `Checkouts`, of a repository at a
  reference; an empty reference is the default branch. The checkout ID is
  kept on the agent. Deleting the agent keeps the checkout unless removal
  is asked for, and forge-checkouts refuses to remove one with
  uncommitted changes.

## Limits

`Bounds` limit the manager, and `Limits` one agent, never above the bounds:

| Bound            | Default | When reached                                       |
| ---------------- | ------- | -------------------------------------------------- |
| `MaxAgents`      | 100     | `Create` fails with `ErrLimit`.                    |
| `MaxRunning`     | 4       | `Send` fails with `ErrLimit`.                      |
| `MaxEvents`      | 10000   | The turn ends with a `limit` result, agent failed. |
| `MaxEventBytes`  | 256 KiB | Event text and input are cut, marked `truncated`.  |
| `MaxLineBytes`   | 4 MiB   | The line is skipped, with an `error` event.        |
| `MaxOutputBytes` | 4 MiB   | Raw output is cut, marked `truncated`.             |
| `KeepOutputs`    | 10      | Older turns lose their raw output.                 |
| `TurnTimeout`    | 1h      | The turn ends with a `timeout` result.             |
| `MaxDepth`       | 2       | A deeper child fails to start.                     |
| `MaxChildren`    | 4       | The parent cannot add another direct child.        |
| `MaxTreeAgents`  | 16      | The tree cannot add another agent.                  |
| `TreeTimeout`    | 24h     | The shared deadline expires.                       |
| `MaxTreeTokens`  | 2000000 | Reported input and output exhaust the tree budget. |

Tree size counts existing records, including stopped agents. The tree's
clock starts at its first turn and continues between turns. The deadline
and reported tokens are stored with the root. Token use is enforced when
the client reports it, which may be at the end of a turn.

## Credentials

Client credentials come from `Config.Env`, which is never recorded. The
values of variables whose names hold `KEY`, `TOKEN`, `SECRET`, `PASSWORD`,
`CREDENTIAL` or `AUTH`, and each turn's MCP token, are masked in events,
raw output and errors.

## License

[MIT](../../LICENSE)
