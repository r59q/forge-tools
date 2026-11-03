import { button, h } from "./dom.js";

/** renderBreadcrumbs shows the root and each parent directory of path as buttons. */
export function renderBreadcrumbs(
  container: HTMLElement,
  rootLabel: string,
  path: string | undefined,
  showDirectory: (path: string) => unknown,
): void {
  const parts = path?.split("/") ?? [];
  const crumbs: Node[] = [button(rootLabel, () => showDirectory("."))];

  parts.forEach((part, index) => {
    const isFile = index === parts.length - 1;
    const directory = parts.slice(0, index + 1).join("/");

    crumbs.push(
      h("span", {}, "/"),
      isFile ? h("span", {}, part) : button(part, () => showDirectory(directory)),
    );
  });

  container.replaceChildren(...crumbs);
}
