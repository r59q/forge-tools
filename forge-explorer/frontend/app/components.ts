import { Button, Field, Input, Link, Select } from "../src/components/controls.js";
import { h } from "../src/components/dom.js";
import { Separator } from "../src/components/layout.js";
import { StatusMessage, Text } from "../src/components/messages.js";
import type { Source } from "../src/types.js";

/** AppHeader introduces the standalone worktree explorer. */
export function AppHeader(): HTMLElement {
  return h("header", {}, h("h1", {}, "Forge Explorer"),
    h("p", {}, Text("Browse and search a configured worktree.", "muted")));
}

/** SourceSelector presents source labels and preserves native selection. */
export class SourceSelector {
  readonly node = Select([], { id: "source", "aria-label": "Source" });

  /** update replaces the available source options. */
  update(sources: Source[]): void {
    const options = Select(sources.map((source) => ({ value: source.id, label: source.label })));
    this.node.replaceChildren(...options.children);
  }
}

/** ConnectionForm exposes native token validation and source selection. */
export class ConnectionForm {
  readonly token = Input("password", { id: "token", autocomplete: "off", required: true });
  readonly source = new SourceSelector();
  readonly node = h("form", { id: "connect" },
    Field("Service token", this.token),
    Button("Connect", () => {}, { type: "submit" }),
    Field("Source", this.source.node));
}

/** ConnectionStatus announces authentication, loading, empty, error and ready states. */
export function ConnectionStatus(): HTMLParagraphElement {
  return StatusMessage("Enter the service token to connect.", { id: "connection-status" });
}

/** AttributionFooter retains upstream credit and the shipped license link. */
export function AttributionFooter(): HTMLElement {
  return h("footer", {}, "Interface components adapted from ",
    Link("NudaUI", "https://nudaui.dev"), Separator(" · "), Link("MIT license", "/NUDAUI-LICENSE.txt"));
}
