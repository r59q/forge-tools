import { h } from "./dom.js";
import type { Attributes } from "./dom.js";

/** Text renders plain, muted, caption or code text without interpreting markup. */
export function Text(
  content: string,
  treatment: "plain" | "muted" | "caption" | "code" = "plain",
): HTMLElement {
  return h(treatment === "code" ? "code" : "span", { class: `text-${treatment}` }, content);
}

/** StatusMessage is a polite live message, including loading and error states. */
export function StatusMessage(message = "", attributes: Attributes = {}): HTMLParagraphElement {
  return h("p", { role: "status", ...attributes }, message);
}

/** EmptyState explains an empty workspace or result collection. */
export function EmptyState(message: string): HTMLParagraphElement {
  return StatusMessage(message, { class: "empty-state" });
}

/** ResultSummary explains empty and partial results. */
export function ResultSummary(
  count: number,
  state: { skipped: number; truncated: boolean; canceled: boolean },
): string {
  const notes = [countText(count)];

  if (state.skipped) {
    notes.push(`${state.skipped} skipped (policy, unsupported content or read failures).`);
  }
  if (state.truncated) notes.push("Truncated at the configured limit.");
  if (state.canceled) notes.push("Canceled; results may be incomplete.");

  return notes.join(" ");
}

function countText(count: number): string {
  if (count === 0) return "No results.";

  return count === 1 ? "1 result." : `${count} results.`;
}
