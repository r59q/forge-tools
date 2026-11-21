import assert from "node:assert/strict";
import { test } from "node:test";
import { addConnection } from "../dist/management.js";

const details = { forge: "gitlab", host: "gitlab.example.test", name: "Work", credential: "synthetic-token" };

test("submits to AddConnection and returns only its public ID", async () => {
  const result = await addConnection(async (url, options) => {
    assert.equal(url, "/v1/connections");
    assert.equal(options.method, "POST");
    assert.equal(options.headers["Content-Type"], "application/json");
    assert.equal(options.cache, "no-store");
    assert.equal(options.redirect, "error");
    assert.deepEqual(JSON.parse(options.body), details);
    return Response.json({ id: "connection-1", name: "Work", login: "operator" });
  }, details);

  assert.deepEqual(result, { connection: { id: "connection-1" } });
});

test("only uses fixed errors for rejected, malformed, and failed responses", async () => {
  const failures = [
    [Response.json({ message: details.credential }, { status: 400 }), /forge rejected/],
    [Response.json({ id: "ignored" }, { status: 401 }), /Operator authorization/],
    [Response.json({ id: "" }), /Could not confirm/],
    [Response.json(null), /Could not confirm/],
    [new Response(details.credential), /Could not confirm/],
  ];
  for (const [response, message] of failures) {
    const result = await addConnection(async () => response, details);
    assert.deepEqual(Object.keys(result), ["error"]);
    assert.match(result.error, message);
    assert.ok(!result.error.includes(details.credential));
  }

  const result = await addConnection(async () => {
    throw new Error(details.credential);
  }, details);
  assert.deepEqual(result, { error: "Could not confirm the connection. Try again." });
});
