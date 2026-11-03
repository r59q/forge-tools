import type { FileContent, Location, Range } from "../types.js";
import type { Navigation } from "../model/navigation.js";
import { renderBreadcrumbs } from "./breadcrumbs.js";
import { renderCode } from "./code.js";
import { h } from "./dom.js";
import { PANEL_ID, renderTabs, tabId } from "./tabs.js";

const NO_FILE = "Choose a file to begin.";

/** FileViewActions are the navigation commands the file view can issue. */
export interface FileViewActions {
  open(location: Location): unknown;
  close(path: string): unknown;
  select(range: Range): void;
  showDirectory(path: string): unknown;
}

/** FileView shows the open tabs, breadcrumbs, status and the current file. */
export class FileView {
  readonly #tabs = h("div", { class: "nuda-tabs" });
  readonly #breadcrumbs = h("nav", { class: "breadcrumbs", "aria-label": "Breadcrumbs" });
  readonly #status = h("p", { class: "status", role: "status" }, NO_FILE);
  readonly #selection = h("p", { class: "selection", role: "status" });
  readonly #panel = h("div", {
    id: PANEL_ID,
    class: "nuda-tabs__panel is-active",
    role: "tabpanel",
    tabindex: 0,
  });
  readonly node = h(
    "main",
    {},
    this.#tabs,
    this.#breadcrumbs,
    this.#status,
    this.#selection,
    this.#panel,
  );
  // The file and range on screen. Redrawing only when they change keeps focus.
  #shown?: { file: FileContent; range: Range };

  constructor(
    private readonly rootLabel: string,
    private readonly actions: FileViewActions,
  ) {}

  update(model: Navigation): void {
    const path = model.location?.path;

    this.#status.textContent = statusText(model);
    this.#status.classList.toggle("error", model.error !== "");
    this.#panel.setAttribute("aria-busy", String(model.loading));

    renderTabs(this.#tabs, model.tabs.all, path, this.actions.open, this.actions.close);
    this.#labelPanel(model.tabs.indexOf(path));
    renderBreadcrumbs(this.#breadcrumbs, this.rootLabel, path, this.actions.showDirectory);
    this.#showFile(model.file, model.location);
  }

  showSelection(location: Location): void {
    const { startLine, endLine } = location.range;
    this.#selection.textContent = `Selected lines ${startLine}–${endLine}`;
  }

  #labelPanel(tabIndex: number): void {
    if (tabIndex >= 0) {
      this.#panel.setAttribute("aria-labelledby", tabId(tabIndex));
      return;
    }

    this.#panel.removeAttribute("aria-labelledby");
    this.#panel.setAttribute("aria-label", "File content");
  }

  #showFile(file: FileContent | undefined, location: Location | undefined): void {
    if (!file || !location) {
      this.#panel.replaceChildren(h("p", {}, "No file open."));
      this.#shown = undefined;
      return;
    }

    const shown = this.#shown;
    if (shown?.file === file && sameRange(shown.range, location.range)) return;

    this.#shown = { file, range: { ...location.range } };
    this.#selection.textContent = "";
    renderCode(this.#panel, file, location.range, this.actions.select);
    this.#panel.querySelector<HTMLButtonElement>('.line-number[tabindex="0"]')?.focus();
  }
}

function statusText(model: Navigation): string {
  if (model.error) return model.error;
  if (model.loading) return "Loading file…";
  if (!model.location) return NO_FILE;

  const { path, version } = model.location;
  return `${path} · ${version.slice(0, 12)}`;
}

function sameRange(a: Range, b: Range): boolean {
  return (
    a.startLine === b.startLine &&
    a.startColumn === b.startColumn &&
    a.endLine === b.endLine &&
    a.endColumn === b.endColumn
  );
}
