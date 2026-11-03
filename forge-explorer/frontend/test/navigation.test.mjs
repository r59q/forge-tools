import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./modules.mjs";
const { Navigation, wholeFile, validateRange } =
  await load("src/navigation.ts");
const source = { id: "old", version: "old-root" };
const loc = (path = "a.go", version = "v1", side = "old") => ({
  source: side,
  version,
  path,
  range: wholeFile(),
});
function setup(options = {}) {
  const calls = [];
  const provider = {
    async read(source, path, expected, signal) {
      calls.push({ source, path, expected, signal });
      if (expected && expected !== "v1") throw new Error("Stale content");
      return { location: loc(path, "v1", source.id), text: "one\ntwo" };
    },
  };
  let changes = 0;
  const model = new Navigation(provider, source, options, () => changes++);
  return { model, calls, provider, changes: () => changes };
}

test("navigation, selection, tabs and history preserve origin and versions", async () => {
  const selections = [];
  const { model, calls } = setup({
    onSelection: (location) => selections.push(location),
  });
  await model.open("a.go");
  await model.open("b.go");
  assert.equal(model.tabs.length, 2);
  await model.back();
  assert.equal(model.location.path, "a.go");
  await model.forward();
  assert.equal(model.location.path, "b.go");
  assert.equal(calls[2].expected, "v1");
  model.select({ startLine: 2, startColumn: 1, endLine: 2, endColumn: 4 });
  assert.equal(selections[0].source, "old");
  selections[0].range.startLine = 999;
  assert.equal(model.location.range.startLine, 2);
  await model.closeTab("b.go");
  assert.equal(model.location.path, "a.go");
  await model.back();
  await model.open("c.go");
  assert.equal(model.index, model.history.length - 1);
});

test("stale results and cross-source navigation never advance history", async () => {
  const { model, calls } = setup();
  await model.open("a.go");
  await model.open(loc("b.go", "stale"));
  assert.match(model.error, /Stale/);
  assert.equal(model.history.length, 1);
  await model.refresh();
  assert.equal(model.location.path, "b.go");
  const count = calls.length;
  await model.open(loc("same.go", "v1", "new"));
  await model.open("../escape");
  assert.equal(calls.length, count);
  assert.equal(model.location.source, "old");
});

test("wrong provider identities and invalid ranges are rejected", async () => {
  const { model, provider } = setup();
  provider.read = async () => ({
    location: loc("a.go", "v1", "new"),
    text: "one",
  });
  await model.open("a.go");
  assert.equal(model.file, undefined);
  provider.read = async () => ({ location: loc(), text: "one" });
  await model.open({
    ...loc(),
    range: { startLine: 5, endLine: 5, startColumn: 1, endColumn: 2 },
  });
  assert.match(model.error, /range/);
  assert.throws(() =>
    validateRange("x", {
      startLine: 1,
      endLine: 1,
      startColumn: 3,
      endColumn: 4,
    }),
  );
});

test("late responses, cancellation and disposal cannot mutate the instance", async () => {
  const { model, provider, changes } = setup();
  const pending = [];
  provider.read = (_source, path, _version, signal) =>
    new Promise((resolve) => pending.push({ path, signal, resolve }));
  const a = model.open("a.go");
  const b = model.open("b.go");
  assert.equal(pending[0].signal.aborted, true);
  pending[1].resolve({ location: loc("b.go"), text: "b" });
  await b;
  pending[0].resolve({ location: loc("a.go"), text: "a" });
  await a;
  assert.equal(model.location.path, "b.go");
  const c = model.open("c.go");
  model.dispose();
  const count = changes();
  assert.equal(pending[2].signal.aborted, true);
  pending[2].resolve({ location: loc("c.go"), text: "c" });
  await c;
  assert.equal(changes(), count);
  assert.equal(model.location.path, "b.go");
});

test("two instances and host interception keep independent stacks", async () => {
  const intercepted = [];
  const a = setup({
    onNavigate: (location) => {
      intercepted.push(location);
      return false;
    },
  });
  const b = setup();
  await a.model.open("a.go");
  await b.model.open("b.go");
  assert.equal(a.calls.length, 0);
  assert.equal(a.model.tabs.length, 0);
  assert.equal(b.model.tabs.length, 1);
  assert.equal(intercepted[0].source, "old");
  a.model.dispose();
  await b.model.open("c.go");
  assert.equal(b.model.history.length, 2);
});
