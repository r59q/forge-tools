import { CapabilitySummary, ExplorerShell, ExplorerSidebar } from "./components/shell.js";
import type {
  ContentProvider,
  ExplorerHandle,
  ExplorerOptions,
  Source,
} from "./types.js";
import { copyLocation } from "./model/location.js";
import { Navigation } from "./model/navigation.js";
import { h } from "./components/dom.js";
import { FileWorkspace } from "./components/file-view.js";
import { SearchPanel } from "./components/search.js";
import { ExplorerToolbar } from "./components/toolbar.js";
import { FileTree } from "./components/tree.js";
import { styles } from "./styles.js";

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

  const files: FileWorkspace = new FileWorkspace(source.label, {
    open: (location) => model.open(location),
    close: (path) => closeTab(path),
    select: (range) => model.select(range),
    showDirectory: (path) => tree.show(path),
  });
  const model: Navigation = new Navigation(provider, source, withSelection(options, files), render);
  const toolbar = new ExplorerToolbar(model, closeTab);
  const tree = new FileTree(provider, source, (path) => model.open(path), lifetime.signal);
  const search = new SearchPanel(provider, source, (location) => model.open(location), lifetime.signal);

  const sidebar = ExplorerSidebar(tree.node, search.node, () => tree.show("."));
  const shell = ExplorerShell(source, toolbar.node, sidebar, files.node, CapabilitySummary(provider, source));
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
function withSelection(options: ExplorerOptions, files: FileWorkspace): ExplorerOptions {
  return {
    ...options,
    onSelection(location) {
      files.showSelection(location);
      options.onSelection?.(location);
    },
  };
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
