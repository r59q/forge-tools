import assert from "node:assert/strict";
import { test } from "node:test";
import { recentChats, sessionTree } from "../dist/src/chats.js";
import { Transcript } from "../dist/src/transcript.js";

function event(seq, kind, changes = {}) {
  return { seq: String(seq), kind, turn: 1, ...changes };
}

test("the last activity is the title, with file reads grouped between messages", () => {
  const transcript = new Transcript();
  transcript.append(event(1, "message", { role: "user", text: "Review this" }));
  transcript.append(event(2, "tool_call", { toolName: "Read", toolId: "r1" }));
  assert.equal(transcript.blocks[1].title, "Read · Call");
  const change = transcript.append(event(3, "tool_result", { toolId: "r1", text: "contents" }));
  assert.equal(change.index, 1);
  assert.equal(change.block.title, "Read · Result · contents");
  assert.deepEqual(change.block.entries.map(entry => entry.seq), ["2", "3"]);
  transcript.append(event(4, "message", { role: "assistant", text: "Done" }));
  transcript.append(event(5, "tool_call", { toolName: "Edit" }));
  assert.equal(transcript.blocks.length, 4);
  assert.equal(transcript.blocks[3].title, "Edit · Call");
});

test("final results can reuse message text and replayed sequences are ignored without rounding", () => {
  const transcript = new Transcript();
  const message = event("9007199254740992", "message", { role: "assistant", text: "Conclusion" });
  const result = event("9007199254740993", "result", { text: "Conclusion" });
  transcript.append(message);
  transcript.append(result);
  assert.equal(transcript.repeatsMessage(result), true);
  assert.equal(transcript.append(message), undefined);
  assert.equal(transcript.after, result.seq);
  assert.equal(transcript.blocks.length, 2);
});

test("answers, expired requests and turn results all settle pending input", () => {
  const transcript = new Transcript();
  for (const [index, id] of ["question", "permission", "third"].entries()) {
    transcript.append(event(index + 1, "input_request", { requestId: id }));
  }

  assert.equal(transcript.pending.size, 3);
  assert.deepEqual(transcript.append(event(4, "input_answer", { requestId: "permission" })).settled, ["permission"]);
  assert.deepEqual(transcript.append(event(5, "error", { requestId: "question" })).settled, ["question"]);
  assert.deepEqual(transcript.append(event(6, "result")).settled, ["third"]);
  assert.equal(transcript.pending.size, 0);
});

test("activity sorting preserves nanoseconds and archiving leaves family navigation intact", () => {
  const agents = [
    { id: "grandchild", parent: "child", updatedAt: "2026-01-01T00:00:00Z" },
    { id: "child", parent: "parent", updatedAt: "2026-01-01T00:00:00.000000002Z" },
    { id: "parent", parent: "", updatedAt: "2026-01-01T00:00:00.000000001Z" },
    { id: "orphan", parent: "missing", updatedAt: "2026-01-02T00:00:00Z" },
  ];
  const metadata = new Map([["child", { archived: true }]]);
  assert.deepEqual(recentChats(agents, metadata, false).map(a => a.id), ["orphan", "parent", "grandchild"]);
  assert.deepEqual(recentChats(agents, metadata, true).map(a => a.id), ["child"]);
  const tree = sessionTree(agents, "grandchild");
  assert.equal(tree.agent.id, "parent");
  assert.equal(tree.children[0].agent.id, "child");
  assert.equal(tree.children[0].children[0].agent.id, "grandchild");
  assert.equal(sessionTree(agents, "orphan").agent.id, "orphan");
});
