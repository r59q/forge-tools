import { AppHeader, AttributionFooter, ConnectionForm, ConnectionStatus } from "./components.js";
import { h } from "../src/components/dom.js";
import { createExplorer, httpProvider } from "../src/index";
import type { ExplorerHandle } from "../src/index";

const connection = new ConnectionForm();
const form = connection.node;
const token = connection.token;
const sourceSelect = connection.source.node;
const status = ConnectionStatus();
const host = h("div", { id: "explorer" });
document.body.append(AppHeader(), form, status, host, AttributionFooter());
const lifetime = new AbortController();
const provider = httpProvider("", () => token.value);
let explorer: ExplorerHandle | undefined;
let request: AbortController | undefined;

form.addEventListener(
  "submit",
  (event) => {
    event.preventDefault();
    void connect();
  },
  { signal: lifetime.signal },
);

async function connect(): Promise<void> {
  request?.abort();
  request = new AbortController();
  const signal = AbortSignal.any([request.signal, lifetime.signal]);
  status.textContent = "Loading sources…";
  try {
    const sources = await provider.sources(signal);
    if (signal.aborted) return;
    const selected =
      sources.find((source) => source.id === sourceSelect.value) ?? sources[0];
    connection.source.update(sources);
    explorer?.dispose();
    if (!selected) {
      status.textContent = "No sources configured.";
      return;
    }
    sourceSelect.value = selected.id;
    explorer = createExplorer(host, provider, selected);
    status.textContent = "Connected. Token stays in this page’s memory.";
  } catch (error) {
    if (!signal.aborted) status.textContent = (error as Error).message;
  }
}
sourceSelect.addEventListener(
  "change",
  () => {
    void connect();
  },
  { signal: lifetime.signal },
);
window.addEventListener(
  "pagehide",
  () => {
    lifetime.abort();
    request?.abort();
    explorer?.dispose();
    token.value = "";
  },
  { once: true },
);
