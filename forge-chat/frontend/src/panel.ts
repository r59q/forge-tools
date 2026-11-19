import { ChatView } from "./chat-view.js";
import { byActivity } from "./chats.js";
import { createForm } from "./create.js";
import { element } from "./dom.js";
import { followChat } from "./live.js";
import { Sidebar } from "./sidebar.js";
import { Transcript } from "./transcript.js";
import { TranscriptView } from "./transcript-view.js";
import type { Agent, ChatMetadata, ChatProvider, MetadataPatch } from "./types.js";

interface ActiveChat {
  id: string;
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
  private active?: ActiveChat;
  private agents: Agent[] = [];
  private metadata = new Map<string, ChatMetadata>();
  private version = 0;
  private refreshing = false;
  private catalogKey = "";

  constructor(private readonly provider: ChatProvider) {
    const create = createForm(spec => this.perform(async signal => {
      const agent = await provider.create(spec, signal);
      this.accept(agent);
      this.select(agent.id);
    }));
    this.sidebar = new Sidebar(create, id => this.select(id));
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
    const agent = this.agents.find(candidate => candidate.id === id);
    if (!agent || this.active?.id === id) return;
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
    this.active = { id, view, stream };
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
    const version = this.version;
    try {
      const [agents, metadata] = await Promise.all([
        this.provider.agents(this.abort.signal), this.provider.metadata(this.abort.signal),
      ]);
      if (this.abort.signal.aborted || version !== this.version) return;
      this.catalogError.textContent = "";
      this.agents = agents;
      this.metadata = new Map(metadata.map(entry => [entry.id, entry]));
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
      if (!this.abort.signal.aborted && (!id || this.active?.id === id)) this.error.textContent = failure(error);
      return false;
    }
  }

  private patch(id: string, value: MetadataPatch): Promise<boolean> {
    return this.perform(async signal => {
      const metadata = await this.provider.update(id, value, signal);
      if (signal.aborted) return;
      this.version++;
      this.metadata.set(id, metadata);
      this.renderCatalog();
    }, id);
  }

  private accept(agent: Agent): void {
    if (this.abort.signal.aborted) return;
    const current = this.agents.find(candidate => candidate.id === agent.id);
    if (current && byActivity(current, agent) < 0) return;
    this.version++;
    this.agents = this.agents.filter(candidate => candidate.id !== agent.id).concat(agent);
    this.renderCatalog();
  }

  private renderCatalog(): void {
    const key = JSON.stringify([this.agents, [...this.metadata.values()], this.active?.id]);
    if (key === this.catalogKey) return;
    this.catalogKey = key;
    this.sidebar.render(this.agents, this.metadata, this.active?.id ?? "");
    if (!this.active) return;
    const agent = this.agents.find(candidate => candidate.id === this.active?.id);
    if (!agent) {
      this.active.stream.abort();
      this.active = undefined;
      this.body.replaceChildren(element("p", "empty-chat", "This chat is no longer available."));
      return;
    }
    const metadata = this.metadata.get(agent.id);
    this.active.view.renderHeader(agent, metadata);
    this.active.view.renderLinks(agent, metadata, this.agents);
  }
}

function failure(error: unknown): string {
  return error instanceof Error ? error.message : "The chat service could not complete the request.";
}
