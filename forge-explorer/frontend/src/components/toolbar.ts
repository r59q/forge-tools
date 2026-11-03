import type { Navigation } from "../model/navigation.js";
import { button, h } from "./dom.js";

/** Toolbar holds the history and file buttons. */
export class Toolbar {
  readonly back: HTMLButtonElement;
  readonly node: HTMLElement;
  readonly #forward: HTMLButtonElement;
  readonly #reload: HTMLButtonElement;
  readonly #close: HTMLButtonElement;

  constructor(
    private readonly model: Navigation,
    closeFile: (path: string) => unknown,
  ) {
    this.back = button("Back", () => model.back());
    this.#forward = button("Forward", () => model.forward());
    this.#reload = button("Reload file", () => model.refresh());
    this.#close = button("Close file", () => {
      if (model.location) closeFile(model.location.path);
    });

    this.node = h(
      "div",
      { class: "toolbar" },
      this.back,
      this.#forward,
      this.#reload,
      this.#close,
    );
  }

  update(): void {
    this.back.disabled = !this.model.canGoBack;
    this.#forward.disabled = !this.model.canGoForward;
    this.#reload.disabled = !this.model.canReload;
    this.#close.disabled = !this.model.location;
  }
}
