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
