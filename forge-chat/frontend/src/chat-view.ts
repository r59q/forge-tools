import { isTerminal } from "./chats.js";
import { button, element, option, submit } from "./dom.js";
import { chatLabel, stateLabel } from "./sidebar.js";
import type { Agent, ChatMetadata, MetadataPatch } from "./types.js";

export class ChatView {
  readonly status = element("p", "stream-state");
  readonly element: HTMLElement;
  private readonly header = element("header", "chat-header");
  private readonly links = element("div", "chat-links");
  private readonly composer: HTMLElement;
  private readonly text = element("textarea", "");
  private readonly send = submit("Send");
  private sending = false;
  private writable = false;

  constructor(
    transcript: HTMLElement,
    send: (text: string) => Promise<boolean>,
    private readonly stop: (id: string) => Promise<boolean>,
    private readonly patch: (id: string, value: MetadataPatch) => Promise<boolean>,
    private readonly select: (id: string) => void,
  ) {
    this.status.setAttribute("role", "status");
    this.composer = this.compose(send);
    this.element = element("section", "conversation", this.header, this.links, transcript, this.composer);
  }

  renderHeader(agent: Agent, metadata?: ChatMetadata): void {
    const state = element("span", "state-pill", stateLabel(agent.state));
    const description = [agent.model, agent.effort, agent.workspace?.kind].filter(Boolean).join(" · ");
    const title = element("div", "chat-title", element("h1", "", chatLabel(agent)), element("p", "muted", description));
    const archive = button(metadata?.archived ? "Restore" : "Archive", () => {
      archive.disabled = true;
      void this.patch(agent.id, { archived: !metadata?.archived }).then(() => { archive.disabled = false; });
    });
    const controls = element("div", "chat-controls", state, archive);
    if (!isTerminal(agent.state)) controls.append(this.stopButton(agent));
    this.header.replaceChildren(title, controls);
    this.writable = agent.state === "created" || agent.state === "idle";
    this.updateComposer();
  }

  renderLinks(agent: Agent, metadata: ChatMetadata | undefined, agents: readonly Agent[]): void {
    const ids = metadata?.links?.ids ?? [];
    const links = ids.flatMap(id => {
      const linked = agents.find(candidate => candidate.id === id);
      if (!linked) return [];
      const remove = button("×", () => void this.patch(agent.id, { links: { ids: ids.filter(other => other !== id) } }));
      remove.setAttribute("aria-label", `Remove link to ${chatLabel(linked)}`);
      return [element("span", "linked-chat", button(chatLabel(linked), () => this.select(id)), remove)];
    });
    const choices = agents.filter(candidate => candidate.id !== agent.id && !ids.includes(candidate.id));
    const add = element("select", "link-select", option("", "Link another chat"),
      ...choices.map(candidate => option(candidate.id, chatLabel(candidate))));
    add.setAttribute("aria-label", "Link another chat");
    add.disabled = !choices.length;
    add.addEventListener("change", () => {
      if (!add.value) return;
      add.disabled = true;
      void this.patch(agent.id, { links: { ids: [...ids, add.value] } }).then(() => {
        add.disabled = false;
        add.value = "";
      });
    });
    this.links.replaceChildren(...links, add);
  }

  private stopButton(agent: Agent): HTMLButtonElement {
    const stop = button("Stop", () => {
      stop.disabled = true;
      void this.stop(agent.id).then(() => { stop.disabled = false; });
    });
    return stop;
  }

  private compose(send: (text: string) => Promise<boolean>): HTMLElement {
    this.text.setAttribute("aria-label", "Message");
    this.text.placeholder = "Write a message…";
    this.text.rows = 2;
    const form = element("form", "composer", this.text, this.send);
    form.addEventListener("submit", event => {
      event.preventDefault();
      const text = this.text.value.trim();
      if (!text || this.sending || !this.writable) return;
      this.sending = true;
      this.updateComposer();
      void send(text).then(ok => {
        if (ok) this.text.value = "";
        this.sending = false;
        this.updateComposer();
      });
    });
    return element("footer", "composer-area", this.status, form);
  }

  private updateComposer(): void {
    this.text.disabled = this.sending || !this.writable;
    this.send.disabled = this.text.disabled;
  }
}
