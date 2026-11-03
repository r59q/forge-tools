import type { Location, Match } from "../types.js";
import { Button, Input, Select } from "./controls.js";
import { h } from "./dom.js";
import { ScrollArea } from "./layout.js";
import { Text } from "./messages.js";

const MAX_QUERY = 256;

/** SearchOptions exposes native text/file mode and optional path filtering. */
export class SearchOptions {
  readonly mode = Select([
    { value: "text", label: "Text" },
    { value: "files", label: "Files" },
  ], { "aria-label": "Search mode" });
  readonly filter = Input("text", {
    maxlength: MAX_QUERY,
    placeholder: "Path glob, e.g. src/*.go",
    "aria-label": "Path filter",
  });
  readonly node = h("div", { class: "search-options" }, this.mode, this.filter);
}

/** SearchBar submits native validated queries and exposes cancellation. */
export class SearchBar {
  readonly query = Input("search", {
    required: true,
    maxlength: MAX_QUERY,
    placeholder: "Find text or files",
    "aria-label": "Search query",
  });
  readonly options = new SearchOptions();
  readonly cancel: HTMLButtonElement;
  readonly node: HTMLFormElement;

  constructor(cancel: () => void) {
    this.cancel = Button("Cancel search", cancel, { disabled: true });
    this.node = h("form", {}, this.query, this.options.node,
      Button("Search", () => {}, { type: "submit" }), this.cancel);
  }
}

/** SearchResults contains bounded, keyboard-navigable result rows. */
export function SearchResults(): HTMLUListElement {
  return ScrollArea("ul", { class: "search-results" });
}

/** SearchResultItem preserves the location and safely displays its preview. */
export function SearchResultItem(match: Match, navigate: (location: Location) => unknown): HTMLLIElement {
  const { path, range } = match.location;
  const title = range.startLine ? `${path}:${range.startLine}` : path;
  const open = Button(title, () => navigate(match.location));

  return h("li", {}, open, Text(match.preview, "code"));
}
