import { createExplorer, httpProvider } from "@r59q/explorer";

const status = document.querySelector("#status");
const lifetime = new AbortController();

// Same-origin API path. The host server adds the service token.
const provider = httpProvider("/explorer-api");

async function main() {
  const [source] = await provider.sources(lifetime.signal);
  if (!source) throw new Error("The explorer service has no sources.");

  const explorer = createExplorer(document.querySelector("#files"), provider, source, {
    onLocation: showFile,
    onSelection: showSelection,
  });
  status.textContent = `Browsing ${source.label}`;

  // The URL hash names the open file, so links and reloads open it.
  const openFromHash = () => {
    const path = decodeURIComponent(location.hash.slice(1)) || "README.md";
    explorer.open(path).catch(showError);
  };

  openFromHash();
  addEventListener("hashchange", openFromHash, { signal: lifetime.signal });

  addEventListener("pagehide", () => {
    lifetime.abort();
    explorer.dispose();
  }, { once: true });
}

function showFile(file) {
  history.replaceState(null, "", `#${encodeURI(file.path)}`);
  status.textContent = `Viewing ${file.path}`;
}

function showSelection(file) {
  const { startLine, endLine } = file.range;
  status.textContent = `Selected ${file.path}, lines ${startLine}–${endLine}`;
}

function showError(error) {
  status.textContent = error.message;
}

main().catch(showError);
