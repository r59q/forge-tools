import type { ContentProvider, Directory, Entry, Source } from "../types.js";
import { button, focusSibling, h, resultSummary } from "./dom.js";

/** Tree is a lazily loaded directory tree. Directories load when first opened. */
export class Tree {
  readonly node = h("nav", { class: "tree", "aria-label": "Files" });
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
    parent.replaceChildren(h("p", {}, "Loading directory…"));
    parent.setAttribute("aria-busy", "true");

    try {
      const directory = await this.provider.browse(this.source, path, signal);
      if (!signal.aborted) this.#showDirectory(parent, directory, signal);
    } catch (error) {
      if (!signal.aborted) {
        parent.replaceChildren(h("p", { class: "error" }, (error as Error).message));
      }
    } finally {
      if (!signal.aborted) parent.setAttribute("aria-busy", "false");
    }
  }

  #showDirectory(parent: HTMLElement, directory: Directory, signal: AbortSignal): void {
    if (directory.source !== this.source.id) {
      throw new Error("Directory belongs to another source.");
    }

    const items = directory.entries.map((entry) => h("li", {}, this.#entry(entry, signal)));
    parent.replaceChildren(h("ul", {}, ...items));

    const { entries, state } = directory;
    if (!entries.length || state.skipped || state.truncated) {
      parent.append(h("p", { class: "partial" }, resultSummary(entries.length, state)));
    }
  }

  #entry(entry: Entry, signal: AbortSignal): HTMLElement {
    const label = entry.path.split("/").at(-1) ?? entry.path;

    if (entry.kind === "directory") return this.#directory(entry.path, label, signal);
    return this.#file(entry, label);
  }

  #directory(path: string, label: string, signal: AbortSignal): HTMLElement {
    const children = h("div");
    const details = h("details", {}, h("summary", {}, label), children);
    let loaded = false;

    details.addEventListener(
      "toggle",
      () => {
        if (!details.open || loaded) return;
        loaded = true;
        void this.#fill(children, path, signal);
      },
      { signal },
    );

    return details;
  }

  #file(entry: Entry, label: string): HTMLElement {
    const supported = entry.kind === "file";

    return button(label, () => this.navigate(entry.path), {
      class: "file",
      disabled: !supported,
      title: supported ? undefined : "Unsupported entry (symlink or special file)",
    });
  }
}

// visibleControls lists the tree's focusable controls, skipping collapsed ones.
function visibleControls(tree: HTMLElement): HTMLElement[] {
  const controls = tree.querySelectorAll<HTMLElement>("summary,button:not(:disabled)");

  return [...controls].filter((control) => control.getClientRects().length > 0);
}
