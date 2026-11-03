import { createExplorer } from "../src/index";
import type {
  ContentProvider,
  ExplorerHandle,
  Location,
  Source,
} from "../src/index";

/** The diff host owns counterpart policy. An ambiguous combined-view action defaults to new. */
export function embedSides(
  oldHost: HTMLElement,
  newHost: HTMLElement,
  provider: ContentProvider,
  oldSource: Source,
  newSource: Source,
): {
  old: ExplorerHandle;
  new: ExplorerHandle;
  route: (location: Location, side?: "old" | "new") => Promise<void>;
  dispose: () => void;
} {
  const old = createExplorer(oldHost, provider, oldSource);
  const next = createExplorer(newHost, provider, newSource);
  return {
    old,
    new: next,
    async route(location, side = "new") {
      const target = side === "old" ? old : next;
      const source = side === "old" ? oldSource : newSource;
      // A counterpart must be read in its own source. Never reuse the other side's hash/range.
      await target.open(
        location.source === source.id ? location : location.path,
      );
    },
    dispose() {
      old.dispose();
      next.dispose();
    },
  };
}
