// Shared parts of the standalone app build, used by build.mjs and dev.mjs.
import { readFile } from "node:fs/promises";

/** common holds the esbuild options every bundle uses. */
export const common = {
  bundle: true,
  format: "esm",
  target: "es2023",
  loader: { ".css": "text" },
  legalComments: "inline",
};

/** appEntry is the standalone app's script entry point. */
export const appEntry = "app/main.ts";

/** appShell lists the page and license files copied next to app.js. */
export const appShell = {
  page: "app/index.html",
  license: "nudaui/LICENSE",
};

/** appStyleFiles are concatenated, in order, into app.css. */
export const appStyleFiles = [
  "src/components/nuda/shine-sweep/code.css",
  "src/components/primitives.css",
  "app/app.css",
];

/** appStyles returns the contents of app.css. */
export async function appStyles() {
  const parts = await Promise.all(
    appStyleFiles.map((file) => readFile(file, "utf8")),
  );

  return parts.join("\n");
}
