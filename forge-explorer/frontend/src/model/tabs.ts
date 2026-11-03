import type { Location } from "../types.js";
import { copyLocation } from "./location.js";

const MAX_TABS = 20;

/** Tabs holds open files, at most one per path, oldest first. */
export class Tabs {
  #locations: Location[] = [];

  get length(): number {
    return this.#locations.length;
  }

  /** all returns copies of the open locations. */
  get all(): Location[] {
    return this.#locations.map(copyLocation);
  }

  indexOf(path: string | undefined): number {
    return this.#locations.findIndex((location) => location.path === path);
  }

  last(): Location | undefined {
    const location = this.#locations.at(-1);

    return location && copyLocation(location);
  }

  /** set updates the tab for location's path, or opens a new one. */
  set(location: Location): void {
    const index = this.indexOf(location.path);

    if (index >= 0) {
      this.#locations[index] = copyLocation(location);
      return;
    }

    this.#locations.push(copyLocation(location));
    if (this.#locations.length > MAX_TABS) this.#locations.shift();
  }

  remove(path: string): void {
    this.#locations = this.#locations.filter(
      (location) => location.path !== path,
    );
  }
}
