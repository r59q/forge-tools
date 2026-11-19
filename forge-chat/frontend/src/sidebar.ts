import { recentChats, sessionTree } from "./chats.js";
import type { ChatCatalog, SessionNode } from "./chats.js";
import { button, element } from "./dom.js";
import type { Agent } from "./types.js";

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

  constructor(create: HTMLElement, private readonly select: (id: string) => void, toggleArchived: () => void) {
    this.toggle = button("Show archived", toggleArchived);
    this.element = element("aside", "sidebar", element("div", "brand", "forge-chat"), create,
      element("nav", "chat-navigation", element("div", "list-heading", element("h2", "", "Chats"), this.toggle), this.list),
      this.related);
  }

  render(catalog: ChatCatalog): void {
    const selected = catalog.active?.id ?? "";
    this.renderList(catalog, selected);

    const tree = sessionTree(catalog.agents, selected);
    this.related.hidden = !tree?.children.length;
    this.related.replaceChildren(...(tree ? [element("h2", "", "Session tree"), element("ul", "", this.node(tree, selected))] : []));
  }

  private renderList(catalog: ChatCatalog, selected: string): void {
    const archived = catalog.showArchived;
    this.toggle.textContent = archived ? "Show active" : "Show archived";
    const chats = recentChats(catalog.agents, catalog.metadata, archived);
    const rows = chats.map(agent => this.chatButton(agent, selected, true));
    this.list.replaceChildren(...rows);
    if (!rows.length) this.list.append(element("p", "muted", archived ? "No archived chats." : "No active chats."));
  }

  private node(node: SessionNode, selected: string): HTMLLIElement {
    const row = element("li", "", this.chatButton(node.agent, selected, false));
    if (node.children.length) row.append(element("ul", "", ...node.children.map(child => this.node(child, selected))));
    return row;
  }

  private chatButton(agent: Agent, selected: string, showState: boolean): HTMLButtonElement {
    const control = button(chatLabel(agent), () => this.select(agent.id), "chat-item");
    if (showState) control.append(element("span", "chat-state", stateLabel(agent.state)));
    if (agent.id === selected) control.setAttribute("aria-current", "true");
    return control;
  }
}
