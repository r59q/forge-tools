import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";
import { digest, fromRegistry, validateID } from "./nuda-source.mjs";
import { stage, apply } from "./nuda-store.mjs";

const root = fileURLToPath(new URL("../", import.meta.url));
const [command, id, ...args] = process.argv.slice(2);
validateID(id ?? "");
const option = (name) => args[args.indexOf(name) + 1];

async function fetchText(url) {
  const response = await fetch(url, {
    signal: AbortSignal.timeout(30_000),
    redirect: "error",
  });
  if (!response.ok) throw new Error(`Upstream HTTP ${response.status}`);
  const text = await response.text();
  if (text.length > 2_000_000) throw new Error("Oversized upstream response");
  return { text, etag: response.headers.get("etag") };
}

if (command === "apply") {
  console.log(await apply(root, id, args.includes("--keep-working")));
} else if (command === "stage") {
  const revision = args.includes("--revision") ? option("--revision") : null;
  let sourceURL = `https://nudaui.dev/api/components/${id}.json`;
  let payload;
  let raw;
  let etag = null;
  if (args.includes("--registry")) {
    if (!/^[a-f0-9]{40}$/.test(revision ?? ""))
      throw new Error("Pinned registry revision required");
    const file = option("--registry");
    if (!/^[a-z-]+\.tsx$/.test(file))
      throw new Error("Invalid registry filename");
    sourceURL = `https://raw.githubusercontent.com/sgomez-dev/NudaUI/${revision}/src/components/showcase/registry/${file}`;
    ({ text: raw, etag } = await fetchText(sourceURL));
    payload = fromRegistry(id, raw);
  } else if (args.includes("--fixture")) {
    raw = await readFile(resolve(option("--fixture")), "utf8");
    sourceURL = "local-fixture";
    payload = JSON.parse(raw);
  } else {
    ({ text: raw, etag } = await fetchText(sourceURL));
    payload = JSON.parse(raw);
  }
  const plan = await stage(root, id, payload, {
    id,
    sourceURL,
    apiURL: `https://nudaui.dev/api/components/${id}.json`,
    revision,
    retrievedAt: new Date().toISOString(),
    etag,
    sourceChecksum: digest(raw),
    license: "MIT",
    licenseURL: "https://github.com/sgomez-dev/NudaUI/blob/main/LICENSE",
  });
  console.log(JSON.stringify(plan, null, 2));
  console.log(
    `Inspect nudaui/.staging/${id}/ and compare baseline/ with src/components/nuda/${id}/ before apply.`,
  );
} else {
  throw new Error(
    "Usage: npm run nuda -- stage|apply ID [--registry FILE --revision SHA | --fixture FILE | --keep-working]",
  );
}
