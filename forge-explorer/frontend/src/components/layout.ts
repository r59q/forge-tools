import { h } from "./dom.js";
import type { Attributes } from "./dom.js";

/** Toolbar groups related controls without imposing arrow-key navigation. */
export function Toolbar(...controls: HTMLElement[]): HTMLDivElement {
  return h("div", { class: "toolbar" }, ...controls);
}

/** Separator is decorative punctuation between related items. */
export function Separator(text: string): HTMLSpanElement {
  return h("span", { "aria-hidden": "true" }, text);
}

/** ScrollArea adds overflow containment to a semantic element without a wrapper. */
export function ScrollArea<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attributes: Attributes = {},
  ...children: HTMLElement[]
): HTMLElementTagNameMap[K] {
  return h(tag, { ...attributes, class: `scroll-area ${attributes.class ?? ""}`.trim() }, ...children);
}

/** Panel groups a header and body in a semantic container. */
export function Panel(
  tag: "section" | "main",
  attributes: Attributes,
  header: HTMLElement,
  body: HTMLElement,
): HTMLElement {
  return h(tag, attributes, header, body);
}

/** PanelHeader groups a panel's title, controls and status. */
export function PanelHeader(...children: HTMLElement[]): HTMLElement {
  return h("header", { class: "panel-header" }, ...children);
}

/** PanelBody groups a panel's content. */
export function PanelBody(...children: HTMLElement[]): HTMLDivElement {
  return h("div", { class: "panel-body" }, ...children);
}
