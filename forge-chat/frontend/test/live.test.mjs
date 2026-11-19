import assert from "node:assert/strict";
import { test } from "node:test";
import { followChat } from "../dist/src/live.js";
import { ChatRequestError, readEvents } from "../dist/src/provider.js";
import { Transcript } from "../dist/src/transcript.js";

async function collect(events) {
  const got = [];
  for await (const event of events) got.push(event);
  return got;
}

test("NDJSON retains UTF-8 and complete events across chunk boundaries", async () => {
  const line = new TextEncoder().encode('{"event":{"seq":"1","text":"café"}}\n{"event":{"seq":"2"}}\n');
  const body = new ReadableStream({
    start(controller) {
      for (const byte of line) controller.enqueue(Uint8Array.of(byte));
      controller.close();
    },
  });
  assert.deepEqual(await collect(readEvents(body)), [{ seq: "1", text: "café" }, { seq: "2" }]);
});

test("a partial line at EOF is reported after complete events", async () => {
  const body = new Blob(['{"event":{"seq":"1"}}\n{"event":']).stream();
  const events = readEvents(body);
  assert.equal((await events.next()).value.seq, "1");
  await assert.rejects(events.next(), /Incomplete transcript record/);
});

test("reconnect from the last received sequence and drain a terminal turn", async () => {
  const attempts = [];
  const statuses = [];
  const provider = {
    async *stream(_id, after) {
      attempts.push(after);
      if (attempts.length === 1) {
        yield { seq: "1", kind: "message", role: "user", text: "Start", turn: 1 };
        throw new TypeError("Disconnected");
      }
      if (attempts.length === 2) return;
      yield { seq: "2", kind: "result", status: "stopped", turn: 1 };
    },
    async get() { return { state: "stopped" }; },
  };
  const transcript = new Transcript();
  await followChat(provider, "chat", transcript, AbortSignal.timeout(3_000), {
    event() {}, agent() {}, status: status => statuses.push(status),
  });
  assert.deepEqual(attempts, ["0", "1", "1"]);
  assert.equal(transcript.after, "2");
  assert.ok(statuses.includes("Reconnecting…"));
});

test("missing streams stop retrying", async () => {
  const statuses = [];
  const provider = {
    async *stream() { throw new ChatRequestError(404); },
  };
  await followChat(provider, "chat", new Transcript(), AbortSignal.timeout(2_000), {
    event() {}, agent() {}, status: status => statuses.push(status),
  });
  assert.equal(statuses.at(-1), "This chat is no longer available.");
});
