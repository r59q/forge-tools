import { ChatPanel } from "./panel.js";
import type { ChatOptions, ChatProvider } from "./types.js";

export { ChatRequestError, httpChat } from "./provider.js";
export type { HTTPOptions } from "./provider.js";
export type {
  Agent, ChatMetadata, ChatOptions, ChatProvider, InputAnswer, IslandPosition, MetadataPatch, NewAgent,
  Repository, SelectedRepository, TranscriptEvent,
} from "./types.js";

/** Mount an embeddable chat into an existing element and return its cleanup function. */
export function mountChat(host: HTMLElement, provider: ChatProvider, options: ChatOptions = {}): () => void {
  const chat = new ChatPanel(provider, options);
  host.replaceChildren(chat.element);
  return () => chat.dispose();
}
