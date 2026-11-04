import type { ContentProvider, Query, SearchResults, Source } from "../types.js";
import { validateLocation } from "./location.js";

/** SearchModel owns source-bound results and cancellation independently of the DOM. */
export class SearchModel {
  results?: SearchResults;
  busy = false;
  message = "Enter a query to search this source.";
  #request?: AbortController;

  constructor(
    private readonly provider: ContentProvider,
    private readonly source: Source,
    private readonly signal: AbortSignal,
    private readonly changed: () => void,
  ) {}

  /** run replaces the prior search, ignoring late responses and disposed instances. */
  async run(query: Query): Promise<void> {
    if (!query.text.trim() || this.signal.aborted) return;

    this.#request?.abort();
    this.#request = new AbortController();
    const signal = AbortSignal.any([this.signal, this.#request.signal]);
    this.results = undefined;
    this.message = "Searching…";
    this.busy = true;
    this.changed();

    try {
      const results = await this.provider.search(this.source, query, signal);
      if (signal.aborted) return;
      this.#validate(results);
      this.results = results;
      this.message = "";
    } catch (error) {
      if (!signal.aborted) this.message = (error as Error).message;
    } finally {
      if (!signal.aborted) {
        this.busy = false;
        this.changed();
      }
    }
  }

  /** cancel aborts the current request and announces incomplete results. */
  cancel(): void {
    if (this.signal.aborted) return;
    this.#request?.abort();
    this.message = "Search canceled; results may be incomplete.";
    this.busy = false;
    this.changed();
  }

  #validate(results: SearchResults): void {
    for (const match of results.matches) {
      validateLocation(this.source, match.location);
      if (!match.location.version) throw new Error("Search result has no content version.");
    }
  }
}
