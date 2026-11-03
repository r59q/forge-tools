import type {
  ContentProvider,
  ExplorerOptions,
  FileContent,
  Location,
  Range,
  Source,
} from "../types.js";
import { History } from "./history.js";
import {
  copyLocation,
  validateLocation,
  validateRange,
  validateRead,
  wholeFile,
} from "./location.js";
import { Tabs } from "./tabs.js";

/** Navigation is one explorer's file state. Failed or stale reads never change history or tabs. */
export class Navigation {
  readonly tabs = new Tabs();
  readonly history = new History();

  #file?: FileContent;
  #location?: Location;
  #loading = false;
  #error = "";
  #requestedPath?: string;
  #request?: AbortController;
  #disposed = false;

  /** changed is called after every state change the view should render. */
  constructor(
    private readonly provider: ContentProvider,
    private readonly source: Source,
    private readonly options: ExplorerOptions,
    private readonly changed: () => void,
  ) {}

  get file(): FileContent | undefined {
    return this.#file;
  }

  get location(): Location | undefined {
    return this.#location;
  }

  get loading(): boolean {
    return this.#loading;
  }

  get error(): string {
    return this.#error;
  }

  get canGoBack(): boolean {
    return this.history.canGoBack && !this.#loading;
  }

  get canGoForward(): boolean {
    return this.history.canGoForward && !this.#loading;
  }

  get canReload(): boolean {
    return this.#requestedPath !== undefined;
  }

  /** open reads a path's current content, or a location at its expected version. */
  open(target: string | Location): Promise<void> {
    return this.#navigate(target, 0);
  }

  back(): Promise<void> {
    return this.#step(-1);
  }

  forward(): Promise<void> {
    return this.#step(1);
  }

  /** refresh reads the latest content of the last requested path. */
  async refresh(): Promise<void> {
    if (this.#requestedPath) await this.open(this.#requestedPath);
  }

  /** select records range as the current location's selection. */
  select(range: Range): void {
    if (!this.#location || !this.#file || this.#disposed) return;

    validateRange(this.#file.text, range);
    this.#location = { ...this.#location, range: { ...range } };
    this.history.replaceCurrent(this.#location);
    this.tabs.set(this.#location);
    this.options.onSelection?.(copyLocation(this.#location));
  }

  /** closeTab closes path; closing the current file opens the last remaining tab. */
  async closeTab(path: string): Promise<void> {
    this.tabs.remove(path);

    if (this.#location?.path !== path) {
      this.changed();
      return;
    }

    const next = this.tabs.last();
    if (next) await this.open(next);
    else this.#clear();
  }

  dispose(): void {
    this.#disposed = true;
    this.#request?.abort();
  }

  async #step(step: number): Promise<void> {
    const target = this.history.peek(step);
    if (target) await this.#navigate(target, step);
  }

  // historyStep 0 adds a history entry; -1 and 1 move back or forward.
  async #navigate(target: string | Location, historyStep: number): Promise<void> {
    if (this.#disposed) return;

    const location = this.#resolve(target);
    if (!this.#accept(location)) return;

    const request = this.#begin(location.path);
    try {
      const file = await this.provider.read(
        this.source,
        location.path,
        location.version,
        request.signal,
      );
      if (this.#isCurrent(request)) this.#show(file, location, historyStep);
    } catch (error) {
      if (this.#isCurrent(request)) this.#error = (error as Error).message;
    } finally {
      if (this.#isCurrent(request)) this.#finish();
    }
  }

  #resolve(target: string | Location): Location {
    if (typeof target !== "string") return copyLocation(target);

    return { source: this.source.id, path: target, version: "", range: wholeFile() };
  }

  // accept validates location and lets the host intercept it.
  #accept(location: Location): boolean {
    try {
      validateLocation(this.source, location);
    } catch (error) {
      this.#error = (error as Error).message;
      this.changed();
      return false;
    }

    return this.options.onNavigate?.(copyLocation(location)) !== false;
  }

  // begin cancels any read in flight and starts a new one.
  #begin(path: string): AbortController {
    this.#request?.abort();
    this.#request = new AbortController();
    this.#requestedPath = path;
    this.#loading = true;
    this.#error = "";
    this.changed();

    return this.#request;
  }

  #isCurrent(request: AbortController): boolean {
    return request === this.#request && !request.signal.aborted;
  }

  #show(file: FileContent, requested: Location, historyStep: number): void {
    validateRead(this.source, file, requested);

    this.#file = { text: file.text, location: copyLocation(file.location) };
    this.#location = { ...file.location, range: { ...requested.range } };

    if (historyStep) this.history.go(historyStep);
    else this.history.push(this.#location);

    this.tabs.set(this.#location);
    this.options.onLocation?.(copyLocation(this.#location));
  }

  #finish(): void {
    this.#loading = false;
    this.changed();
  }

  #clear(): void {
    this.#request?.abort();
    this.#file = undefined;
    this.#location = undefined;
    this.#requestedPath = undefined;
    this.#error = "";
    this.#finish();
  }
}
