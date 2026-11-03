import type {
  ContentProvider,
  Directory,
  FileContent,
  Query,
  SearchResults,
  Source,
} from "./types.js";

/** API failure with the stable gRPC status code (9 means a stale location). */
export class ProviderError extends Error {
  constructor(
    public readonly code: number,
    message: string,
  ) {
    super(message);
  }
}

/** Creates a REST provider. Supply a service token getter; tokens are never persisted.
 * The default base URL is same-origin. Custom fetch supports host authentication adapters.
 */
export function httpProvider(
  baseURL = "",
  token: () => string = () => "",
  fetcher = fetch,
): ContentProvider {
  const base = baseURL.replace(/\/$/, "");
  async function request<T>(
    route: string,
    signal: AbortSignal,
    body?: object,
  ): Promise<T> {
    const response = await fetcher(base + route, {
      method: body ? "POST" : "GET",
      signal,
      credentials: "omit",
      cache: "no-store",
      redirect: "error",
      headers: {
        Authorization: `Bearer ${token()}`,
        ...(body ? { "Content-Type": "application/json" } : {}),
      },
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!response.ok) {
      const error = (await response.json().catch(() => ({}))) as {
        code?: number;
      };
      const messages: Record<number, string> = {
        3: "Invalid request or range.",
        5: "File or source no longer exists.",
        7: "Path is outside the accessible root.",
        8: "File exceeds the source limit.",
        9: "Content changed. Reload to open the current version.",
        12: "Unsupported content or operation.",
        16: "Authentication required.",
      };
      throw new ProviderError(
        error.code ?? 13,
        messages[error.code ?? 13] ?? `Request failed (${response.status}).`,
      );
    }
    return (await response.json()) as T;
  }
  function route(
    source: Source,
    resource: string,
    fields?: Record<string, string>,
  ): string {
    const query = new URLSearchParams({
      sourceVersion: source.version,
      ...fields,
    });
    return `/v1/explorer/sources/${encodeURIComponent(source.id)}/${resource}?${query}`;
  }
  return {
    async sources(signal) {
      return (
        await request<{ sources: Source[] }>("/v1/explorer/sources", signal)
      ).sources;
    },
    async browse(source, path, signal) {
      return (
        await request<{ directory: Directory }>(
          route(source, "tree", { path }),
          signal,
        )
      ).directory;
    },
    async read(source, path, expectedVersion, signal) {
      return (
        await request<{ file: FileContent }>(
          route(source, "file", { path, expectedVersion }),
          signal,
        )
      ).file;
    },
    async search(source: Source, query: Query, signal) {
      return (
        await request<{ results: SearchResults }>(
          route(source, "search"),
          signal,
          { ...query, sourceVersion: source.version },
        )
      ).results;
    },
  };
}
