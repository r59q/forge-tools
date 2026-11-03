/** Create an element using textContent so repository strings cannot become markup. */
export function element<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  text = "",
  className = "",
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.textContent = text;
  node.className = className;
  return node;
}

/** NudaUI Shine Sweep markup, with an explicit non-submit button type. */
export function button(
  label: string,
  action: () => void,
  className = "",
): HTMLButtonElement {
  const node = element("button", "", `nuda-shine-sweep ${className}`);
  node.type = "button";
  node.append(element("span", label, "nuda-shine-sweep__label"));
  node.addEventListener("click", action);
  return node;
}

/** Explain empty and partial-result states consistently. */
export function resultSummary(
  count: number,
  state: { skipped: number; truncated: boolean; canceled: boolean },
): string {
  return (
    `${count ? `${count} result${count === 1 ? "" : "s"}` : "No results"}.` +
    (state.skipped
      ? ` ${state.skipped} skipped (policy, unsupported content or read failures).`
      : "") +
    (state.truncated ? " Truncated at the configured limit." : "") +
    (state.canceled ? " Canceled; results may be incomplete." : "")
  );
}
