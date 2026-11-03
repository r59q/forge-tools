import type { Location } from "../types.js";
import { h } from "./dom.js";

/** PANEL_ID is the id of the tab panel that shows the open file. */
export const PANEL_ID = "file-panel";

/** tabId returns the element id of the tab at index. */
export function tabId(index: number): string {
  return `file-tab-${index}`;
}

/** renderTabs is a scoped TypeScript adaptation of NudaUI animated-tabs.
 * It uses no document-wide queries or observers.
 */
export function renderTabs(
  container: HTMLElement,
  locations: Location[],
  active: string | undefined,
  open: (location: Location) => void,
  close: (path: string) => void,
): void {
  const tabs = locations.map((location, index) =>
    createTab(location, index, location.path === active, open),
  );
  const bar = h(
    "div",
    { class: "nuda-tabs__bar", role: "tablist", "aria-label": "Open files" },
    ...tabs,
  );

  bar.addEventListener("keydown", (event) => {
    const index = tabs.indexOf(event.target as HTMLButtonElement);
    const location = locations[index];
    if (!location) return;

    if (event.key === "Delete") {
      event.preventDefault();
      close(location.path);
      return;
    }
    moveFocus(event, tabs, index);
  });

  container.replaceChildren(bar);
}

function createTab(
  location: Location,
  index: number,
  active: boolean,
  open: (location: Location) => void,
): HTMLButtonElement {
  const tab = h(
    "button",
    {
      type: "button",
      id: tabId(index),
      class: active ? "nuda-tabs__tab is-active" : "nuda-tabs__tab",
      role: "tab",
      "aria-controls": PANEL_ID,
      "aria-selected": String(active),
      tabindex: active ? 0 : -1,
    },
    location.path,
  );

  tab.addEventListener("click", () => open(location));
  return tab;
}

// moveFocus implements roving focus: only the focused tab is in the tab order.
function moveFocus(event: KeyboardEvent, tabs: HTMLButtonElement[], index: number): void {
  const target = keyTarget(event.key, index, tabs.length);
  if (target === undefined) return;

  event.preventDefault();
  tabs.forEach((tab, tabIndex) => {
    tab.tabIndex = tabIndex === target ? 0 : -1;
  });
  tabs[target]?.focus();
}

function keyTarget(key: string, index: number, count: number): number | undefined {
  switch (key) {
    case "ArrowRight":
      return (index + 1) % count;
    case "ArrowLeft":
      return (index - 1 + count) % count;
    case "Home":
      return 0;
    case "End":
      return count - 1;
    default:
      return undefined;
  }
}
