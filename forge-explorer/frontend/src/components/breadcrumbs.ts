import { Separator } from "./layout.js";
import { Text } from "./messages.js";
import { Button } from "./controls.js";
import { h } from "./dom.js";

/** FileBreadcrumbs shows the root and each parent directory of path as buttons. */
export function FileBreadcrumbs(
  container: HTMLElement,
  rootLabel: string,
  path: string | undefined,
  showDirectory: (path: string) => unknown,
): void {
  const parts = path?.split("/") ?? [];
  const crumbs: Node[] = [BreadcrumbItem(rootLabel, () => showDirectory("."))];

  parts.forEach((part, index) => {
    const isFile = index === parts.length - 1;
    const directory = parts.slice(0, index + 1).join("/");

    crumbs.push(
      BreadcrumbSeparator(),
      BreadcrumbItem(part, isFile ? undefined : () => showDirectory(directory)),
    );
  });

  container.replaceChildren(...crumbs);
}

/** Breadcrumbs labels the current path navigation. */
export function Breadcrumbs(): HTMLElement {
  return h("nav", { class: "breadcrumbs", "aria-label": "Breadcrumbs" });
}

/** BreadcrumbItem is a parent-directory action or the current file label. */
export function BreadcrumbItem(label: string, navigate?: () => unknown): HTMLElement {
  if (navigate) return Button(label, navigate);

  const current = Text(label);
  current.setAttribute("aria-current", "page");
  return current;
}

/** BreadcrumbSeparator visually separates path segments without spoken punctuation. */
export function BreadcrumbSeparator(): HTMLElement {
  return Separator("/");
}
