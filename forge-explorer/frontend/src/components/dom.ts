/** Attributes for h. false and undefined omit the attribute; true sets it empty. */
export type Attributes = Record<string, string | number | boolean | undefined>;

/** h creates an element. Strings become text nodes, so repository text never becomes markup. */
export function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attributes: Attributes = {},
  ...children: Array<Node | string>
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);

  for (const [name, value] of Object.entries(attributes)) {
    if (value === false || value === undefined) continue;
    node.setAttribute(name, value === true ? "" : String(value));
  }

  node.append(...children);
  return node;
}

/** button creates a NudaUI Shine Sweep button. A promise from action is not awaited. */
export function button(
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

const ARROW_STEPS: Record<string, number> = { ArrowDown: 1, ArrowUp: -1 };

/** focusSibling moves focus between controls on ArrowUp and ArrowDown. */
export function focusSibling(event: KeyboardEvent, controls: HTMLElement[]): void {
  const step = ARROW_STEPS[event.key];
  if (!step) return;

  const index = controls.indexOf(event.target as HTMLElement);
  const target = controls[index + step];
  if (!target) return;

  event.preventDefault();
  target.focus();
}

/** resultSummary explains empty and partial results. */
export function resultSummary(
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
