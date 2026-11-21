import { readRepositoryIdentity } from "./repositories.js";
import type { RepositoryIdentity } from "./repositories.js";

function key({ sourceId, path }: RepositoryIdentity): string {
  return JSON.stringify([sourceId, path]);
}

/** Return the storage key for a deployment's repository preference. */
export function repositorySelectionKey(deploymentId: string): string {
  return `sinterguild.repository-selection:${deploymentId}`;
}

/** Decode distinct public identities from a saved selection. */
export function readRepositorySelection(read: () => string | null): RepositoryIdentity[] {
  const repositories = new Map<string, RepositoryIdentity>();
  try {
    const saved: unknown = JSON.parse(read() ?? "null");
    if (Array.isArray(saved)) {
      for (const entry of saved) {
        const repository = readRepositoryIdentity(entry);
        if (repository) {
          repositories.set(key(repository), repository);
        }
      }
    }
  } catch {
    // Unavailable storage or invalid JSON has no completed selection.
  }
  return [...repositories.values()];
}

/** Track checked repositories from the current listing and restore visible saved entries. */
export class RepositorySelection {
  /** Unique visible repositories in listing order. */
  readonly repositories: readonly RepositoryIdentity[];
  readonly #checked: Set<string>;

  constructor(visible: readonly RepositoryIdentity[], read: () => string | null) {
    const byKey = new Map(visible.map(repository => [key(repository), repository]));
    const saved = new Set(readRepositorySelection(read).map(key));
    this.repositories = [...byKey.values()];
    this.#checked = new Set([...byKey.keys()].filter(id => saved.has(id)));
  }

  /** Return whether any visible repository is checked. */
  get hasSelection(): boolean {
    return this.#checked.size > 0;
  }

  /** Return whether this repository is checked. */
  has(repository: RepositoryIdentity): boolean {
    return this.#checked.has(key(repository));
  }

  /** Update the draft selection. */
  set(repository: RepositoryIdentity, checked: boolean): void {
    if (checked) {
      this.#checked.add(key(repository));
    } else {
      this.#checked.delete(key(repository));
    }
  }

  /** Return the checked, visible identities in listing order. */
  selected(): RepositoryIdentity[] {
    return this.repositories.filter(repository => this.has(repository))
      .map(({ sourceId, path }) => ({ sourceId, path }));
  }
}
