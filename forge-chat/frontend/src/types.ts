/** A forge repository is identified by its connection source and repository path. */
export type Repository = { source: string; path: string } | { localPath: string };

/** The conversation information returned by forge-agents. */
export interface Agent {
  id: string;
  client: string;
  model: string;
  effort: string;
  parent: string;
  state: string;
  turns: number;
  updatedAt: string;
  workspace?: { kind: string; checkout?: string } | null;
}

/** The settings for a new conversation. An omitted workspace is a scratch directory. */
export interface NewAgent {
  client: string;
  model?: string;
  effort?: string;
  workspace?: { kind: "checkout"; repository: Repository };
  policy?: { mode: "read_only"; ask: boolean };
}

/** A stored forge-agents transcript event. Protobuf encodes int64 sequences as strings. */
export interface TranscriptEvent {
  seq: string;
  time: string;
  turn: number;
  kind: string;
  role?: string;
  text?: string;
  toolId?: string;
  toolName?: string;
  input?: string;
  isError?: boolean;
  requestId?: string;
  requestKind?: string;
  allow?: boolean;
  status?: string;
  truncated?: boolean;
  usage?: { inputTokens?: string; outputTokens?: string; cachedTokens?: string; costUsd?: number } | null;
}

/** A reply to a question or a decision on a permission request. */
export interface InputAnswer {
  text?: string;
  allow?: boolean;
}

/** Coordinates stored for a future island view. */
export interface IslandPosition {
  x: number;
  y: number;
  z: number;
}

/** Display preferences owned by forge-chat for one conversation. */
export interface ChatMetadata {
  id: string;
  archived?: boolean;
  links?: { ids?: string[] } | null;
  islandPosition?: IslandPosition | null;
}

/** An independent update to one or more display preferences. */
export type MetadataPatch = Omit<ChatMetadata, "id">;

/** Data source for an embedded chat. Implementations should honor cancellation. */
export interface ChatProvider {
  agents(signal: AbortSignal): Promise<Agent[]>;
  metadata(signal: AbortSignal): Promise<ChatMetadata[]>;
  create(spec: NewAgent, signal: AbortSignal): Promise<Agent>;
  get(id: string, signal: AbortSignal): Promise<Agent>;
  send(id: string, text: string, signal: AbortSignal): Promise<Agent>;
  answer(id: string, requestId: string, answer: InputAnswer, signal: AbortSignal): Promise<Agent>;
  stop(id: string, signal: AbortSignal): Promise<Agent>;
  update(id: string, patch: MetadataPatch, signal: AbortSignal): Promise<ChatMetadata>;
  stream(id: string, after: string, signal: AbortSignal): AsyncIterable<TranscriptEvent>;
}
