import type { Entry } from "../types.js";
import { Button, Disclosure } from "./controls.js";
import { h } from "./dom.js";

/** FileTreeItem renders a lazy directory, file action or disabled unsupported entry.
 * File rows retain discovery state; directories use native disclosure semantics.
 */
export function FileTreeItem(
  entry: Entry,
  navigate: (path: string) => unknown,
  fill: (parent: HTMLElement, path: string) => unknown,
  signal: AbortSignal,
): HTMLElement {
  const label = entry.path.split("/").at(-1) ?? entry.path;
  const content = entry.kind === "directory"
    ? DirectoryItem(entry.path, label, fill, signal)
    : FileItem(entry, label, navigate);

  return h("li", {}, content);
}

function DirectoryItem(
  path: string,
  label: string,
  fill: (parent: HTMLElement, path: string) => unknown,
  signal: AbortSignal,
): HTMLElement {
  const children = h("div");
  const details = Disclosure(label, children);
  let loaded = false;

  details.addEventListener("toggle", () => {
    if (!details.open || loaded) return;
    loaded = true;
    void fill(children, path);
  }, { signal });

  return details;
}

function FileItem(entry: Entry, label: string, navigate: (path: string) => unknown): HTMLButtonElement {
  const supported = entry.kind === "file";

  return Button(label, () => navigate(entry.path), {
    class: "file",
    disabled: !supported,
    title: supported ? undefined : "Unsupported entry (symlink or special file)",
  });
}
