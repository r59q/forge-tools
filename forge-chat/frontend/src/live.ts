import { isTerminal } from "./chats.js";
import { ChatRequestError } from "./provider.js";
import type { Transcript, TranscriptChange } from "./transcript.js";
import type { Agent, ChatProvider } from "./types.js";

/** Report changes from the current stream to its caller. */
export interface LiveView {
  event(change: TranscriptChange): void;
  agent(agent: Agent): void;
  status(text: string): void;
}

/** Follow a transcript until aborted, deleted, unauthorized, or drained and terminal. */
export async function followChat(
  provider: ChatProvider, id: string, transcript: Transcript, signal: AbortSignal, view: LiveView,
): Promise<void> {
  let delay = 500;
  let draining = false;
  while (!signal.aborted) {
    view.status("");
    try {
      for await (const event of provider.stream(id, transcript.after, signal)) {
        if (signal.aborted) return;
        delay = 500;
        const change = transcript.append(event);
        if (change) view.event(change);
      }
      if (signal.aborted) return;
      const agent = await provider.get(id, signal);
      if (signal.aborted) return;
      view.agent(agent);
      if (isTerminal(agent.state)) {
        if (draining) return;
        // The agent could have become terminal after the old stream ended.
        // Read again with the same cursor to drain any events from that gap.
        draining = true;
        continue;
      }
    } catch (error) {
      if (signal.aborted) return;
      if (error instanceof ChatRequestError && [401, 403, 404].includes(error.status)) {
        view.status(error.status === 404 ? "This chat is no longer available." : "Chat access expired.");
        return;
      }
    }
    view.status("Reconnecting…");
    await pause(delay, signal);
    delay = Math.min(delay * 2, 8_000);
  }
}

function pause(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise(resolve => {
    function finish() {
      clearTimeout(timer);
      signal.removeEventListener("abort", finish);
      resolve();
    }

    const timer = setTimeout(finish, ms);
    signal.addEventListener("abort", finish, { once: true });
    if (signal.aborted) finish();
  });
}
