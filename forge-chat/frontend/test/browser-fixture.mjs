import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { once } from "node:events";

export async function fixture() {
  const parent = agent("parent", "claude", "", "2026-01-01T00:00:00Z");
  const child = agent("child", "codex", "parent", "2026-01-02T00:00:00Z");
  const agents = [parent, child];
  const chats = new Map();
  const answers = [];
  const cursors = [];
  const creates = [];
  const messages = [];
  const transcripts = new Map([[parent.id, [line(1, "message", { role: "user", text: "Child result: the build passes" })]]]);
  const streams = new Map();

  function write(id, seq, kind, data) {
    const entry = line(seq, kind, data);
    transcripts.get(id).push(entry);
    streams.get(id)?.write(entry);
  }

  function startStream(res, id, after) {
    res.writeHead(200, { "Content-Type": "application/x-ndjson" });
    res.flushHeaders();
    const events = transcripts.get(id);
    if (!events) return res.end();
    if (id === parent.id) cursors.push(after);
    events.slice(Number(after)).forEach(entry => res.write(entry));
    streams.set(id, res);
    res.on("close", () => { if (streams.get(id) === res) streams.delete(id); });
  }

  async function turn(req, current) {
    const { id } = current;
    const { text } = await body(req);
    messages.push({ id, text });
    current.state = "running";
    if (id === parent.id) {
      write(id, 2, "message", { role: "user", text });
      write(id, 3, "tool_call", { toolId: "read", toolName: "Read", input: "README.md" });
    } else {
      const seq = transcripts.get(id).length + 1;
      write(id, seq, "message", { role: "user", text });
      write(id, seq + 1, "message", { role: "assistant", text: `Reply from ${id}` });
      current.state = "idle";
    }
  }

  async function handle(req, res) {
    const url = new URL(req.url, "http://127.0.0.1");
    if (!url.pathname.startsWith("/v1/")) return serveFile(url, res);
    if (req.headers.authorization !== "Bearer browser-test") return send(res, {}, 401);

    const [, , resource, id, action, input] = url.pathname.split("/");
    if (resource === "chat-metadata") {
      if (!id) return send(res, { chats: [...chats.values()] });
      const metadata = { ...chats.get(id), ...(await body(req)), id };
      chats.set(id, metadata);
      return send(res, metadata);
    }
    if (!id && req.method === "GET") return send(res, { agents });
    if (!id && req.method === "POST") {
      const spec = await body(req);
      const created = { ...agent(`new-${creates.length + 1}`, spec.client, "", new Date().toISOString()), ...spec, state: "created" };
      creates.push(spec);
      agents.push(created);
      transcripts.set(created.id, []);
      return send(res, created);
    }
    const current = agents.find(entry => entry.id === id);
    if (!current) return send(res, {}, 404);
    if (action === "stream") return startStream(res, id, url.searchParams.get("after"));
    if (action === "messages") await turn(req, current);
    if (action === "inputs") {
      const answer = await body(req);
      answers.push({ input, ...answer });
      write(id, input === "question" ? 6 : 8, "input_answer", { requestId: input, ...answer });
      if (input === "question") {
        write(id, 7, "input_request", { requestId: "permission", requestKind: "permission", toolName: "Read", input: "build.log" });
      } else {
        write(id, 9, "message", { role: "assistant", text: "See [the diff](https://example.test/diff) <img src=/bad>" });
        write(id, 10, "result", { status: "success" });
        current.state = "idle";
      }
    }
    if (action === "stop") {
      current.state = "stopped";
      streams.get(id)?.end();
    }
    return send(res, current);
  }

  const server = createServer((req, res) => {
    void handle(req, res).catch(error => res.destroy(error));
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  return {
    url: `http://127.0.0.1:${server.address().port}`,
    answers, cursors, creates, agents, messages,
    close() { server.closeAllConnections(); server.close(); },
    interrupt() {
      const stream = streams.get(parent.id);
      streams.delete(parent.id);
      stream.end('{"event":');
      write(parent.id, 4, "tool_result", { toolId: "read", text: "Build steps" });
      write(parent.id, 5, "input_request", { requestId: "question", requestKind: "question", text: "Which target?" });
    },
  };
}

function agent(id, client, parent, updatedAt) {
  return { id, client, parent, updatedAt, state: "idle", turns: 1, model: "", effort: "" };
}

function line(seq, kind, data) {
  return JSON.stringify({ event: { seq: String(seq), kind, turn: 1, ...data } }) + "\n";
}

function send(res, value, status = 200) {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(value));
}

async function body(req) {
  const parts = [];
  for await (const part of req) parts.push(part);
  return JSON.parse(Buffer.concat(parts).toString());
}

async function serveFile(url, res) {
  if (url.pathname === "/selected-repositories") {
    const contents = await readFile(new URL("selected-repositories.html", import.meta.url));
    res.writeHead(200, { "Content-Type": "text/html" });
    return res.end(contents);
  }
  const path = url.pathname === "/" ? "/index.html" : url.pathname;
  const type = path.endsWith(".js") ? "text/javascript" : path.endsWith(".css") ? "text/css" : "text/html";
  if (!/^\/(?:index\.html|(?:app|src)\/[a-z.-]+)$/.test(path)) return send(res, {}, 404);
  const contents = await readFile(new URL("../dist/web" + path, import.meta.url));
  res.writeHead(200, { "Content-Type": type });
  res.end(contents);
}
