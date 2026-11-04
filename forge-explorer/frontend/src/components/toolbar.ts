import { Button } from "./controls.js";
import type { Navigation } from "../model/navigation.js";
import { Toolbar } from "./layout.js";

/** ExplorerToolbar holds the history and file buttons. */
export class ExplorerToolbar {
  readonly back: HTMLButtonElement;
  readonly node: HTMLElement;
  readonly #forward: HTMLButtonElement;
  readonly #reload: HTMLButtonElement;
  readonly #close: HTMLButtonElement;

  constructor(
    private readonly model: Navigation,
    closeFile: (path: string) => unknown,
  ) {
    this.back = Button("Back", () => model.back());
    this.#forward = Button("Forward", () => model.forward());
    this.#reload = Button("Reload file", () => model.refresh());
    this.#close = Button("Close file", () => {
      if (model.location) closeFile(model.location.path);
    });

    this.node = Toolbar(
      this.back,
      this.#forward,
      this.#reload,
      this.#close,
    );
  }

  /** update enables only actions supported by the current navigation state. */
  update(): void {
    this.back.disabled = !this.model.canGoBack;
    this.#forward.disabled = !this.model.canGoForward;
    this.#reload.disabled = !this.model.canReload;
    this.#close.disabled = !this.model.location;
  }
}
