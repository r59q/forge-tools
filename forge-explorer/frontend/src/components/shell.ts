import type { Capabilities, ContentProvider, Source } from "../types.js";
import { Button } from "./controls.js";
import { h } from "./dom.js";
import { Text } from "./messages.js";

const SEMANTIC_OPERATIONS: Array<keyof Capabilities> = [
  "symbols", "definitions", "references", "history",
];

/** ExplorerShell lays out one source's header, controls, sidebar and workspace. */
export function ExplorerShell(
  source: Source,
  toolbar: HTMLElement,
  sidebar: HTMLElement,
  workspace: HTMLElement,
  capabilities: HTMLElement,
): HTMLElement {
  return h("section", { class: "explorer", "aria-label": `${source.label} explorer` },
    SourceHeader(source), toolbar, h("div", { class: "layout" }, sidebar, workspace), capabilities);
}

/** SourceHeader identifies live or versioned source content. */
export function SourceHeader(source: Source): HTMLElement {
  const content = source.kind === "worktree" ? "live content" : source.version;

  return h("header", { class: "source-header" }, h("h2", {}, source.label),
    h("p", { class: "source-caption" }, Text(`${source.kind} · ${source.id} · ${content}`, "caption")));
}

/** ExplorerSidebar groups tree refresh, discovery and search. */
export function ExplorerSidebar(tree: HTMLElement, search: HTMLElement, refresh: () => unknown): HTMLElement {
  return h("aside", {}, Button("Refresh tree", refresh), tree, search);
}

/** CapabilitySummary reports only semantic operations supported by both source and provider. */
export function CapabilitySummary(provider: ContentProvider, source: Source): HTMLElement {
  const describe = (operation: keyof Capabilities): string => {
    const available = source.capabilities[operation] && provider[operation] !== undefined;
    return `${operation}: ${available ? "available through provider" : "unsupported"}`;
  };

  return h("p", { class: "capabilities" }, Text(SEMANTIC_OPERATIONS.map(describe).join(" · "), "caption"));
}
