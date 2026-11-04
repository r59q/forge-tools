import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

// Publish only new versions; authentication and network errors must fail CI.
export function publishPackage(manifest, run = execFileSync) {
  const registry = "https://npm.pkg.github.com";
  if (
    manifest.name !== "@r59q/explorer" ||
    manifest.publishConfig?.registry !== registry
  ) {
    throw new Error("Unexpected package name or publishing registry");
  }
  const spec = `${manifest.name}@${manifest.version}`;
  let existing;
  try {
    existing = run(
      "npm",
      ["view", spec, "version", "--json", "--registry", registry],
      { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] },
    );
  } catch (error) {
    let code;
    try {
      code = JSON.parse(String(error.stdout)).error?.code;
    } catch {
      // Preserve failures without structured npm output.
    }
    if (code !== "E404") throw error;
    run("npm", ["publish", "--ignore-scripts", "--registry", registry], {
      stdio: "inherit",
    });
    return "published";
  }
  if (JSON.parse(existing) !== manifest.version) {
    throw new Error("Registry returned an unexpected version");
  }
  console.log(
    `${spec} already exists; bump package.json and package-lock.json to publish changes.`,
  );
  return "exists";
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  publishPackage(JSON.parse(readFileSync("package.json", "utf8")));
}
