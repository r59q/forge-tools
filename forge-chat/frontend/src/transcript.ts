import type { TranscriptEvent } from "./types.js";

/** A consecutive run of tool and diagnostic activity. */
export interface Activity {
  entries: TranscriptEvent[];
  title: string;
}

/** Display tool results with the name of their call where available. */
export function activityTitle(event: TranscriptEvent, toolName?: string): string {
  const name = event.toolName || toolName || "Tool";
  switch (event.kind) {
    case "tool_call": return toolTitle(`${name} · Call`, event.input);
    case "tool_result": return toolTitle(`${name} · ${event.isError ? "Failed" : "Result"}`, event.text || event.input);
    case "usage": return "Token usage";
    default: return event.role === "reasoning" ? "Reasoning" : event.kind.replaceAll("_", " ");
  }
}

function toolTitle(label: string, text = ""): string {
  const preview = text.trimStart().slice(0, 256).replace(/\s+/g, " ").trimEnd();
  if (!preview) return label;
  return `${label} · ${preview.length > 80 ? preview.slice(0, 80) + "…" : preview}`;
}

function isActivity(event: TranscriptEvent): boolean {
  return event.kind === "tool_call" || event.kind === "tool_result"
    || event.kind === "usage" || event.role === "reasoning";
}

/** The display blocks are either conversation events or collapsible activity runs. */
export type TranscriptBlock = TranscriptEvent | Activity;

/** The change needed by a transcript view after appending one event. */
export interface TranscriptChange {
  block: TranscriptBlock;
  index: number;
  settled: string[];
}

/** Reduce events to a resumable transcript, independent of any DOM. */
export class Transcript {
  after = "0";
  readonly blocks: TranscriptBlock[] = [];
  readonly pending = new Map<string, TranscriptEvent>();
  private readonly tools = new Map<string, string>();
  private readonly assistantText = new Map<number, string>();

  /** Record one event, ignoring sequence numbers already rendered. */
  append(event: TranscriptEvent): TranscriptChange | undefined {
    if (BigInt(event.seq) <= BigInt(this.after)) return undefined;
    this.after = event.seq;
    if (isActivity(event)) return this.activity(event);

    const settled = this.settle(event);
    if (event.kind === "message" && event.role === "assistant" && event.text) {
      this.assistantText.set(event.turn, event.text);
    }
    const index = this.blocks.push(event) - 1;
    return { block: event, index, settled };
  }

  /** Determine whether a result repeats the assistant's message for the same turn. */
  repeatsMessage(event: TranscriptEvent): boolean {
    return Boolean(event.text && this.assistantText.get(event.turn) === event.text);
  }

  private activity(event: TranscriptEvent): TranscriptChange {
    const key = `${event.turn}:${event.toolId}`;
    if (event.toolId && event.toolName) this.tools.set(key, event.toolName);
    const title = activityTitle(event, this.tools.get(key));
    const last = this.blocks.at(-1);
    if (last && "entries" in last) {
      last.entries.push(event);
      last.title = title;
      return { block: last, index: this.blocks.length - 1, settled: [] };
    }
    const block = { entries: [event], title };
    return { block, index: this.blocks.push(block) - 1, settled: [] };
  }

  private settle(event: TranscriptEvent): string[] {
    if (event.kind === "input_request" && event.requestId) {
      this.pending.set(event.requestId, event);
      return [];
    }
    if (event.requestId && (event.kind === "input_answer" || event.kind === "error")) {
      return this.pending.delete(event.requestId) ? [event.requestId] : [];
    }
    if (event.kind !== "result") return [];

    const closed = [...this.pending].filter(([, request]) => request.turn <= event.turn).map(([id]) => id);
    for (const id of closed) this.pending.delete(id);
    return closed;
  }
}
