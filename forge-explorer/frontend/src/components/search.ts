import type { ContentProvider, Location, Source } from "../types.js";
import { SearchModel } from "../model/search.js";
import { focusSibling } from "./dom.js";
import { Panel, PanelBody, PanelHeader } from "./layout.js";
import { ResultSummary, StatusMessage } from "./messages.js";
import { SearchBar, SearchResultItem, SearchResults } from "./search-parts.js";

/** SearchPanel renders source-bound search state independently of file navigation. */
export class SearchPanel {
  readonly #bar = new SearchBar(() => this.#model.cancel());
  readonly #status = StatusMessage();
  readonly #results = SearchResults();
  readonly node = Panel("section", { class: "search", "aria-label": "Search files" },
    PanelHeader(this.#bar.node, this.#status), PanelBody(this.#results));
  readonly #model: SearchModel;

  constructor(
    provider: ContentProvider,
    source: Source,
    private readonly navigate: (location: Location) => unknown,
    signal: AbortSignal,
  ) {
    this.#model = new SearchModel(provider, source, signal, () => this.#render());
    this.#render();
    this.#bar.node.addEventListener("submit", (event) => {
      event.preventDefault();
      void this.#model.run({
        text: this.#bar.query.value,
        pathGlob: this.#bar.options.filter.value,
        files: this.#bar.options.mode.value === "files",
      });
    }, { signal });
    this.#results.addEventListener("keydown", (event) => {
      focusSibling(event, [...this.#results.querySelectorAll("button")]);
    }, { signal });
  }

  #render(): void {
    const { results, busy, message } = this.#model;
    this.#status.textContent = results ? ResultSummary(results.matches.length, results.state) : message;
    this.#results.replaceChildren(...(results?.matches ?? []).map((match) => SearchResultItem(match, this.navigate)));
    this.node.setAttribute("aria-busy", String(busy));
    this.#bar.cancel.disabled = !busy;
  }
}
