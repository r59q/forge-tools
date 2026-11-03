/** A configured source incarnation. Live worktrees are not immutable snapshots. */
export interface Source {
  id: string;
  label: string;
  version: string;
  kind: string;
  capabilities: Capabilities;
  limits: {
    fileBytes: number;
    entries: number;
    results: number;
    searchBytes: number;
    depth: number;
  };
}

/** Optional provider operations; advertise only operations implemented for this source. */
export interface Capabilities {
  symbols: boolean;
  definitions: boolean;
  references: boolean;
  history: boolean;
}

/** One-based lines and UTF-16 columns, exclusive end column; all zeros mean whole file. */
export interface Range {
  startLine: number;
  startColumn: number;
  endLine: number;
  endColumn: number;
}

/** Source-bound file identity. Version identifies the bytes at path, not just HEAD. */
export interface Location {
  source: string;
  version: string;
  path: string;
  range: Range;
}

/** A bounded UTF-8 file observation. Repository text must be rendered as data. */
export interface FileContent {
  location: Location;
  text: string;
}

/** Partial-result information. Skipped includes unreadable and unsupported entries. */
export interface ResultState {
  truncated: boolean;
  canceled: boolean;
  skipped: number;
}

/** Directory child; unsupported entries cannot be opened through discovery. */
export interface Entry {
  path: string;
  kind: "file" | "directory" | "unsupported";
}

/** Live bounded listing; version describes this listing only. */
export interface Directory {
  source: string;
  version: string;
  entries: Entry[];
  state: ResultState;
}

/** Literal search or file lookup. Glob follows Go path.Match, not gitignore syntax. */
export interface Query {
  text: string;
  pathGlob?: string;
  files?: boolean;
  limit?: number;
}

/** A result always retains its source and content version. */
export interface Match {
  location: Location;
  preview: string;
}

/** Search results may be partial even when the request succeeds. */
export interface SearchResults {
  matches: Match[];
  state: ResultState;
}

/** Symbol navigation result, scoped to the same source/version as the request. */
export interface SymbolLocation {
  name: string;
  location: Location;
}

/** History metadata. Opening another revision requires an explicitly selected source. */
export interface HistoryEntry {
  version: string;
  label: string;
  timestamp?: string;
}

/** Embeddable provider contract. All operations honor cancellation and preserve source identity.
 * A source version must be checked before reading; expectedVersion checks file identity.
 * Providers are host-owned and may be shared. Disposing an explorer cancels its own calls only.
 */
export interface ContentProvider {
  sources(signal: AbortSignal): Promise<Source[]>;
  browse(source: Source, path: string, signal: AbortSignal): Promise<Directory>;
  read(
    source: Source,
    path: string,
    expectedVersion: string,
    signal: AbortSignal,
  ): Promise<FileContent>;
  search(
    source: Source,
    query: Query,
    signal: AbortSignal,
  ): Promise<SearchResults>;
  symbols?(location: Location, signal: AbortSignal): Promise<SymbolLocation[]>;
  definitions?(location: Location, signal: AbortSignal): Promise<Location[]>;
  references?(location: Location, signal: AbortSignal): Promise<Location[]>;
  history?(location: Location, signal: AbortSignal): Promise<HistoryEntry[]>;
}

/** Navigation can be intercepted synchronously by the embedding host. false cancels this instance's navigation. */
export interface ExplorerOptions {
  onNavigate?: (location: Readonly<Location>) => boolean | void;
  onLocation?: (location: Readonly<Location>) => void;
  onSelection?: (location: Readonly<Location>) => void;
}

/** Public component lifecycle and navigation commands. Promises settle after the visible operation. */
export interface ExplorerHandle {
  open(path: string | Location): Promise<void>;
  back(): Promise<void>;
  forward(): Promise<void>;
  refresh(): Promise<void>;
  current(): Location | undefined;
  dispose(): void;
}
