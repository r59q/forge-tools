import { ChatView } from "./chat-view.js";
import { ChatCatalog } from "./chats.js";
import { createForm } from "./create.js";
import { element } from "./dom.js";
import { followChat } from "./live.js";
import { Sidebar } from "./sidebar.js";
import { Transcript } from "./transcript.js";
import { TranscriptView } from "./transcript-view.js";
import type { Agent, ChatOptions, ChatProvider, MetadataPatch } from "./types.js";

interface ActiveChat {
  view: ChatView;
  stream: AbortController;
}

export class ChatPanel {
  readonly element: HTMLElement;
  private readonly abort = new AbortController();
  private readonly error = element("p", "chat-error");
  private readonly catalogError = element("p", "chat-error");
  private readonly main = element("main", "chat-main");
  private readonly body = element("div", "chat-body");
  private readonly sidebar: Sidebar;
  private readonly timer: ReturnType<typeof setInterval>;
  private readonly catalog = new ChatCatalog();
  private active?: ActiveChat;
  private refreshing = false;
  private catalogKey = "";

  constructor(private readonly provider: ChatProvider, options: ChatOptions) {
    const create = createForm(spec => this.perform(async signal => {
      const agent = await provider.create(spec, signal);
      this.accept(agent);
      this.select(agent.id);
    }), options.repositories);
    this.sidebar = new Sidebar(create, id => this.select(id), () => {
      this.catalog.showArchived = !this.catalog.showArchived;
      this.renderCatalog();
    });
    this.error.setAttribute("role", "alert");
    this.catalogError.setAttribute("role", "status");
    this.body.append(element("p", "empty-chat", "Select a chat or start a new one."));
    this.main.append(this.catalogError, this.error, this.body);
    this.element = element("div", "forge-chat", this.sidebar.element, this.main);
    this.renderCatalog();
    void this.refresh();
    this.timer = setInterval(() => void this.refresh(), 4_000);
  }

  dispose(): void {
    clearInterval(this.timer);
    this.abort.abort();
    this.active?.stream.abort();
    this.element.remove();
  }

  private select(id: string): void {
    if (this.abort.signal.aborted || !this.catalog.select(id)) return;
    this.active?.stream.abort();
    this.error.textContent = "";
    const stream = new AbortController();
    const transcript = new Transcript();
    const display = new TranscriptView(transcript, (requestId, answer) => this.perform(async signal => {
      this.accept(await this.provider.answer(id, requestId, answer, signal));
    }, id));
    const view = new ChatView(display.element,
      text => this.perform(async signal => this.accept(await this.provider.send(id, text, signal)), id),
      target => this.perform(async signal => this.accept(await this.provider.stop(target, signal)), id),
      (target, value) => this.patch(target, value),
      target => this.select(target));
    this.active = { view, stream };
    this.body.replaceChildren(view.element);
    this.renderCatalog();
    void followChat(this.provider, id, transcript, stream.signal, {
      event: change => display.render(change),
      agent: current => this.accept(current),
      status: text => { view.status.textContent = text; },
    });
  }

  private async refresh(): Promise<void> {
    if (this.refreshing || this.abort.signal.aborted) return;
    this.refreshing = true;
    const version = this.catalog.version;
    try {
      const [agents, metadata] = await Promise.all([
        this.provider.agents(this.abort.signal), this.provider.metadata(this.abort.signal),
      ]);
      if (this.abort.signal.aborted) return;
      this.catalogError.textContent = "";
      this.catalog.replace(agents, metadata, version);
      this.renderCatalog();
    } catch (error) {
      if (!this.abort.signal.aborted) this.catalogError.textContent = failure(error);
    } finally {
      this.refreshing = false;
    }
  }

  private async perform(action: (signal: AbortSignal) => Promise<void>, id?: string): Promise<boolean> {
    this.error.textContent = "";
    try {
      await action(this.abort.signal);
      return !this.abort.signal.aborted;
    } catch (error) {
      if (!this.abort.signal.aborted && (!id || this.catalog.active?.id === id)) this.error.textContent = failure(error);
      return false;
    }
  }

  private patch(id: string, value: MetadataPatch): Promise<boolean> {
    return this.perform(async signal => {
      const metadata = await this.provider.update(id, value, signal);
      if (signal.aborted) return;
      this.catalog.save(metadata);
      this.renderCatalog();
    }, id);
  }

  private accept(agent: Agent): void {
    if (this.abort.signal.aborted) return;
    this.catalog.accept(agent);
    this.renderCatalog();
  }

  private renderCatalog(): void {
    const { active: agent, agents, metadata, showArchived } = this.catalog;
    const key = JSON.stringify([agents, [...metadata.values()], agent?.id, showArchived]);
    if (key === this.catalogKey) return;
    this.catalogKey = key;
    this.sidebar.render(this.catalog);
    if (!this.active) return;
    if (!agent) {
      this.active.stream.abort();
      this.active = undefined;
      this.body.replaceChildren(element("p", "empty-chat", "This chat is no longer available."));
      return;
    }
    const preferences = metadata.get(agent.id);
    this.active.view.renderHeader(agent, preferences);
    this.active.view.renderLinks(agent, preferences, agents);
  }
}

function failure(error: unknown): string {
  return error instanceof Error ? error.message : "The chat service could not complete the request.";
}
