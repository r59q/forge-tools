import { h } from "./dom.js";
import type { Attributes } from "./dom.js";

/** Button creates a NudaUI Shine Sweep button. A promise from action is not awaited. */
export function Button(
  label: string,
  action: () => unknown,
  attributes: Attributes = {},
): HTMLButtonElement {
  const className = `nuda-shine-sweep ${attributes.class ?? ""}`.trim();
  const node = h(
    "button",
    { type: "button", ...attributes, class: className },
    h("span", { class: "nuda-shine-sweep__label" }, label),
  );

  node.addEventListener("click", () => void action());
  return node;
}

/** Input keeps native editing and validation for supported input types. */
export function Input(
  type: "text" | "search" | "password" | "number",
  attributes: Attributes = {},
): HTMLInputElement {
  return h("input", { type, ...attributes });
}

/** Select renders a native select with text-only option labels. */
export function Select(
  options: ReadonlyArray<{ value: string; label: string }>,
  attributes: Attributes = {},
): HTMLSelectElement {
  return h("select", attributes, ...options.map((option) =>
    h("option", { value: option.value }, option.label),
  ));
}

/** Field associates a visible label with its native control. */
export function Field(label: string, control: HTMLInputElement | HTMLSelectElement): HTMLLabelElement {
  return h("label", { class: "field" }, label, control);
}

/** Link is a native anchor with a text-only label. */
export function Link(label: string, href: string): HTMLAnchorElement {
  return h("a", { href }, label);
}

/** Disclosure retains native summary keyboard and expanded-state semantics. */
export function Disclosure(label: string, body: HTMLElement): HTMLDetailsElement {
  return h("details", {}, h("summary", {}, label), body);
}
