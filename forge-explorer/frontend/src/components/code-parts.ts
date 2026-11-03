import { Input } from "./controls.js";
import { h } from "./dom.js";
import { highlight } from "./highlight.js";
import { Toolbar } from "./layout.js";

/** CodeNavigation groups pagination, the visible range and direct line navigation. */
export function CodeNavigation(...controls: HTMLElement[]): HTMLElement {
  const node = Toolbar(...controls);
  node.className = "code-controls";
  return node;
}

/** GoToLine uses a native bounded number input and Enter to move focus. */
export function GoToLine(count: number, jump: (line: number) => void): HTMLInputElement {
  const node = Input("number", { min: 1, max: count, "aria-label": "Go to line" });
  node.addEventListener("keydown", (event) => {
    if (event.key === "Enter") jump(Number(node.value));
  });
  return node;
}

/** LineNumber is a specialised selection control managed by CodeViewer's roving focus. */
export function LineNumber(line: number): HTMLButtonElement {
  return h("button", {
    type: "button", class: "line-number", "aria-label": `Select line ${line}`,
  }, String(line));
}

/** CodeLine displays one safely highlighted line alongside its selection control. */
export function CodeLine(line: number, text: string, path: string): HTMLElement {
  return h("div", { class: "code-line", "data-line": line },
    LineNumber(line), h("code", {}, highlight(text, path)));
}
