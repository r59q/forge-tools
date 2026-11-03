import type {
  ContentProvider,
  ExplorerOptions,
  FileContent,
  Location,
  Range,
  Source,
} from "./types.js";

/** Empty range denotes a whole file. */
export function wholeFile(): Range {
  return { startLine: 0, startColumn: 0, endLine: 0, endColumn: 0 };
}

/** Clone mutable location fields before crossing the host/component boundary. */
export function copyLocation(location: Location): Location {
  return { ...location, range: { ...location.range } };
}

/** Validate source identity and a confined slash path for custom providers too. */
export function validateLocation(source: Source, location: Location): void {
  if (
    location.source !== source.id ||
    !location.path ||
    location.path.startsWith("/") ||
    location.path
      .split("/")
      .some(
        (part) => !part || part === "." || part === ".." || part === ".git",
      ) ||
    /[\\\0]/.test(location.path)
  )
    throw new Error("Navigation must remain within its originating source.");
}

/** Verify a location's UTF-16 range against the bytes the provider returned. */
export function validateRange(text: string, range: Range): void {
  const values = Object.values(range);
  if (values.every((value) => value === 0)) return;
  const lines = text.split("\n");
  if (
    values.some((value) => !Number.isSafeInteger(value) || value < 1) ||
    range.endLine < range.startLine ||
    range.endLine > lines.length ||
    range.startColumn > (lines[range.startLine - 1]?.length ?? -1) + 1 ||
    range.endColumn > (lines[range.endLine - 1]?.length ?? -1) + 1 ||
    (range.startLine === range.endLine && range.endColumn < range.startColumn)
  ) {
    throw new Error("The selected range is no longer valid.");
  }
}

/** Instance-local navigation state. Failed/stale reads never advance history or tabs. */
export class Navigation {
  file?: FileContent;
  location?: Location;
  tabs: Location[] = [];
  history: Location[] = [];
  index = -1;
  loading = false;
  error = "";
  requestedPath?: string;
  private request?: AbortController;
  private disposed = false;
  private revision = 0;

  constructor(
    readonly provider: ContentProvider,
    readonly source: Source,
    private options: ExplorerOptions,
    private changed: () => void,
  ) {}

  async open(target: string | Location, historyIndex?: number): Promise<void> {
    if (this.disposed) return;
    const location =
      typeof target === "string"
        ? {
            source: this.source.id,
            path: target,
            version: "",
            range: wholeFile(),
          }
        : copyLocation(target);
    try {
      validateLocation(this.source, location);
    } catch (error) {
      this.error = String((error as Error).message);
      this.changed();
      return;
    }
    if (this.options.onNavigate?.(copyLocation(location)) === false) return;
    this.requestedPath = location.path;
    this.request?.abort();
    const request = new AbortController();
    this.request = request;
    const revision = ++this.revision;
    this.loading = true;
    this.error = "";
    this.changed();
    try {
      const file = await this.provider.read(
        this.source,
        location.path,
        location.version,
        request.signal,
      );
      if (request.signal.aborted || revision !== this.revision || this.disposed)
        return;
      validateLocation(this.source, file.location);
      if (
        file.location.path !== location.path ||
        !file.location.version ||
        (location.version && file.location.version !== location.version)
      )
        throw new Error("Content changed. Reload the file.");
      validateRange(file.text, location.range);
      this.file = { text: file.text, location: copyLocation(file.location) };
      this.location = { ...file.location, range: { ...location.range } };
      this.record(this.location, historyIndex);
      this.options.onLocation?.(copyLocation(this.location));
    } catch (error) {
      if (!request.signal.aborted && !this.disposed)
        this.error = (error as Error).message;
    } finally {
      if (revision === this.revision && !this.disposed) {
        this.loading = false;
        this.changed();
      }
    }
  }

  private record(location: Location, historyIndex?: number): void {
    if (historyIndex !== undefined) this.index = historyIndex;
    else {
      this.history = this.history.slice(0, this.index + 1);
      this.history.push(copyLocation(location));
      if (this.history.length > 100) this.history.shift();
      this.index = this.history.length - 1;
    }
    const tab = this.tabs.findIndex((value) => value.path === location.path);
    if (tab >= 0) this.tabs[tab] = copyLocation(location);
    else this.tabs.push(copyLocation(location));
    if (this.tabs.length > 20) this.tabs.shift();
  }

  async back(): Promise<void> {
    const target = this.history[this.index - 1];
    if (target) await this.open(target, this.index - 1);
  }

  async forward(): Promise<void> {
    const target = this.history[this.index + 1];
    if (target) await this.open(target, this.index + 1);
  }

  async refresh(): Promise<void> {
    if (this.requestedPath) await this.open(this.requestedPath);
  }

  select(range: Range): void {
    if (!this.location || !this.file || this.disposed) return;
    validateRange(this.file.text, range);
    this.location = { ...this.location, range: { ...range } };
    if (this.index >= 0) this.history[this.index] = copyLocation(this.location);
    const tab = this.tabs.findIndex(
      (value) => value.path === this.location?.path,
    );
    if (tab >= 0) this.tabs[tab] = copyLocation(this.location);
    this.options.onSelection?.(copyLocation(this.location));
  }

  async closeTab(path: string): Promise<void> {
    this.tabs = this.tabs.filter((tab) => tab.path !== path);
    if (this.location?.path === path) {
      const target = this.tabs.at(-1);
      if (target) await this.open(target);
      else {
        this.request?.abort();
        this.file = undefined;
        this.location = undefined;
        this.requestedPath = undefined;
        this.loading = false;
        this.error = "";
        this.changed();
      }
    } else this.changed();
  }

  dispose(): void {
    this.disposed = true;
    this.request?.abort();
    this.revision++;
  }
}
