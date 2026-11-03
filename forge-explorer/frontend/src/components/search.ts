import type { ContentProvider, Location, Source } from "../types.js";
import { validateLocation } from "../navigation.js";
import { button, element, resultSummary } from "./dom.js";

/** Search controls own their request, focus and cancellation independently of file navigation. */
export function createSearch(
  provider: ContentProvider,
  source: Source,
  navigate: (location: Location) => void,
  signal: AbortSignal,
): HTMLElement {
  const node = element("section", "", "search");
  node.setAttribute("aria-label", "Search files");
  const form = element("form");
  const query = element("input");
  query.type = "search";
  query.required = true;
  query.maxLength = 256;
  query.placeholder = "Find text or files";
  query.setAttribute("aria-label", "Search query");
  const filter = element("input");
  filter.placeholder = "Path glob, e.g. src/*.go";
  filter.maxLength = 256;
  filter.setAttribute("aria-label", "Path filter");
  const mode = element("select");
  mode.setAttribute("aria-label", "Search mode");
  for (const [value, label] of [
    ["text", "Text"],
    ["files", "Files"],
  ]) {
    const option = element("option", label);
    option.value = value ?? "";
    mode.append(option);
  }
  const submit = button("Search", () => {});
  submit.type = "submit";
  const status = element("p", "Enter a query to search this source.");
  status.setAttribute("role", "status");
  const results = element("ul", "", "search-results");
  let request: AbortController | undefined;
  let sequence = 0;
  const cancel = button("Cancel search", () => {
    request?.abort();
    sequence++;
    status.textContent = "Search canceled; results may be incomplete.";
    node.setAttribute("aria-busy", "false");
    cancel.disabled = true;
  });
  cancel.disabled = true;
  form.append(query, mode, filter, submit, cancel);
  node.append(form, status, results);
  form.addEventListener(
    "submit",
    (event) => {
      event.preventDefault();
      void search();
    },
    { signal },
  );

  async function search(): Promise<void> {
    if (!query.value.trim() || signal.aborted) return;
    request?.abort();
    request = new AbortController();
    const active = ++sequence;
    const requestSignal = AbortSignal.any([signal, request.signal]);
    status.textContent = "Searching…";
    node.setAttribute("aria-busy", "true");
    cancel.disabled = false;
    results.replaceChildren();
    try {
      const response = await provider.search(
        source,
        {
          text: query.value,
          pathGlob: filter.value,
          files: mode.value === "files",
        },
        requestSignal,
      );
      if (requestSignal.aborted || active !== sequence) return;
      for (const result of response.matches) {
        validateLocation(source, result.location);
        if (!result.location.version)
          throw new Error("Search result has no content version.");
      }
      for (const result of response.matches) {
        const item = element("li");
        const title = `${result.location.path}${result.location.range.startLine ? `:${result.location.range.startLine}` : ""}`;
        const open = button(title, () => navigate(result.location));
        item.append(open, element("code", result.preview));
        results.append(item);
      }
      status.textContent = resultSummary(
        response.matches.length,
        response.state,
      );
    } catch (error) {
      if (!requestSignal.aborted) status.textContent = (error as Error).message;
    } finally {
      if (!requestSignal.aborted && active === sequence) {
        node.setAttribute("aria-busy", "false");
        cancel.disabled = true;
      }
    }
  }
  results.addEventListener(
    "keydown",
    (event) => {
      if (!["ArrowDown", "ArrowUp"].includes(event.key)) return;
      const buttons = [...results.querySelectorAll("button")];
      const index = buttons.indexOf(event.target as HTMLButtonElement);
      const next = buttons[index + (event.key === "ArrowDown" ? 1 : -1)];
      if (next) {
        event.preventDefault();
        next.focus();
      }
    },
    { signal },
  );
  return node;
}
