import type { Location } from "../types.js";
import { copyLocation } from "./location.js";

const MAX_ENTRIES = 100;

/** History is a bounded back/forward stack of visited locations. */
export class History {
  #entries: Location[] = [];
  #index = -1;

  get length(): number {
    return this.#entries.length;
  }

  get canGoBack(): boolean {
    return this.#index > 0;
  }

  get canGoForward(): boolean {
    return this.#index < this.#entries.length - 1;
  }

  /** peek returns the entry step positions from the current one. */
  peek(step: number): Location | undefined {
    const entry = this.#entries[this.#index + step];

    return entry && copyLocation(entry);
  }

  /** go moves the current position by step. */
  go(step: number): void {
    this.#index += step;
  }

  /** push drops forward entries and makes location the current entry. */
  push(location: Location): void {
    this.#entries = this.#entries.slice(0, this.#index + 1);
    this.#entries.push(copyLocation(location));

    if (this.#entries.length > MAX_ENTRIES) this.#entries.shift();
    this.#index = this.#entries.length - 1;
  }

  /** replaceCurrent updates the current entry, e.g. after a selection. */
  replaceCurrent(location: Location): void {
    if (this.#index >= 0) this.#entries[this.#index] = copyLocation(location);
  }
}
