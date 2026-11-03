import type { Navigation } from "../model/navigation.js";
import type { Range } from "../types.js";
import { StatusMessage } from "./messages.js";

const NO_FILE = "Choose a file to begin.";

/** FileMetadata announces the current path/version, loading and stale/read errors. */
export class FileMetadata {
  readonly node = StatusMessage(NO_FILE, { class: "status" });

  /** update reflects navigation state without replacing the live region. */
  update(model: Navigation): void {
    this.node.textContent = statusText(model);
    this.node.classList.toggle("error", model.error !== "");
  }
}

/** SelectionSummary announces a selected line range and clears on navigation. */
export class SelectionSummary {
  readonly node = StatusMessage("", { class: "selection" });

  /** update announces range, or clears a previous selection. */
  update(range?: Range): void {
    this.node.textContent = range ? `Selected lines ${range.startLine}–${range.endLine}` : "";
  }
}

function statusText(model: Navigation): string {
  if (model.error) return model.error;
  if (model.loading) return "Loading file…";
  if (!model.location) return NO_FILE;

  const { path, version } = model.location;
  return `${path} · ${version.slice(0, 12)}`;
}
