/** Public repository identity from the management listing. */
export interface RepositoryIdentity {
  readonly sourceId: string;
  readonly path: string;
}

type Listing =
  | { state: "ready"; repositories: RepositoryIdentity[] }
  | { state: "empty" | "unconfigured" | "failed" };

/** Project untrusted listing or storage data onto the public identity fields. */
export function readRepositoryIdentity(value: unknown): RepositoryIdentity | undefined {
  if (typeof value !== "object" || value === null || !("sourceId" in value) || !("path" in value)) {
    return;
  }
  const { sourceId, path } = value;
  if (typeof sourceId === "string" && sourceId.length && typeof path === "string" && path.length) {
    return { sourceId, path };
  }
}

/** Read a complete repository listing from the management REST API. */
export async function listRepositories(managementFetch: typeof fetch): Promise<Listing> {
  try {
    const response = await managementFetch("/v1/repositories", {
      method: "GET",
      cache: "no-store",
      redirect: "error",
    });
    if (response.ok) {
      return parseListing(await response.json());
    }
  } catch {
    // Network and decoding failures share the failed listing state.
  }
  return { state: "failed" };
}

function parseListing(value: unknown): Listing {
  if (typeof value !== "object" || value === null || !("configured" in value)) {
    return { state: "failed" };
  }
  if (value.configured === false) {
    return { state: "unconfigured" };
  }
  if (value.configured !== true || !("repositories" in value) || !Array.isArray(value.repositories)) {
    return { state: "failed" };
  }
  const repositories = value.repositories.map(readRepositoryIdentity);
  if (!repositories.every(repository => repository !== undefined)) {
    return { state: "failed" };
  }
  if (repositories.length === 0) {
    return { state: "empty" };
  }
  return { state: "ready", repositories };
}
