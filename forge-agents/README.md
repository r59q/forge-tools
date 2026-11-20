# Forge Agents

Headless agent sessions over the Claude Code, Codex and Gemini CLIs. An
agent is one conversation with one client CLI, run turn by turn in a
workspace, with a durable transcript the same for every client.

## Layout

- [`library/`](library/README.md): agents, their states and turns, the
  client adapters, the shared transcript events, workspaces and the
  executor that runs client processes, as a Go library.
- [`service/`](service/README.md): serves agents over REST, gRPC and an
  NDJSON event stream, with a durable SQLite store and forge-checkouts
  worktrees as workspaces.

## Design

- **One transcript for three clients.** Each adapter turns its client's
  JSON output into the same events: `message`, `tool_call`, `tool_result`,
  `input_request`, `input_answer`, `usage`, `error` and `result`, each with
  a sequence number, time and turn. The raw output is kept apart, per turn,
  for debugging.
- **Turns, not processes.** Each message starts one client process, which
  resumes the client's own session and exits at the end of the turn.
  Nothing runs between turns.
- **Child sessions.** An agent can delegate through its scoped MCP handle
  and can wait for results or end its turn. If a child is still working,
  forge-agents resumes the parent once all its children finish. The parent
  transcript records `child_spawned` and `child_finished`. REST and gRPC
  expose the direct parent, root and depth.
- **People answer questions.** A client asks through the `ask_user` tool,
  and Claude Code also asks for permissions; the agent waits in
  `waiting_input` until the question is answered through the API. Codex and
  Gemini CLI cannot pause for permission headlessly, so the policy decides
  for them up front.
- **No forge access.** forge-agents never commits, pushes, opens pull
  requests or holds forge credentials. A checkout workspace is a
  forge-checkouts worktree; what happens to its changes is up to the
  caller.
- **Credentials stay out.** Client credentials come from the service
  environment. Values of credential-like variables, and the service's own
  tokens, are masked in transcripts, raw output and errors, and the service
  tokens never reach client processes.
- **Bounded.** Agents, running turns, transcript length, event size, line
  size, raw output and turn time are limited. A reached limit is an error
  or a `limit` result in the transcript, never a silent cut.

forge-agents combines one other tool: it may use forge-checkouts, and no
other tool. The library depends only on a workspace interface; the service
wires in the forge-checkouts client.

## Delegating work

| MCP tool | Arguments and result |
| --- | --- |
| `spawn_agent` | `prompt` and `workspace`. Optional `client`, `model` and `effort` inherit from the parent. Returns the child `id`. |
| `list_agents` | Lists direct children with their state, latest result and pending input requests. |
| `send_message` | `id` and `text`. Starts a turn on the child. |
| `wait_agents` | `ids` and optional `timeout_ms` (default 30,000). Returns child states and results with a `timed_out` flag. |
| `stop_agent` | `id`. Stops the child and its descendants. |

Choose `none` for a fresh scratch directory, `checkout` for a new
forge-checkouts worktree of the associated repository's default branch,
or `shared` to use the parent's directory. A checkout needs an ancestor
with a repository checkout or a configured path to a local repository.
Concurrent writes in a shared workspace can overwrite or conflict with
each other. Use `checkout` for children that write.

Children inherit the parent's client/model allowlist and permissions.
`spawn_agent` can narrow these with `allowed_models` (client/model pairs),
`mode` or `ask`. The root's own client/model pair is its default
allowlist. Child input requests are answered through the regular API,
using the child and request IDs.

Each child result uses up to 4 KiB of text from its last completed turn.
An agent with unfinished children enters `waiting_children` without
occupying a turn slot. The continuation receives each child's ID, state
and result. Stopping or deleting the parent stops the subtree. Deletion
keeps descendant records for inspection.
