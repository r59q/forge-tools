import type { FileContent, Location, Range } from "../types.js";
import type { Navigation } from "../model/navigation.js";
import { Breadcrumbs, FileBreadcrumbs } from "./breadcrumbs.js";
import { renderCodeViewer } from "./code.js";
import { FileMetadata, SelectionSummary } from "./file-metadata.js";
import { EmptyState } from "./messages.js";
import { Panel, PanelBody, PanelHeader } from "./layout.js";
import { PANEL_ID, FileTabs, TabPanel, Tabs, tabId } from "./tabs.js";

/** FileWorkspaceActions are the navigation commands the file view can issue. */
export interface FileWorkspaceActions {
  open(location: Location): unknown;
  close(path: string): unknown;
  select(range: Range): void;
  showDirectory(path: string): unknown;
}

/** FileWorkspace shows the open tabs, breadcrumbs, status and the current file. */
export class FileWorkspace {
  readonly #tabs = Tabs();
  readonly #breadcrumbs = Breadcrumbs();
  readonly #metadata = new FileMetadata();
  readonly #selection = new SelectionSummary();
  readonly #panel = TabPanel(PANEL_ID);
  readonly node = Panel("main", {},
    PanelHeader(this.#tabs, this.#breadcrumbs, this.#metadata.node, this.#selection.node),
    PanelBody(this.#panel));
  // The file and range on screen. Redrawing only when they change keeps focus.
  #shown?: { file: FileContent; range: Range };

  constructor(
    private readonly rootLabel: string,
    private readonly actions: FileWorkspaceActions,
  ) {}

  /** update renders navigation state while retaining unchanged code rows. */
  update(model: Navigation): void {
    const path = model.location?.path;

    this.#metadata.update(model);
    this.#panel.setAttribute("aria-busy", String(model.loading));

    FileTabs(this.#tabs, model.tabs.all, path, this.actions.open, this.actions.close);
    this.#labelPanel(model.tabs.indexOf(path));
    FileBreadcrumbs(this.#breadcrumbs, this.rootLabel, path, this.actions.showDirectory);
    this.#showFile(model.file, model.location);
  }

  /** showSelection announces the selection before the host callback runs. */
  showSelection(location: Location): void {
    this.#selection.update(location.range);
  }

  #labelPanel(tabIndex: number): void {
    if (tabIndex >= 0) {
      this.#panel.removeAttribute("aria-label");
      this.#panel.setAttribute("aria-labelledby", tabId(tabIndex));
      return;
    }

    this.#panel.removeAttribute("aria-labelledby");
    this.#panel.setAttribute("aria-label", "File content");
  }

  #showFile(file: FileContent | undefined, location: Location | undefined): void {
    if (!file || !location) {
      this.#panel.replaceChildren(EmptyState("No file open."));
      this.#shown = undefined;
      return;
    }

    const shown = this.#shown;
    if (shown?.file === file && sameRange(shown.range, location.range)) return;

    this.#shown = { file, range: { ...location.range } };
    this.#selection.update();
    renderCodeViewer(this.#panel, file, location.range, this.actions.select);
    this.#panel.querySelector<HTMLButtonElement>('.line-number[tabindex="0"]')?.focus();
  }
}

function sameRange(a: Range, b: Range): boolean {
  return (
    a.startLine === b.startLine &&
    a.startColumn === b.startColumn &&
    a.endLine === b.endLine &&
    a.endColumn === b.endColumn
  );
}
