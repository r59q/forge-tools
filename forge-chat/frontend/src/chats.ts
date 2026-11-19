import type { Agent, ChatMetadata } from "./types.js";

/** List chats in descending order of activity, selecting either active or archived chats. */
export function recentChats(agents: readonly Agent[], metadata: ReadonlyMap<string, ChatMetadata>, archived: boolean): Agent[] {
  return agents.filter(agent => Boolean(metadata.get(agent.id)?.archived) === archived)
    .sort(byActivity);
}

function byActivity(a: Agent, b: Agent): number {
  const milliseconds = Date.parse(b.updatedAt) - Date.parse(a.updatedAt);
  if (milliseconds) return milliseconds;
  // Protobuf timestamps can differ at sub-millisecond precision. Optional
  // fractional seconds have to be padded before comparing their digits.
  const fraction = (value: string) => (value.split(".")[1]?.split("Z")[0] ?? "").padEnd(9, "0");
  return fraction(b.updatedAt).localeCompare(fraction(a.updatedAt)) || a.id.localeCompare(b.id);
}

/** An agent and its immediate descendants in a session tree. */
export interface SessionNode {
  agent: Agent;
  children: SessionNode[];
}

/** Find the selected agent's family, even if some relatives are archived. */
export function sessionTree(agents: readonly Agent[], selected: string): SessionNode | undefined {
  const nodes = new Map(agents.map(agent => [agent.id, { agent, children: [] as SessionNode[] }]));
  const root = rootOf(nodes, selected);
  if (!root) return undefined;
  for (const node of nodes.values()) {
    nodes.get(node.agent.parent)?.children.push(node);
  }
  return root;
}

function rootOf(nodes: Map<string, SessionNode>, selected: string): SessionNode | undefined {
  let node = nodes.get(selected);
  const seen = new Set<string>();
  while (node && !seen.has(node.agent.id)) {
    seen.add(node.agent.id);
    const parent = nodes.get(node.agent.parent);
    if (!parent) return node;
    node = parent;
  }
  return undefined;
}

/** A terminal agent has no future events once its stream has drained. */
export function isTerminal(state: string): boolean {
  return state === "stopped" || state === "failed";
}

/** Track chat selection and preferences independently of the mounted views. */
export class ChatCatalog {
  private entries: Agent[] = [];
  private preferences = new Map<string, ChatMetadata>();
  private generation = 0;
  private selected = "";
  showArchived = false;

  get agents(): readonly Agent[] { return this.entries; }
  get metadata(): ReadonlyMap<string, ChatMetadata> { return this.preferences; }
  get version(): number { return this.generation; }
  get active(): Agent | undefined { return this.find(this.selected); }

  select(id: string): boolean {
    if (id === this.selected || !this.find(id)) return false;
    this.selected = id;
    return true;
  }

  replace(agents: Agent[], metadata: ChatMetadata[], version: number): void {
    if (version !== this.generation) return;
    this.entries = agents;
    this.preferences = new Map(metadata.map(entry => [entry.id, entry]));
    if (!this.active) this.selected = "";
  }

  accept(agent: Agent): void {
    const current = this.find(agent.id);
    if (current && byActivity(current, agent) < 0) return;
    this.generation++;
    this.entries = this.entries.filter(candidate => candidate.id !== agent.id).concat(agent);
  }

  save(metadata: ChatMetadata): void {
    this.generation++;
    this.preferences.set(metadata.id, metadata);
  }

  private find(id: string): Agent | undefined {
    return this.entries.find(agent => agent.id === id);
  }
}
