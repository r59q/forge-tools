import type {
  Capabilities,
  ContentProvider,
  ExplorerHandle,
  ExplorerOptions,
  Source,
} from "./types.js";
import { copyLocation } from "./model/location.js";
import { Navigation } from "./model/navigation.js";
import { button, h } from "./components/dom.js";
import { FileView } from "./components/file-view.js";
import { Search } from "./components/search.js";
import { Toolbar } from "./components/toolbar.js";
import { Tree } from "./components/tree.js";
import { styles } from "./styles.js";

const SEMANTIC_OPERATIONS: Array<keyof Capabilities> = [
  "symbols",
  "definitions",
  "references",
  "history",
];

/** createExplorer mounts one isolated explorer in host's shadow root.
 * The host owns the provider and source. dispose() cancels all instance work
 * and empties the host for reuse.
 */
export function createExplorer(
  host: HTMLElement,
  provider: ContentProvider,
  source: Source,
  options: ExplorerOptions = {},
): ExplorerHandle {
  source = structuredClone(source);
  const shadow = emptyShadowRoot(host);
  const lifetime = new AbortController();

  const files: FileView = new FileView(source.label, {
    open: (location) => model.open(location),
    close: (path) => closeTab(path),
    select: (range) => model.select(range),
    showDirectory: (path) => tree.show(path),
  });
  const model: Navigation = new Navigation(provider, source, withSelection(options, files), render);
  const toolbar = new Toolbar(model, closeTab);
  const tree = new Tree(provider, source, (path) => model.open(path), lifetime.signal);
  const search = new Search(provider, source, (location) => model.open(location), lifetime.signal);

  const sidebar = h("aside", {}, button("Refresh tree", () => tree.show(".")), tree.node, search.node);
  const shell = h(
    "section",
    { class: "explorer", "aria-label": `${source.label} explorer` },
    ...sourceHeader(source),
    toolbar.node,
    h("div", { class: "layout" }, sidebar, files.node),
    capabilityNote(provider, source),
  );
  shadow.append(h("style", {}, styles), shell);
  bindHistoryKeys(shell, model, lifetime.signal);

  function render(): void {
    if (lifetime.signal.aborted) return;

    toolbar.update();
    files.update(model);
  }

  async function closeTab(path: string): Promise<void> {
    await model.closeTab(path);
    if (!lifetime.signal.aborted) focusAfterClose(shadow, toolbar.back);
  }

  render();
  void tree.show(".");

  return {
    open: (target) => model.open(target),
    back: () => model.back(),
    forward: () => model.forward(),
    refresh: () => model.refresh(),
    current: () => model.location && copyLocation(model.location),
    dispose() {
      lifetime.abort();
      model.dispose();
      shadow.replaceChildren();
    },
  };
}

function emptyShadowRoot(host: HTMLElement): ShadowRoot {
  const shadow = host.shadowRoot ?? host.attachShadow({ mode: "open" });
  if (shadow.childNodes.length) throw new Error("Explorer host must be empty.");

  return shadow;
}

// withSelection shows the selection in the file view before notifying the host.
function withSelection(options: ExplorerOptions, files: FileView): ExplorerOptions {
  return {
    ...options,
    onSelection(location) {
      files.showSelection(location);
      options.onSelection?.(location);
    },
  };
}

function sourceHeader(source: Source): HTMLElement[] {
  const content = source.kind === "worktree" ? "live content" : source.version;

  return [
    h("h2", {}, source.label),
    h("p", { class: "source-caption" }, `${source.kind} · ${source.id} · ${content}`),
  ];
}

// capabilityNote states honestly which semantic operations are available.
function capabilityNote(provider: ContentProvider, source: Source): HTMLElement {
  const describe = (operation: keyof Capabilities): string => {
    const available = source.capabilities[operation] && provider[operation] !== undefined;
    return `${operation}: ${available ? "available through provider" : "unsupported"}`;
  };

  return h("p", { class: "capabilities" }, SEMANTIC_OPERATIONS.map(describe).join(" · "));
}

// bindHistoryKeys maps Alt+Left and Alt+Right to back and forward.
function bindHistoryKeys(node: HTMLElement, model: Navigation, signal: AbortSignal): void {
  const commands: Record<string, () => Promise<void>> = {
    ArrowLeft: () => model.back(),
    ArrowRight: () => model.forward(),
  };

  node.addEventListener("keydown", (event) => {
    const command = event.altKey ? commands[event.key] : undefined;
    if (!command) return;

    event.preventDefault();
    void command();
  }, { signal });
}

// focusAfterClose moves focus to the active tab, the tree, or fallback.
function focusAfterClose(shadow: ShadowRoot, fallback: HTMLElement): void {
  const target =
    shadow.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]') ??
    shadow.querySelector<HTMLElement>(".tree button:not(:disabled)") ??
    fallback;

  target.focus();
}
