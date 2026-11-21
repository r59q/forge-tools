import { createServer } from "node:http";
import { once } from "node:events";
import { createSinterGuild } from "../server.mjs";

export const operatorToken = "fixture-management-operator";
export const chatToken = "fixture-chat-access";
export const forgeToken = "fixture-forge-accepted";
export const target = {
  sourceId: "work-forge",
  path: "example-organization/shared-repository-with-a-long-name",
};
export const personal = { sourceId: "personal-forge", path: target.path };
export const ignored = { sourceId: target.sourceId, path: "example-organization/other-repo" };
export const agentId = "fixture-agent";
export const reply = "The repository uses npm test.";

export async function startFixture(t) {
  const management = managementFixture();
  const chat = chatFixture();
  const managementURL = await serve(t, management.server);
  const chatURL = await serve(t, chat.server);
  const url = await serve(t, createSinterGuild({ managementURL, chatURL }));
  return { url, management, chat };
}

async function serve(t, server) {
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  t.after(() => new Promise(resolve => {
    server.close(resolve);
    server.closeAllConnections();
  }));
  return `http://127.0.0.1:${server.address().port}`;
}

function fixtureServer(token, handler) {
  const requests = [];
  const server = createServer((req, res) => {
    requests.push({ method: req.method, url: req.url, headers: req.headers });
    if (req.headers.authorization !== `Bearer ${token}`) return send(res, 401, {});
    void handler(req, res).catch(error => res.destroy(error));
  });
  return { requests, server };
}

function managementFixture() {
  const attempts = [];
  const connections = [];
  const service = fixtureServer(operatorToken, async (req, res) => {
    if (req.url === "/v1/connections" && req.method === "GET") return send(res, 200, { configured: true, connections });
    if (req.url === "/v1/connections" && req.method === "POST") {
      const { origin, host, "sec-fetch-site": site } = req.headers;
      if (site === "cross-site" || (origin && new URL(origin).host !== host)) return send(res, 403, {});
      const details = await readJSON(req);
      attempts.push(details);
      if (details.credential !== forgeToken) return send(res, 400, { message: `Rejected ${details.credential}` });
      const id = details.name === "Personal" ? personal.sourceId : target.sourceId;
      const connection = { id, name: details.name, status: "ok" };
      connections.push(connection);
      return send(res, 200, connection);
    }
    if (req.url === "/v1/repositories" && req.method === "GET") {
      const repositories = [personal, target, ignored].filter(repo => connections.some(({ id }) => id === repo.sourceId));
      return send(res, 200, { configured: true, repositories });
    }
    return send(res, 404, {});
  });
  return { ...service, attempts };
}

function chatFixture() {
  const creates = [];
  const messages = [];
  const agents = [];
  const events = [];
  const streams = new Set();
  const service = fixtureServer(chatToken, async (req, res) => {
    if (req.url === "/v1/chat-metadata") return send(res, 200, { chats: [] });
    if (req.url === "/v1/agents" && req.method === "GET") return send(res, 200, { agents });
    if (req.url === "/v1/agents" && req.method === "POST") {
      const spec = await readJSON(req);
      creates.push(spec);
      const agent = { id: agentId, state: "created", turns: 0, updatedAt: "2026-01-01T00:00:00Z", ...spec };
      agents.push(agent);
      return send(res, 200, agent);
    }
    if (req.url === `/v1/agents/${agentId}/stream?after=0` && agents.length) {
      res.writeHead(200, { "Content-Type": "application/x-ndjson" });
      res.flushHeaders();
      events.forEach(event => res.write(event));
      streams.add(res);
      res.on("close", () => streams.delete(res));
      return;
    }
    if (req.url === `/v1/agents/${agentId}/messages` && req.method === "POST" && agents.length) {
      const { text } = await readJSON(req);
      messages.push(text);
      events.push(event(1, "user", text), event(2, "assistant", reply));
      streams.forEach(stream => events.forEach(entry => stream.write(entry)));
      const agent = Object.assign(agents[0], { state: "idle", turns: 1 });
      return send(res, 200, agent);
    }
    return send(res, 404, {});
  });
  return { ...service, creates, messages, streams };
}

function event(seq, role, text) {
  return JSON.stringify({ event: { seq: String(seq), kind: "message", turn: 1, role, text } }) + "\n";
}

function send(res, status, value) {
  res.writeHead(status, { "Content-Type": "application/json" }).end(JSON.stringify(value));
}

async function readJSON(req) {
  const parts = [];
  for await (const part of req) parts.push(part);
  return JSON.parse(Buffer.concat(parts).toString());
}
