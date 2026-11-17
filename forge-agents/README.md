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
