import { ScrollArea } from "./layout.js";
import { FileTreeItem } from "./tree-item.js";
import { ResultSummary, StatusMessage } from "./messages.js";
import type { ContentProvider, Directory, Source } from "../types.js";
import { focusSibling, h } from "./dom.js";

/** FileTree is a lazily loaded directory tree. Directories load when first opened. */
export class FileTree {
  readonly node = ScrollArea("nav", { class: "tree", "aria-label": "Files" });
  #request?: AbortController;

  constructor(
    private readonly provider: ContentProvider,
    private readonly source: Source,
    private readonly navigate: (path: string) => unknown,
    private readonly signal: AbortSignal,
  ) {
    this.node.addEventListener(
      "keydown",
      (event) => focusSibling(event, visibleControls(this.node)),
      { signal },
    );
  }

  /** show replaces the tree with the directory at path. */
  async show(path: string): Promise<void> {
    this.#request?.abort();
    this.#request = new AbortController();

    await this.#fill(this.node, path, AbortSignal.any([this.signal, this.#request.signal]));
  }

  async #fill(parent: HTMLElement, path: string, signal: AbortSignal): Promise<void> {
    parent.replaceChildren(StatusMessage("Loading directory…"));
    parent.setAttribute("aria-busy", "true");

    try {
      const directory = await this.provider.browse(this.source, path, signal);
      if (!signal.aborted) this.#showDirectory(parent, directory, signal);
    } catch (error) {
      if (!signal.aborted) {
        parent.replaceChildren(StatusMessage((error as Error).message, { class: "error" }));
      }
    } finally {
      if (!signal.aborted) parent.setAttribute("aria-busy", "false");
    }
  }

  #showDirectory(parent: HTMLElement, directory: Directory, signal: AbortSignal): void {
    if (directory.source !== this.source.id) {
      throw new Error("Directory belongs to another source.");
    }

    const items = directory.entries.map((entry) => FileTreeItem(
      entry, this.navigate, (parent, path) => this.#fill(parent, path, signal), signal,
    ));
    parent.replaceChildren(h("ul", {}, ...items));

    const { entries, state } = directory;
    if (!entries.length || state.skipped || state.truncated || state.canceled) {
      parent.append(StatusMessage(ResultSummary(entries.length, state), { class: "partial" }));
    }
  }

}

// visibleControls lists the tree's focusable controls, skipping collapsed ones.
function visibleControls(tree: HTMLElement): HTMLElement[] {
  const controls = tree.querySelectorAll<HTMLElement>("summary,button:not(:disabled)");

  return [...controls].filter((control) => control.getClientRects().length > 0);
}
