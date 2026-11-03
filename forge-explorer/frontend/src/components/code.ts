import type { FileContent, Range } from "../types.js";
import { button, h } from "./dom.js";
import { highlight } from "./highlight.js";

const PAGE_SIZE = 200;

/** renderCode shows file one page of lines at a time. Line-number buttons select whole lines. */
export function renderCode(
  container: HTMLElement,
  file: FileContent,
  range: Range,
  select: (range: Range) => void,
): void {
  const view = new CodeView(file, range, select);
  container.replaceChildren(view.controls, view.code);
}

// CodeView renders one page of lines and owns its selection and keyboard focus.
// Only the focused line's button is in the tab order.
class CodeView {
  readonly code: HTMLPreElement;
  readonly controls: HTMLElement;

  readonly #lines: string[];
  readonly #path: string;
  readonly #select: (range: Range) => void;
  #selection: Range;
  #anchor: number; // where a Shift-selection starts
  #focusLine: number;
  #first: number; // zero-based index of the first rendered line

  readonly #previous = button("Previous lines", () => this.#moveTo(this.#first - PAGE_SIZE + 1));
  readonly #next = button("Next lines", () => this.#moveTo(this.#first + PAGE_SIZE + 1));
  readonly #position = h("span");
  readonly #jump = h("input", { type: "number", min: 1, "aria-label": "Go to line" });

  constructor(file: FileContent, range: Range, select: (range: Range) => void) {
    this.#lines = file.text.split("\n");
    this.#path = file.location.path;
    this.#select = select;
    this.#selection = { ...range };
    this.#anchor = range.startLine || 1;
    this.#focusLine = this.#anchor;
    this.#first = pageStart(this.#anchor);

    this.controls = this.#createControls();
    this.code = h("pre", { class: "code", "aria-label": this.#path });
    this.code.addEventListener("click", (event) => this.#onClick(event));
    this.code.addEventListener("keydown", (event) => this.#onKey(event));
    this.#draw();
  }

  #createControls(): HTMLElement {
    this.#jump.max = String(this.#lines.length);
    this.#jump.addEventListener("keydown", (event) => {
      if (event.key === "Enter") this.#jumpTo(Number(this.#jump.value));
    });

    return h(
      "div",
      { class: "code-controls" },
      this.#previous,
      this.#position,
      this.#next,
      this.#jump,
    );
  }

  #onClick(event: MouseEvent): void {
    const line = gutterLine(event.target);
    if (line) this.#choose(line, event.shiftKey);
  }

  #onKey(event: KeyboardEvent): void {
    const line = gutterLine(event.target);
    const target = line && keyTarget(event.key, line, this.#lines.length);
    if (!target) return;

    event.preventDefault();
    if (event.shiftKey) this.#choose(target, true);
    else this.#anchor = target;
    this.#focus(target);
  }

  #jumpTo(line: number): void {
    if (Number.isInteger(line) && line >= 1 && line <= this.#lines.length) {
      this.#moveTo(line);
    }
  }

  #moveTo(line: number): void {
    this.#anchor = line;
    this.#focus(line);
  }

  // choose selects the lines from the anchor to line, or just line.
  #choose(line: number, extend: boolean): void {
    if (!extend) this.#anchor = line;

    const first = Math.min(this.#anchor, line);
    const last = Math.max(this.#anchor, line);
    const endColumn = (this.#lines[last - 1]?.length ?? 0) + 1;

    this.#selection = { startLine: first, startColumn: 1, endLine: last, endColumn };
    this.#focusLine = line;
    this.#select(this.#selection);
    this.#markRows();
  }

  // focus focuses line, turning the page first when needed.
  #focus(line: number): void {
    this.#focusLine = line;

    const first = pageStart(line);
    if (first === this.#first) {
      this.#markRows();
    } else {
      this.#first = first;
      this.#draw();
    }

    this.code.querySelector<HTMLButtonElement>(`[data-line="${line}"] button`)?.focus();
  }

  #draw(): void {
    const end = Math.min(this.#first + PAGE_SIZE, this.#lines.length);
    const rows: HTMLElement[] = [];
    for (let index = this.#first; index < end; index++) {
      rows.push(createRow(index + 1, this.#lines[index] ?? "", this.#path));
    }

    this.code.replaceChildren(...rows);
    this.#markRows();

    this.#previous.disabled = this.#first === 0;
    this.#next.disabled = end >= this.#lines.length;
    this.#position.textContent = `Lines ${this.#first + 1}–${end} of ${this.#lines.length}`;
    this.#jump.value = String(this.#first + 1);
  }

  // markRows applies selection and focus state to the rendered rows.
  #markRows(): void {
    const { startLine, endLine } = this.#selection;

    for (const row of this.code.querySelectorAll<HTMLElement>("[data-line]")) {
      const line = Number(row.dataset.line);
      const selected = line >= startLine && line <= endLine;
      const gutter = row.querySelector("button");

      row.classList.toggle("selected", selected);
      gutter?.setAttribute("aria-pressed", String(selected));
      if (gutter) gutter.tabIndex = line === this.#focusLine ? 0 : -1;
    }
  }
}

function createRow(line: number, text: string, path: string): HTMLElement {
  const gutter = h(
    "button",
    { type: "button", class: "line-number", "aria-label": `Select line ${line}` },
    String(line),
  );

  return h(
    "div",
    { class: "code-line", "data-line": line },
    gutter,
    h("code", {}, highlight(text, path)),
  );
}

// pageStart returns the zero-based index of the first line on line's page.
function pageStart(line: number): number {
  return Math.floor(Math.max(0, line - 1) / PAGE_SIZE) * PAGE_SIZE;
}

// gutterLine returns the line number of a clicked or focused line-number button.
function gutterLine(target: EventTarget | null): number | undefined {
  const gutter = (target as Element | null)?.closest(".line-number");
  const line = gutter?.parentElement?.dataset.line;

  return line ? Number(line) : undefined;
}

function keyTarget(key: string, line: number, count: number): number | undefined {
  switch (key) {
    case "Home":
      return 1;
    case "End":
      return count;
    case "ArrowUp":
      return Math.max(1, line - 1);
    case "ArrowDown":
      return Math.min(count, line + 1);
    default:
      return undefined;
  }
}
