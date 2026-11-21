import assert from "node:assert/strict";
import { test } from "node:test";
import { listRepositories } from "../dist/repositories.js";
import { readRepositorySelection, RepositorySelection } from "../dist/repository-selection.js";

const personal = { sourceId: "Personal-1", path: "Owner/Repo" };
const work = { sourceId: "work-2", path: personal.path };

test("lists repositories through management and returns their exact identities", async () => {
  const result = await listRepositories(async (url, options) => {
    assert.equal(url, "/v1/repositories");
    assert.deepEqual(options, { method: "GET", cache: "no-store", redirect: "error" });
    return Response.json({
      configured: true,
      repositories: [{ ...personal, host: "example.test", credential: "fixture-token" }, work],
    });
  });
  assert.deepEqual(result, { state: "ready", repositories: [personal, work] });
});

test("distinguishes empty, unconfigured, malformed and failed listings", async () => {
  const states = [
    [Response.json({ configured: true, repositories: [] }), "empty"],
    [Response.json({ configured: false, repositories: [] }), "unconfigured"],
    [Response.json({ repositories: [] }), "failed"],
    [Response.json({ configured: true }), "failed"],
    [Response.json({ configured: true, repositories: [personal, { path: personal.path }] }), "failed"],
    [Response.json({ message: "fixture error" }, { status: 503 }), "failed"],
    [new Response("invalid JSON"), "failed"],
  ];
  for (const [response, state] of states) {
    assert.deepEqual(await listRepositories(async () => response), { state });
  }
  assert.deepEqual(await listRepositories(async () => { throw new Error("fixture error"); }), { state: "failed" });
});

test("restores only visible exact identities and toggles equal paths independently", () => {
  const saved = JSON.stringify([
    { ...work, credential: "fixture-token" },
    { ...personal, path: "owner/repo" },
    { sourceId: "removed", path: personal.path },
  ]);
  const selection = new RepositorySelection([personal, work], () => saved);
  assert.deepEqual(selection.selected(), [work]);
  selection.set(personal, true);
  assert.deepEqual(selection.selected(), [personal, work]);
  selection.set(work, false);
  assert.deepEqual(selection.selected(), [personal]);
});

test("starts with no selection when saved data is unreadable", () => {
  for (const read of [() => "invalid JSON", () => { throw new Error("storage unavailable"); }]) {
    assert.deepEqual(new RepositorySelection([personal, work], read).selected(), []);
  }
});

test("loads a saved preference without requiring a live listing", () => {
  const saved = JSON.stringify([work, { ...personal, credential: "fixture-token" }, work, { path: "invalid" }]);
  assert.deepEqual(readRepositorySelection(() => saved), [work, personal]);
  for (const value of [null, "invalid JSON", JSON.stringify(personal)]) {
    assert.deepEqual(readRepositorySelection(() => value), []);
  }
});
