import { ChatPanel } from "./panel.js";
import type { ChatProvider } from "./types.js";

export { ChatRequestError, httpChat } from "./provider.js";
export type { HTTPOptions } from "./provider.js";
export type {
  Agent, ChatMetadata, ChatProvider, InputAnswer, IslandPosition, MetadataPatch, NewAgent, Repository, TranscriptEvent,
} from "./types.js";

/** Mount an embeddable chat into an existing element and return its cleanup function. */
export function mountChat(host: HTMLElement, provider: ChatProvider): () => void {
  const chat = new ChatPanel(provider);
  host.replaceChildren(chat.element);
  return () => chat.dispose();
}
