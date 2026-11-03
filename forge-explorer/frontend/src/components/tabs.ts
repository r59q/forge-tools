import type { Location } from "../types.js";
import { element } from "./dom.js";

/** Scoped TypeScript adaptation of NudaUI animated-tabs. No document-wide queries or observers. */
export function renderTabs(
  container: HTMLElement,
  locations: Location[],
  active: string | undefined,
  open: (location: Location) => void,
  close: (path: string) => void,
): void {
  const bar = element("div", "", "nuda-tabs__bar");
  bar.setAttribute("role", "tablist");
  bar.setAttribute("aria-label", "Open files");
  const buttons: HTMLButtonElement[] = [];
  locations.forEach((location, index) => {
    const tab = element("button", location.path, "nuda-tabs__tab");
    tab.type = "button";
    tab.id = `file-tab-${index}`;
    tab.setAttribute("role", "tab");
    tab.setAttribute("aria-controls", "file-panel");
    tab.setAttribute("aria-selected", String(location.path === active));
    tab.classList.toggle("is-active", location.path === active);
    tab.tabIndex = location.path === active ? 0 : -1;
    tab.addEventListener("click", () => open(location));
    tab.addEventListener("keydown", (event) => {
      let target: number | undefined;
      if (event.key === "ArrowRight") target = (index + 1) % locations.length;
      if (event.key === "ArrowLeft")
        target = (index - 1 + locations.length) % locations.length;
      if (event.key === "Home") target = 0;
      if (event.key === "End") target = locations.length - 1;
      if (event.key === "Delete") {
        event.preventDefault();
        close(location.path);
      }
      if (target !== undefined) {
        event.preventDefault();
        buttons.forEach((button, index) => {
          button.tabIndex = index === target ? 0 : -1;
        });
        buttons[target]?.focus();
      }
    });
    buttons.push(tab);
    bar.append(tab);
  });
  container.replaceChildren(bar);
}
