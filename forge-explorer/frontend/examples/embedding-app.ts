import { httpProvider } from "../src/index";
import { embedSides } from "./embedding";

const token = document.querySelector<HTMLInputElement>("#token")!;
const status = document.querySelector<HTMLElement>("#status")!;
const lifetime = new AbortController();
let sides: ReturnType<typeof embedSides> | undefined;
const provider = httpProvider("", () => token.value);
document.querySelector("form")!.addEventListener(
  "submit",
  (event) => {
    event.preventDefault();
    void connect();
  },
  { signal: lifetime.signal },
);
async function connect(): Promise<void> {
  try {
    const sources = await provider.sources(lifetime.signal);
    const old = sources.find((source) => source.id === "old");
    const next = sources.find((source) => source.id === "new");
    if (!old || !next)
      throw new Error("Configure roots named old and new for this example.");
    sides?.dispose();
    sides = embedSides(
      document.querySelector("#old")!,
      document.querySelector("#new")!,
      provider,
      old,
      next,
    );
    status.textContent =
      "Two independent sources. The host routes ambiguous navigation to new.";
  } catch (error) {
    status.textContent = (error as Error).message;
  }
}
document.querySelector("#counterpart")!.addEventListener(
  "click",
  () => {
    const location = sides?.old.current();
    if (location) void sides?.route(location, "new");
  },
  { signal: lifetime.signal },
);
document.querySelector("#combined")!.addEventListener(
  "click",
  () => {
    const location = sides?.old.current();
    if (location) void sides?.route(location);
  },
  { signal: lifetime.signal },
);
document
  .querySelector("#dispose-old")!
  .addEventListener("click", () => sides?.old.dispose(), {
    signal: lifetime.signal,
  });
window.addEventListener(
  "pagehide",
  () => {
    lifetime.abort();
    sides?.dispose();
    token.value = "";
  },
  { once: true },
);
