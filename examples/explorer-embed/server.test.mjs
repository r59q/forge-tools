import assert from "node:assert/strict";
import { createServer } from "node:http";
import { after, test } from "node:test";
import { createHost } from "./server.mjs";

const TOKEN = "service-token";
const servers = [];

after(() => servers.forEach((server) => server.close()));

// listen serves handler on a free loopback port and returns its base URL.
async function listen(handler) {
  const server = createServer(handler);
  servers.push(server);
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  return `http://127.0.0.1:${server.address().port}`;
}

// fakeExplorer records requests and answers like the explorer service.
async function fakeExplorer() {
  const calls = [];
  const url = await listen(async (request, response) => {
    let body = "";
    for await (const chunk of request) body += chunk;

    calls.push({ method: request.method, url: request.url, auth: request.headers.authorization, body });
    response.writeHead(200, { "Content-Type": "application/json" });
    response.end('{"sources":[]}');
  });
  return { url, calls };
}

async function host(explorerURL) {
  return listen(createHost({ explorerURL, token: TOKEN }));
}

test("forwards reads with the service token instead of the browser's", async () => {
  const explorer = await fakeExplorer();
  const base = await host(explorer.url);

  const response = await fetch(`${base}/explorer-api/v1/explorer/sources?x=1`, {
    headers: { Authorization: "Bearer from-browser" },
  });

  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { sources: [] });
  assert.deepEqual(explorer.calls, [
    { method: "GET", url: "/v1/explorer/sources?x=1", auth: `Bearer ${TOKEN}`, body: "" },
  ]);
});

test("forwards search bodies", async () => {
  const explorer = await fakeExplorer();
  const base = await host(explorer.url);
  const query = '{"sourceVersion":"v","text":"Greeting"}';

  await fetch(`${base}/explorer-api/v1/explorer/sources/sample/search`, {
    method: "POST",
    body: query,
  });

  assert.equal(explorer.calls[0].method, "POST");
  assert.equal(explorer.calls[0].body, query);
});

test("forwards only explorer API reads and searches", async () => {
  const explorer = await fakeExplorer();
  const base = await host(explorer.url);

  const outside = await fetch(`${base}/explorer-api/healthz`);
  const deleted = await fetch(`${base}/explorer-api/v1/explorer/sources`, { method: "DELETE" });

  assert.equal(outside.status, 404);
  assert.equal(deleted.status, 405);
  assert.equal(explorer.calls.length, 0);
});

test("rejects oversized request bodies", async () => {
  const explorer = await fakeExplorer();
  const base = await host(explorer.url);

  const response = await fetch(`${base}/explorer-api/v1/explorer/sources/sample/search`, {
    method: "POST",
    body: "x".repeat((1 << 20) + 1),
  });

  assert.equal(response.status, 413);
  assert.equal(explorer.calls.length, 0);
});

test("reports an unreachable explorer service", async () => {
  const base = await host("http://127.0.0.1:1");

  const response = await fetch(`${base}/explorer-api/v1/explorer/sources`);

  assert.equal(response.status, 502);
});

test("serves the page and the installed @r59q/explorer module", async () => {
  const base = await host("http://127.0.0.1:1");

  const page = await fetch(`${base}/`);
  const module = await fetch(`${base}/vendor/explorer.js`);

  assert.match(await page.text(), /"@r59q\/explorer": "\/vendor\/explorer.js"/);
  assert.match(module.headers.get("content-type"), /^text\/javascript/);
  assert.match(await module.text(), /createExplorer/);
});

test("serves no other files", async () => {
  const base = await host("http://127.0.0.1:1");

  for (const path of ["/server.mjs", "/public/app.js", "/../package.json", "/.env"]) {
    assert.equal((await fetch(base + path)).status, 404, path);
  }
});
