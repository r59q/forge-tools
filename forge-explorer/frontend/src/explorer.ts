import type {
  ContentProvider,
  ExplorerHandle,
  ExplorerOptions,
  Source,
} from "./types";
import { copyLocation, Navigation } from "./navigation";
import { button, element } from "./components/dom";
import { renderTabs } from "./components/tabs";
import { renderCode } from "./components/code";
import { createTree } from "./components/tree";
import { createSearch } from "./components/search";
import { styles } from "./styles";

/** Mount one isolated explorer. The host owns provider/source selection; disposal cancels all instance work.
 * Styles and listeners live inside this host's ShadowRoot. Reuse the host after dispose().
 */
export function createExplorer(
  host: HTMLElement,
  provider: ContentProvider,
  source: Source,
  options: ExplorerOptions = {},
): ExplorerHandle {
  source = structuredClone(source);
  const shadow = host.shadowRoot ?? host.attachShadow({ mode: "open" });
  if (shadow.childNodes.length) throw new Error("Explorer host must be empty.");
  const lifetime = new AbortController();
  const style = element("style", styles);
  const shell = element("section", "", "explorer");
  shell.setAttribute("aria-label", `${source.label} explorer`);
  const heading = element("h2", source.label);
  const subtitle = element(
    "p",
    `${source.kind} · ${source.id} · ${source.kind === "worktree" ? "live content" : source.version}`,
    "source-caption",
  );
  const toolbar = element("div", "", "toolbar");
  const tabs = element("div", "", "nuda-tabs");
  const breadcrumbs = element("nav", "", "breadcrumbs");
  breadcrumbs.setAttribute("aria-label", "Breadcrumbs");
  const status = element("p", "Choose a file to begin.", "status");
  status.setAttribute("role", "status");
  const panel = element("div", "", "nuda-tabs__panel is-active");
  panel.id = "file-panel";
  panel.setAttribute("role", "tabpanel");
  panel.tabIndex = 0;
  const selection = element("p", "", "selection");
  selection.setAttribute("role", "status");
  const sidebar = element("aside");
  const content = element("main");
  const layout = element("div", "", "layout");
  let displayedFile: object | undefined;
  let displayedRange = "";
  const model = new Navigation(
    provider,
    source,
    {
      ...options,
      onSelection(location) {
        selection.textContent = `Selected lines ${location.range.startLine}–${location.range.endLine}`;
        options.onSelection?.(location);
      },
    },
    render,
  );
  const back = button("Back", () => {
    void model.back();
  });
  const forward = button("Forward", () => {
    void model.forward();
  });
  const reload = button("Reload file", () => {
    void model.refresh();
  });
  const close = button("Close file", () => {
    if (model.location) void closeTab(model.location.path);
  });
  toolbar.append(back, forward, reload, close);
  const tree = createTree(
    provider,
    source,
    (path) => {
      void model.open(path);
    },
    lifetime.signal,
  );
  sidebar.append(
    button("Refresh tree", () => {
      void tree.show(".");
    }),
    tree.node,
    createSearch(
      provider,
      source,
      (location) => {
        void model.open(location);
      },
      lifetime.signal,
    ),
  );
  content.append(tabs, breadcrumbs, status, selection, panel);
  const capabilities = element("p", "", "capabilities");
  capabilities.textContent = ["symbols", "definitions", "references", "history"]
    .map((key) => {
      const capability = key as keyof Source["capabilities"];
      return `${key}: ${source.capabilities[capability] && provider[capability] ? "available through provider" : "unsupported"}`;
    })
    .join(" · ");
  layout.append(sidebar, content);
  shell.append(heading, subtitle, toolbar, layout, capabilities);
  shadow.append(style, shell);
  shell.addEventListener(
    "keydown",
    (event) => {
      if (event.altKey && event.key === "ArrowLeft") {
        event.preventDefault();
        void model.back();
      }
      if (event.altKey && event.key === "ArrowRight") {
        event.preventDefault();
        void model.forward();
      }
    },
    { signal: lifetime.signal },
  );

  function render(): void {
    if (lifetime.signal.aborted) return;
    back.disabled = model.index < 1 || model.loading;
    forward.disabled = model.index >= model.history.length - 1 || model.loading;
    close.disabled = !model.location;
    reload.disabled = !model.requestedPath;
    panel.setAttribute("aria-busy", String(model.loading));
    status.textContent =
      model.error ||
      (model.loading
        ? "Loading file…"
        : model.location
          ? `${model.location.path} · ${model.location.version.slice(0, 12)}`
          : "Choose a file to begin.");
    status.classList.toggle("error", !!model.error);
    renderTabs(
      tabs,
      model.tabs,
      model.location?.path,
      (location) => {
        void model.open(location);
      },
      (path) => {
        void closeTab(path);
      },
    );
    const tabIndex = model.tabs.findIndex(
      (tab) => tab.path === model.location?.path,
    );
    if (tabIndex >= 0)
      panel.setAttribute("aria-labelledby", `file-tab-${tabIndex}`);
    else {
      panel.removeAttribute("aria-labelledby");
      panel.setAttribute("aria-label", "File content");
    }
    renderBreadcrumbs();
    if (!model.file || !model.location) {
      panel.replaceChildren(element("p", "No file open."));
      displayedFile = undefined;
      return;
    }
    const rangeKey = JSON.stringify(model.location.range);
    if (model.file !== displayedFile || rangeKey !== displayedRange) {
      displayedFile = model.file;
      displayedRange = rangeKey;
      selection.textContent = "";
      renderCode(panel, model.file, model.location.range, (range) =>
        model.select(range),
      );
      panel
        .querySelector<HTMLButtonElement>('.line-number[tabindex="0"]')
        ?.focus();
    }
  }

  async function closeTab(path: string): Promise<void> {
    await model.closeTab(path);
    if (lifetime.signal.aborted) return;
    const focus =
      shadow.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]') ??
      shadow.querySelector<HTMLElement>(".tree button:not(:disabled)") ??
      back;
    focus.focus();
  }

  function renderBreadcrumbs(): void {
    breadcrumbs.replaceChildren(
      button(source.label, () => {
        void tree.show(".");
      }),
    );
    const parts = model.location?.path.split("/") ?? [];
    parts.forEach((part, index) => {
      breadcrumbs.append(element("span", "/"));
      if (index === parts.length - 1) breadcrumbs.append(element("span", part));
      else
        breadcrumbs.append(
          button(part, () => {
            void tree.show(parts.slice(0, index + 1).join("/"));
          }),
        );
    });
  }

  render();
  void tree.show(".");
  return {
    open: (target) => model.open(target),
    back: () => model.back(),
    forward: () => model.forward(),
    refresh: () => model.refresh(),
    current: () => (model.location ? copyLocation(model.location) : undefined),
    dispose() {
      lifetime.abort();
      model.dispose();
      shadow.replaceChildren();
    },
  };
}
