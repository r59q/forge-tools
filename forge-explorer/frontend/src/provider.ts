import type {
  ContentProvider,
  Directory,
  FileContent,
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

const INTERNAL = 13;

const ERROR_MESSAGES: Record<number, string> = {
  3: "Invalid request or range.",
  5: "File or source no longer exists.",
  7: "Path is outside the accessible root.",
  8: "File exceeds the source limit.",
  9: "Content changed. Reload to open the current version.",
  12: "Unsupported content or operation.",
  16: "Authentication required.",
};

type Request = <T>(route: string, signal: AbortSignal, body?: object) => Promise<T>;

/** httpProvider creates a REST provider. Supply a service token getter; tokens are never persisted.
 * The default base URL is same-origin. A custom fetch supports host authentication adapters.
 */
export function httpProvider(
  baseURL = "",
  token: () => string = () => "",
  fetcher = fetch,
): ContentProvider {
  const request = requester(baseURL.replace(/\/$/, ""), token, fetcher);

  return {
    async sources(signal) {
      const body = await request<{ sources: Source[] }>("/v1/explorer/sources", signal);
      return body.sources;
    },
    async browse(source, path, signal) {
      const body = await request<{ directory: Directory }>(sourceRoute(source, "tree", { path }), signal);
      return body.directory;
    },
    async read(source, path, expectedVersion, signal) {
      const route = sourceRoute(source, "file", { path, expectedVersion });
      const body = await request<{ file: FileContent }>(route, signal);
      return body.file;
    },
    async search(source, query, signal) {
      const search = { ...query, sourceVersion: source.version };
      const body = await request<{ results: SearchResults }>(sourceRoute(source, "search"), signal, search);
      return body.results;
    },
  };
}

function requester(base: string, token: () => string, fetcher: typeof fetch): Request {
  return async <T>(route: string, signal: AbortSignal, body?: object): Promise<T> => {
    const response = await fetcher(base + route, requestInit(token(), signal, body));
    if (!response.ok) throw await providerError(response);

    return (await response.json()) as T;
  };
}

// requestInit never sends cookies, caches or follows redirects.
function requestInit(token: string, signal: AbortSignal, body?: object): RequestInit {
  const headers: Record<string, string> = { Authorization: `Bearer ${token}` };
  if (body) headers["Content-Type"] = "application/json";

  return {
    method: body ? "POST" : "GET",
    signal,
    credentials: "omit",
    cache: "no-store",
    redirect: "error",
    headers,
    body: body ? JSON.stringify(body) : undefined,
  };
}

async function providerError(response: Response): Promise<ProviderError> {
  const error = (await response.json().catch(() => ({}))) as { code?: number };
  const code = error.code ?? INTERNAL;
  const message = ERROR_MESSAGES[code] ?? `Request failed (${response.status}).`;

  return new ProviderError(code, message);
}

// sourceRoute builds a per-source URL bound to the source's current version.
function sourceRoute(source: Source, resource: string, fields?: Record<string, string>): string {
  const query = new URLSearchParams({ sourceVersion: source.version, ...fields });

  return `/v1/explorer/sources/${encodeURIComponent(source.id)}/${resource}?${query}`;
}
