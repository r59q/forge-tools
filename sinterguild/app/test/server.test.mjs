import assert from "node:assert/strict";
import { test } from "node:test";
import { agentId, chatToken, operatorToken, startFixture } from "./fixture.mjs";

test("the gateway retains separate service authorization, including streams", async t => {
  const { url, management, chat } = await startFixture(t);
  const operator = { Authorization: `Bearer ${operatorToken}` };
  const chatAccess = { Authorization: `Bearer ${chatToken}` };
  const cases = [
    ["/api/management/v1/connections", {}, 401],
    ["/api/management/v1/connections", chatAccess, 401],
    ["/api/management/v1/connections", operator, 200],
    ["/api/chat/v1/chat-metadata", operator, 401],
    ["/api/chat/v1/chat-metadata", chatAccess, 200],
    [`/api/chat/v1/agents/${agentId}/stream?after=0`, {}, 401],
    [`/api/chat/v1/agents/${agentId}/stream?after=0`, operator, 401],
    ["/api/management/v1/chat-metadata", operator, 404],
    ["/api/chat/v1/connections", chatAccess, 404],
  ];
  for (const [path, headers, status] of cases) {
    const response = await fetch(url + path, {
      headers: { ...headers, Cookie: "private=value", "Grpc-Metadata-Authorization": chatAccess.Authorization },
    });
    await response.body?.cancel();
    assert.equal(response.status, status, path);
  }
  assert.equal(management.requests.length, 3);
  assert.equal(chat.requests.length, 4);
  for (const { headers } of [...management.requests, ...chat.requests]) {
    assert.equal(headers.cookie, undefined);
    assert.equal(headers["grpc-metadata-authorization"], undefined);
  }
});

test("management receives the public origin and still rejects cross-origin changes", async t => {
  const { url, management } = await startFixture(t);
  const response = await fetch(url + "/api/management/v1/connections", {
    method: "POST",
    headers: {
      Authorization: `Bearer ${operatorToken}`,
      Origin: "https://other.example",
      "Content-Type": "application/json",
    },
    body: "{}",
  });
  await response.body?.cancel();
  assert.equal(response.status, 403);
  const [{ headers }] = management.requests;
  assert.equal(headers.origin, "https://other.example");
  assert.equal(headers.host, new URL(url).host);
  assert.deepEqual(management.attempts, []);
});
