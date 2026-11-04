import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./modules.mjs";

const { ResultSummary } = await load("src/components/messages.ts");

test("result summaries distinguish empty, singular, plural and partial states", () => {
  const complete = { skipped: 0, truncated: false, canceled: false };
  assert.equal(ResultSummary(0, complete), "No results.");
  assert.equal(ResultSummary(1, complete), "1 result.");
  assert.equal(ResultSummary(2, complete), "2 results.");
  const partial = { skipped: 2, truncated: true, canceled: true };
  assert.match(ResultSummary(1, partial), /1 result.*2 skipped.*Truncated.*Canceled/);
});
