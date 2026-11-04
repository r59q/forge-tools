import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

// Exercise the actual tarball in a host with no dependencies or registry access.
const project = fileURLToPath(new URL("../", import.meta.url));
const consumer = mkdtempSync(join(tmpdir(), "explorer-package-"));
const manifest = JSON.parse(readFileSync(join(project, "package.json"), "utf8"));
const options = { cwd: consumer, encoding: "utf8" };
try {
  const [packed] = JSON.parse(execFileSync("npm", [
    "pack", "--json", "--ignore-scripts", "--pack-destination", consumer,
  ], { ...options, cwd: project }));
  for (const required of ["dist/types/index.d.ts", "nudaui/LICENSE", "LICENSE"]) {
    assert.ok(packed.files.some((file) => file.path === required), required);
  }
  // The standalone app and dev build belong to the service, not to embedders.
  const appFiles = packed.files.filter((file) => /^dist\/(app|dev)\//.test(file.path));
  assert.deepEqual(appFiles, [], "dist/app and dist/dev are not packed");
  writeFileSync(join(consumer, "package.json"), '{"private":true,"type":"module"}');
  execFileSync("npm", [
    "install", "--offline", "--ignore-scripts", "--no-audit", "--no-fund",
    join(consumer, packed.filename),
  ], options);
  writeFileSync(join(consumer, "consumer.ts"), `
import { createExplorer, httpProvider, type ContentProvider } from "${manifest.name}";
const provider: ContentProvider = httpProvider("/api", () => "");
const [source] = await provider.sources(new AbortController().signal);
createExplorer(document.createElement("div"), provider, source);
`);
  execFileSync(process.execPath, [
    join(project, "node_modules/typescript/bin/tsc"), "--noEmit",
    "--module", "nodenext", "--target", "es2023", "consumer.ts",
  ], options);
  execFileSync(process.execPath, ["--input-type=module", "-e", `
import { createExplorer, httpProvider } from "${manifest.name}";
if (typeof createExplorer !== "function" || typeof httpProvider !== "function") {
  throw new Error("Missing public exports");
}
import.meta.resolve("${manifest.name}/styles.css");
`], options);
  console.log("Package installs offline; public exports and Node ESM types resolve.");
} finally {
  rmSync(consumer, { recursive: true, force: true });
}
