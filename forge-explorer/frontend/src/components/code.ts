import type { FileContent, Range } from "../types";
import { button, element } from "./dom";
import { highlight } from "./highlight";

/** Render at most 200 lines at once, including full-file line numbers and selection. */
export function renderCode(
  container: HTMLElement,
  file: FileContent,
  range: Range,
  select: (range: Range) => void,
): void {
  const lines = file.text.split("\n");
  let start = Math.floor(Math.max(0, range.startLine - 1) / 200) * 200;
  let anchor = range.startLine || 1;
  let selection = { ...range };
  function choose(line: number, extend: boolean): void {
    if (!extend) anchor = line;
    const first = Math.min(anchor, line);
    const last = Math.max(anchor, line);
    selection = {
      startLine: first,
      startColumn: 1,
      endLine: last,
      endColumn: (lines[last - 1]?.length ?? 0) + 1,
    };
    select(selection);
    for (const row of container.querySelectorAll<HTMLElement>("[data-line]")) {
      const number = Number(row.dataset.line);
      row.classList.toggle("selected", number >= first && number <= last);
      const control = row.querySelector("button");
      if (control) control.tabIndex = number === line ? 0 : -1;
      row
        .querySelector("button")
        ?.setAttribute(
          "aria-pressed",
          String(number >= first && number <= last),
        );
    }
  }
  function draw(focusLine?: number): void {
    const controls = element("div", "", "code-controls");
    const previous = button("Previous lines", () => {
      start = Math.max(0, start - 200);
      anchor = start + 1;
      draw(start + 1);
    });
    const next = button("Next lines", () => {
      start += 200;
      anchor = start + 1;
      draw(start + 1);
    });
    previous.disabled = start === 0;
    next.disabled = start + 200 >= lines.length;
    const jump = element("input");
    jump.type = "number";
    jump.min = "1";
    jump.max = String(lines.length);
    jump.value = String(start + 1);
    jump.setAttribute("aria-label", "Go to line");
    jump.addEventListener("keydown", (event) => {
      if (event.key !== "Enter") return;
      const line = Number(jump.value);
      if (!Number.isInteger(line) || line < 1 || line > lines.length) return;
      start = Math.floor((line - 1) / 200) * 200;
      anchor = line;
      draw(line);
    });
    controls.append(
      previous,
      element(
        "span",
        `Lines ${start + 1}–${Math.min(start + 200, lines.length)} of ${lines.length}`,
      ),
      next,
      jump,
    );
    const code = element("pre", "", "code");
    code.setAttribute("aria-label", file.location.path);
    for (
      let index = start;
      index < Math.min(start + 200, lines.length);
      index++
    ) {
      const number = index + 1;
      const row = element("div", "", "code-line");
      row.dataset.line = String(number);
      const selected =
        number >= selection.startLine && number <= selection.endLine;
      row.classList.toggle("selected", selected);
      const gutter = element("button", String(number), "line-number");
      gutter.type = "button";
      gutter.setAttribute("aria-label", `Select line ${number}`);
      gutter.setAttribute("aria-pressed", String(selected));
      gutter.tabIndex =
        number === (focusLine ?? (selection.startLine || start + 1)) ? 0 : -1;
      gutter.addEventListener("click", (event) =>
        choose(number, event.shiftKey),
      );
      gutter.addEventListener("keydown", (event) => {
        if (!["ArrowUp", "ArrowDown", "Home", "End"].includes(event.key))
          return;
        event.preventDefault();
        const target =
          event.key === "Home"
            ? 1
            : event.key === "End"
              ? lines.length
              : Math.min(
                  lines.length,
                  Math.max(1, number + (event.key === "ArrowUp" ? -1 : 1)),
                );
        if (event.shiftKey) choose(target, true);
        else anchor = target;
        start = Math.floor((target - 1) / 200) * 200;
        draw(target);
      });
      const text = element("code");
      text.append(highlight(lines[index] ?? "", file.location.path));
      row.append(gutter, text);
      code.append(row);
    }
    container.replaceChildren(controls, code);
    if (focusLine)
      container
        .querySelector<HTMLButtonElement>(`[data-line="${focusLine}"] button`)
        ?.focus();
  }
  draw();
}
