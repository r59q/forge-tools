import { recentChats, sessionTree } from "./chats.js";
import type { SessionNode } from "./chats.js";
import { button, element } from "./dom.js";
import type { Agent, ChatMetadata } from "./types.js";

export function chatLabel(agent: Agent): string {
  return `${agent.client === "claude" ? "Claude Code" : agent.client} · ${agent.id.slice(0, 8)}`;
}

export function stateLabel(state: string): string {
  return state.replaceAll("_", " ");
}

export class Sidebar {
  readonly element: HTMLElement;
  private readonly list = element("div", "chat-list");
  private readonly related = element("section", "session-tree");
  private readonly toggle: HTMLButtonElement;
  private archived = false;
  private agents: Agent[] = [];
  private metadata = new Map<string, ChatMetadata>();
  private selected = "";

  constructor(create: HTMLElement, private readonly select: (id: string) => void) {
    this.toggle = button("Show archived", () => {
      this.archived = !this.archived;
      this.renderList();
    });
    this.element = element("aside", "sidebar", element("div", "brand", "forge-chat"), create,
      element("nav", "chat-navigation", element("div", "list-heading", element("h2", "", "Chats"), this.toggle), this.list),
      this.related);
  }

  render(agents: Agent[], metadata: Map<string, ChatMetadata>, selected: string): void {
    this.agents = agents;
    this.metadata = metadata;
    this.selected = selected;
    this.renderList();

    const tree = sessionTree(agents, selected);
    this.related.hidden = !tree?.children.length;
    this.related.replaceChildren(...(tree ? [element("h2", "", "Session tree"), element("ul", "", this.node(tree))] : []));
  }

  private renderList(): void {
    this.toggle.textContent = this.archived ? "Show active" : "Show archived";
    const chats = recentChats(this.agents, this.metadata, this.archived);
    const rows = chats.map(agent => this.chatButton(agent, true));
    this.list.replaceChildren(...rows);
    if (!rows.length) this.list.append(element("p", "muted", this.archived ? "No archived chats." : "No active chats."));
  }

  private node(node: SessionNode): HTMLLIElement {
    const row = element("li", "", this.chatButton(node.agent, false));
    if (node.children.length) row.append(element("ul", "", ...node.children.map(child => this.node(child))));
    return row;
  }

  private chatButton(agent: Agent, showState: boolean): HTMLButtonElement {
    const control = button(chatLabel(agent), () => this.select(agent.id), "chat-item");
    if (showState) control.append(element("span", "chat-state", stateLabel(agent.state)));
    if (agent.id === this.selected) control.setAttribute("aria-current", "true");
    return control;
  }
}
