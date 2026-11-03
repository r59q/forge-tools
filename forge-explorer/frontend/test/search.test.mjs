import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./modules.mjs";

const { SearchModel } = await load("src/model/search.ts");
const source = { id: "old", version: "root" };
const state = { skipped: 0, truncated: false, canceled: false };
const match = {
  location: {
    source: "old", path: "file.ts", version: "v1",
    range: { startLine: 1, endLine: 1, startColumn: 1, endColumn: 2 },
  },
  preview: "<img src=x>",
};

function setup() {
  const requests = [];
  const lifetime = new AbortController();
  let changes = 0;
  const provider = {
    search(source, query, signal) {
      return new Promise((resolve, reject) => requests.push({ source, query, signal, resolve, reject }));
    },
  };
  const model = new SearchModel(provider, source, lifetime.signal, () => changes++);
  return { model, requests, lifetime, changes: () => changes };
}

test("search keeps query, results and partial state independent of rendering", async () => {
  const { model, requests } = setup();
  await model.run({ text: "  " });
  assert.equal(requests.length, 0);
  const query = { text: "needle", files: true, pathGlob: "*.ts" };
  const pending = model.run(query);
  assert.equal(model.busy, true);
  assert.equal(model.message, "Searching…");
  assert.deepEqual(requests[0].query, query);
  assert.equal(requests[0].source, source);
  const results = { matches: [match], state: { skipped: 2, truncated: true, canceled: true } };
  requests[0].resolve(results);
  await pending;
  assert.deepEqual(model.results, results);
  assert.equal(model.busy, false);
});

test("replacement, cancellation and disposal ignore late provider responses", async () => {
  const { model, requests, lifetime, changes } = setup();
  const first = model.run({ text: "first" });
  const second = model.run({ text: "second" });
  assert.equal(requests[0].signal.aborted, true);
  requests[0].resolve({ matches: [match], state });
  await first;
  assert.equal(model.results, undefined);
  assert.equal(model.busy, true);
  model.cancel();
  assert.equal(requests[1].signal.aborted, true);
  requests[1].resolve({ matches: [match], state });
  await second;
  assert.equal(model.results, undefined);
  assert.equal(model.busy, false);
  assert.match(model.message, /canceled/);
  const third = model.run({ text: "third" });
  lifetime.abort();
  const count = changes();
  assert.equal(requests[2].signal.aborted, true);
  requests[2].reject(new Error("late failure"));
  await third;
  await model.run({ text: "disposed" });
  model.cancel();
  assert.equal(changes(), count);
  assert.equal(requests.length, 3);
});

test("invalid sources, paths and missing versions never become visible results", async () => {
  for (const location of [
    { ...match.location, source: "new" },
    { ...match.location, path: "../escape" },
    { ...match.location, version: "" },
  ]) {
    const { model, requests } = setup();
    const pending = model.run({ text: "needle" });
    requests[0].resolve({ matches: [match, { ...match, location }], state });
    await pending;
    assert.equal(model.results, undefined);
    assert.equal(model.busy, false);
    assert.notEqual(model.message, "Searching…");
    assert.notEqual(model.message, "");
  }
});

test("failed searches recover and instance lifetimes remain independent", async () => {
  const old = setup();
  const next = setup();
  const failed = old.model.run({ text: "needle" });
  old.requests[0].reject(new Error("Search unavailable"));
  await failed;
  assert.equal(old.model.message, "Search unavailable");
  assert.equal(old.model.busy, false);
  const retry = old.model.run({ text: "retry" });
  const other = next.model.run({ text: "next" });
  old.lifetime.abort();
  old.requests[1].resolve({ matches: [], state });
  next.requests[0].resolve({ matches: [match], state });
  await Promise.all([retry, other]);
  assert.equal(next.requests[0].signal.aborted, false);
  assert.deepEqual(next.model.results.matches, [match]);
});
