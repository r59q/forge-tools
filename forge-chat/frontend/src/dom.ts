// Views build elements here so transcript content is always assigned as text.
export function element<K extends keyof HTMLElementTagNameMap>(
  tag: K, className: string, ...children: (Node | string)[]
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.className = className;
  node.append(...children);
  return node;
}

export function button(label: string, onClick: () => void, className = ""): HTMLButtonElement {
  const control = element("button", className, label);
  control.type = "button";
  control.addEventListener("click", onClick);
  return control;
}

export function submit(label: string): HTMLButtonElement {
  const control = element("button", "primary", label);
  control.type = "submit";
  return control;
}

export function option(value: string, label: string): HTMLOptionElement {
  const control = element("option", "", label);
  control.value = value;
  return control;
}

export function field(label: string, control: HTMLElement): HTMLLabelElement {
  control.setAttribute("aria-label", label);
  return element("label", "field", element("span", "", label), control);
}
