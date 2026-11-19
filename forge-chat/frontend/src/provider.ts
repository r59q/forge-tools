import type { Agent, ChatMetadata, ChatProvider, InputAnswer, MetadataPatch, NewAgent, TranscriptEvent } from "./types.js";

/** An HTTP status returned by forge-chat. */
export class ChatRequestError extends Error {
  constructor(readonly status: number) {
    super(`Chat request failed (${status})`);
  }
}

/** Configure the service address and how to obtain the browser-facing token. */
export interface HTTPOptions {
  baseURL?: string;
  token: () => string;
}

/** Create a data source for forge-chat's REST and NDJSON API. */
export function httpChat(options: HTTPOptions): ChatProvider {
  const request = async (path: string, signal: AbortSignal, method = "GET", body?: unknown): Promise<Response> => {
    const base = (options.baseURL ?? "").replace(/\/$/, "");
    const headers = new Headers({ Authorization: `Bearer ${options.token()}` });
    if (body !== undefined) headers.set("Content-Type", "application/json");
    const response = await fetch(base + path, { method, headers, signal, body: JSON.stringify(body), redirect: "error" });
    if (!response.ok) {
      await response.body?.cancel();
      throw new ChatRequestError(response.status);
    }
    return response;
  };
  const json = async <T>(path: string, signal: AbortSignal, method?: string, body?: unknown): Promise<T> =>
    (await request(path, signal, method, body)).json();
  const agentPath = (id: string) => `/v1/agents/${encodeURIComponent(id)}`;

  return {
    async agents(signal) {
      return (await json<{ agents?: Agent[] }>("/v1/agents", signal)).agents ?? [];
    },
    async metadata(signal) {
      return (await json<{ chats?: ChatMetadata[] }>("/v1/chat-metadata", signal)).chats ?? [];
    },
    create: (body: NewAgent, signal) => json("/v1/agents", signal, "POST", body),
    get: (id, signal) => json(agentPath(id), signal),
    send: (id, text, signal) => json(agentPath(id) + "/messages", signal, "POST", { text }),
    answer: (id, requestId, body: InputAnswer, signal) =>
      json(agentPath(id) + "/inputs/" + encodeURIComponent(requestId), signal, "POST", body),
    stop: (id, signal) => json(agentPath(id) + "/stop", signal, "POST", {}),
    update: (id, body: MetadataPatch, signal) => json("/v1/chat-metadata/" + encodeURIComponent(id), signal, "PATCH", body),
    async *stream(id, after, signal) {
      const response = await request(agentPath(id) + "/stream?after=" + encodeURIComponent(after), signal);
      if (!response.body) throw new Error("The service did not return a transcript stream");
      yield* readEvents(response.body);
    },
  };
}

/** Decode complete NDJSON records, including records split across network chunks. */
export async function* readEvents(body: ReadableStream<Uint8Array<ArrayBuffer>>): AsyncGenerator<TranscriptEvent> {
  const reader = body.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let pending = "";
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;

      const lines = (pending + decoder.decode(value, { stream: true })).split("\n");
      pending = lines.pop() ?? "";
      for (const line of lines) {
        if (line.trim()) yield parseEvent(line);
      }
    }
    // A cut mid-record must reconnect from the last complete sequence.
    if ((pending + decoder.decode()).trim()) throw new Error("Incomplete transcript record");
  } finally {
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

function parseEvent(line: string): TranscriptEvent {
  const { event } = JSON.parse(line) as { event?: TranscriptEvent };
  if (!event || typeof event.seq !== "string" || !/^\d{1,19}$/.test(event.seq)) {
    throw new Error("Invalid transcript record");
  }
  return event;
}
