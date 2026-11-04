import { test } from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createDevServer, devPage } from "../scripts/dev-server.mjs";

async function listen(handler) {
  const server = createServer(handler);
  await new Promise((done) => server.listen(0, "127.0.0.1", done));

  return { server, url: `http://127.0.0.1:${server.address().port}` };
}

async function setup(t) {
  const root = await mkdtemp(join(tmpdir(), "explorer-dev-"));
  await writeFile(join(root, "index.html"), "<html>app</html>");
  await writeFile(join(root, "app.js"), "export {};");
  const client = join(root, "client.js");
  await writeFile(client, "// client");

  const calls = [];
  const upstream = await listen(async (request, response) => {
    let body = "";
    for await (const chunk of request) body += chunk;
    calls.push({ method: request.method, url: request.url, auth: request.headers.authorization, body });
    response.writeHead(200, { "Content-Type": "application/json" });
    response.end('{"ok":true}');
  });

  const dev = createDevServer({ root, upstream: `${upstream.url}/`, token: "secret", client });
  const host = await listen(dev.handle);
  t.after(() => {
    upstream.server.close();
    host.server.closeAllConnections();
    host.server.close();
  });

  return { url: host.url, calls, dev };
}

test("devPage loads the dev client after the app", () => {
  const page = devPage('<head><script src="/app.js"></script></head>');

  assert.match(page, /app\.js"><\/script>\s+<script type="module" src="\/dev\/client\.js"><\/script>\s+<\/head>/);
});

test("serves built files and the dev client", async (t) => {
  const { url } = await setup(t);

  const index = await fetch(`${url}/`);
  assert.equal(index.status, 200);
  assert.equal(index.headers.get("content-type"), "text/html; charset=utf-8");
  assert.equal(await index.text(), "<html>app</html>");

  const client = await fetch(`${url}/dev/client.js`);
  assert.equal(await client.text(), "// client");
  assert.equal((await fetch(`${url}/missing.js`)).status, 404);
});

test("does not serve files outside the build folder", async (t) => {
  const { url } = await setup(t);

  const response = await fetch(`${url}/%2e%2e/package.json`);

  assert.equal(response.status, 404);
});

test("forwards API calls with the service token", async (t) => {
  const { url, calls } = await setup(t);

  const get = await fetch(`${url}/v1/explorer/sources?x=1`, { headers: { Authorization: "Bearer dev-server" } });
  const post = await fetch(`${url}/v1/explorer/sources/a/search`, { method: "POST", body: '{"text":"x"}' });

  assert.equal(get.status, 200);
  assert.deepEqual(await post.json(), { ok: true });
  assert.deepEqual(calls, [
    { method: "GET", url: "/v1/explorer/sources?x=1", auth: "Bearer secret", body: "" },
    { method: "POST", url: "/v1/explorer/sources/a/search", auth: "Bearer secret", body: '{"text":"x"}' },
  ]);
});

test("rejects other methods and large bodies", async (t) => {
  const { url, calls } = await setup(t);

  const removed = await fetch(`${url}/v1/explorer/sources`, { method: "DELETE" });
  const large = await fetch(`${url}/v1/explorer/sources/a/search`, { method: "POST", body: "x".repeat((1 << 20) + 1) });

  assert.equal(removed.status, 405);
  assert.equal(large.status, 413);
  assert.equal(calls.length, 0);
});

test("reports an unreachable explorer service", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "explorer-dev-"));
  const dev = createDevServer({ root, upstream: "http://127.0.0.1:1", token: "secret", client: "" });
  const host = await listen(dev.handle);
  t.after(() => host.server.close());

  const response = await fetch(`${host.url}/v1/explorer/sources`);

  assert.equal(response.status, 502);
});

test("reload notifies open pages", async (t) => {
  const { url, dev } = await setup(t);
  const response = await fetch(`${url}/dev/reload`);
  const reader = response.body.getReader();
  const decoder = new TextDecoder();

  assert.equal(response.headers.get("content-type"), "text/event-stream");
  assert.match(decoder.decode((await reader.read()).value), /connected/);

  dev.reload();

  assert.match(decoder.decode((await reader.read()).value), /event: reload/);
  await reader.cancel();
});
