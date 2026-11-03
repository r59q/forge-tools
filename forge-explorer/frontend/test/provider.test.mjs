import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./modules.mjs";
const { httpProvider, ProviderError } = await load("src/provider.ts");

test("HTTP provider binds every call to the source incarnation and forwards cancellation", async () => {
  const calls = [];
  const provider = httpProvider(
    "https://example.invalid/",
    () => "fixture-token",
    async (url, options) => {
      calls.push({ url, options });
      return new Response(
        JSON.stringify({ sources: [], directory: {}, file: {}, results: {} }),
        { status: 200 },
      );
    },
  );
  const source = { id: "old", version: "root-version" };
  const signal = new AbortController().signal;
  await provider.sources(signal);
  await provider.browse(source, ".", signal);
  await provider.read(source, "path with space.go", "content-version", signal);
  await provider.search(source, { text: "needle", files: true }, signal);
  for (const call of calls) {
    assert.equal(call.options.signal, signal);
    assert.equal(call.options.headers.Authorization, "Bearer fixture-token");
    assert.equal(call.options.redirect, "error");
  }
  assert.match(calls[2].url, /sourceVersion=root-version/);
  assert.match(calls[2].url, /expectedVersion=content-version/);
  assert.match(calls[3].url, /sources\/old\/search/);
  assert.deepEqual(JSON.parse(calls[3].options.body), {
    text: "needle",
    files: true,
    sourceVersion: "root-version",
  });
});

test("stable API statuses become honest errors without reflecting server text", async () => {
  const provider = httpProvider(
    "",
    () => "",
    async () =>
      new Response(
        JSON.stringify({ code: 9, message: "<script>unsafe</script>" }),
        { status: 400 },
      ),
  );
  await assert.rejects(
    provider.read(
      { id: "old", version: "v1" },
      "a",
      "v2",
      new AbortController().signal,
    ),
    (error) => {
      assert.ok(error instanceof ProviderError);
      assert.equal(error.code, 9);
      assert.match(error.message, /Content changed/);
      assert.doesNotMatch(error.message, /script/);
      return true;
    },
  );
});
