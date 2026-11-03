import type { ContentProvider, Location, Match, SearchResults, Source } from "../types.js";
import { validateLocation } from "../model/location.js";
import { button, focusSibling, h, resultSummary } from "./dom.js";

const MAX_QUERY = 256;

/** Search is a text and file search form. It owns its request and cancellation,
 * independently of file navigation.
 */
export class Search {
  readonly #query = h("input", {
    type: "search",
    required: true,
    maxlength: MAX_QUERY,
    placeholder: "Find text or files",
    "aria-label": "Search query",
  });
  readonly #mode = h(
    "select",
    { "aria-label": "Search mode" },
    h("option", { value: "text" }, "Text"),
    h("option", { value: "files" }, "Files"),
  );
  readonly #filter = h("input", {
    maxlength: MAX_QUERY,
    placeholder: "Path glob, e.g. src/*.go",
    "aria-label": "Path filter",
  });
  readonly #cancel = button("Cancel search", () => this.#stop(), { disabled: true });
  readonly #status = h("p", { role: "status" }, "Enter a query to search this source.");
  readonly #results = h("ul", { class: "search-results" });
  readonly node: HTMLElement;
  #request?: AbortController;

  constructor(
    private readonly provider: ContentProvider,
    private readonly source: Source,
    private readonly navigate: (location: Location) => unknown,
    private readonly signal: AbortSignal,
  ) {
    // Submitting the form runs the search.
    const submit = button("Search", () => {}, { type: "submit" });
    const form = h("form", {}, this.#query, this.#mode, this.#filter, submit, this.#cancel);
    this.node = h(
      "section",
      { class: "search", "aria-label": "Search files" },
      form,
      this.#status,
      this.#results,
    );

    form.addEventListener("submit", (event) => {
      event.preventDefault();
      void this.#run();
    }, { signal });
    this.#results.addEventListener("keydown", (event) => {
      focusSibling(event, [...this.#results.querySelectorAll("button")]);
    }, { signal });
  }

  async #run(): Promise<void> {
    if (!this.#query.value.trim() || this.signal.aborted) return;

    this.#request?.abort();
    this.#request = new AbortController();
    const signal = AbortSignal.any([this.signal, this.#request.signal]);
    this.#start();

    try {
      const results = await this.provider.search(this.source, this.#queryValue(), signal);
      if (!signal.aborted) this.#show(results);
    } catch (error) {
      if (!signal.aborted) this.#status.textContent = (error as Error).message;
    } finally {
      if (!signal.aborted) this.#setBusy(false);
    }
  }

  #queryValue(): { text: string; pathGlob: string; files: boolean } {
    return {
      text: this.#query.value,
      pathGlob: this.#filter.value,
      files: this.#mode.value === "files",
    };
  }

  #start(): void {
    this.#status.textContent = "Searching…";
    this.#results.replaceChildren();
    this.#setBusy(true);
  }

  #stop(): void {
    this.#request?.abort();
    this.#status.textContent = "Search canceled; results may be incomplete.";
    this.#setBusy(false);
  }

  #show(results: SearchResults): void {
    results.matches.forEach((match) => this.#validate(match));

    this.#results.replaceChildren(...results.matches.map((match) => this.#item(match)));
    this.#status.textContent = resultSummary(results.matches.length, results.state);
  }

  #validate(match: Match): void {
    validateLocation(this.source, match.location);
    if (!match.location.version) throw new Error("Search result has no content version.");
  }

  #item(match: Match): HTMLElement {
    const open = button(matchTitle(match.location), () => this.navigate(match.location));

    return h("li", {}, open, h("code", {}, match.preview));
  }

  #setBusy(busy: boolean): void {
    this.node.setAttribute("aria-busy", String(busy));
    this.#cancel.disabled = !busy;
  }
}

function matchTitle(location: Location): string {
  const line = location.range.startLine;

  return line ? `${location.path}:${line}` : location.path;
}
