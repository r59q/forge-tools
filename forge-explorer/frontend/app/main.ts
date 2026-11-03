import { createExplorer, httpProvider } from "../src/index";
import type { ExplorerHandle } from "../src/index";

const form = document.querySelector<HTMLFormElement>("#connect")!;
const token = document.querySelector<HTMLInputElement>("#token")!;
const sourceSelect = document.querySelector<HTMLSelectElement>("#source")!;
const status = document.querySelector<HTMLElement>("#connection-status")!;
const host = document.querySelector<HTMLElement>("#explorer")!;
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
    sourceSelect.replaceChildren();
    for (const source of sources) {
      const option = document.createElement("option");
      option.value = source.id;
      option.textContent = source.label;
      sourceSelect.append(option);
    }
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
