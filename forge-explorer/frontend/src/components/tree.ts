import type { ContentProvider, Source } from "../types";
import { button, element, resultSummary } from "./dom";

/** A lazy directory disclosure tree with instance-owned cancellation and keyboard focus. */
export function createTree(
  provider: ContentProvider,
  source: Source,
  navigate: (path: string) => void,
  signal: AbortSignal,
): { node: HTMLElement; show: (path: string) => Promise<void> } {
  const node = element("nav", "", "tree");
  node.setAttribute("aria-label", "Files");
  let current: AbortController | undefined;
  node.addEventListener(
    "keydown",
    (event) => {
      if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
      const controls = [
        ...node.querySelectorAll<HTMLElement>("summary,button:not(:disabled)"),
      ].filter((control) => control.getClientRects().length > 0);
      const index = controls.indexOf(event.target as HTMLElement);
      const target = controls[index + (event.key === "ArrowDown" ? 1 : -1)];
      if (target) {
        event.preventDefault();
        target.focus();
      }
    },
    { signal },
  );

  async function fill(
    parent: HTMLElement,
    path: string,
    request: AbortSignal,
  ): Promise<void> {
    parent.replaceChildren(element("p", "Loading directory…"));
    parent.setAttribute("aria-busy", "true");
    try {
      const directory = await provider.browse(source, path, request);
      if (request.aborted) return;
      if (directory.source !== source.id)
        throw new Error("Directory belongs to another source.");
      const list = element("ul");
      for (const entry of directory.entries) {
        const item = element("li");
        const label = entry.path.split("/").at(-1) ?? entry.path;
        if (entry.kind === "directory") {
          const details = element("details");
          const summary = element("summary", label);
          const children = element("div");
          let loaded = false;
          details.addEventListener(
            "toggle",
            () => {
              if (details.open && !loaded) {
                loaded = true;
                void fill(children, entry.path, request);
              }
            },
            { signal: request },
          );
          details.append(summary, children);
          item.append(details);
        } else {
          const file = button(label, () => navigate(entry.path), "file");
          file.disabled = entry.kind !== "file";
          if (file.disabled)
            file.title = "Unsupported entry (symlink or special file)";
          item.append(file);
        }
        list.append(item);
      }
      parent.replaceChildren(list);
      if (
        !directory.entries.length ||
        directory.state.skipped ||
        directory.state.truncated
      ) {
        parent.append(
          element(
            "p",
            resultSummary(directory.entries.length, directory.state),
            "partial",
          ),
        );
      }
    } catch (error) {
      if (!request.aborted)
        parent.replaceChildren(element("p", (error as Error).message, "error"));
    } finally {
      if (!request.aborted) parent.setAttribute("aria-busy", "false");
    }
  }
  return {
    node,
    async show(path) {
      current?.abort();
      current = new AbortController();
      const request = AbortSignal.any([signal, current.signal]);
      await fill(node, path, request);
    },
  };
}
