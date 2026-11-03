import { mkdir, readFile, writeFile, lstat } from "node:fs/promises";
import { join, resolve } from "node:path";
import {
  blocks,
  digest,
  json,
  validateID,
  validatePayload,
} from "./nuda-source.mjs";

async function read(file) {
  try {
    return await readFile(file, "utf8");
  } catch (error) {
    if (error.code === "ENOENT") return null;
    throw error;
  }
}

// Refuse symlinks in every managed output directory, including existing files.
async function safe(root, ...parts) {
  let path = resolve(root);
  for (const part of ["", ...parts]) {
    path = join(path, part);
    try {
      if ((await lstat(path)).isSymbolicLink())
        throw new Error("Symlink output refused");
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
    }
  }
  return path;
}

async function manifest(root) {
  return JSON.parse(
    (await read(await safe(root, "nudaui", "manifest.json"))) ??
      '{"components":{}}',
  );
}

async function current(root, id, names) {
  const out = {};
  for (const name of names) {
    if (!/^code\.(html|css|js)$/.test(name))
      throw new Error("Invalid managed filename");
    const text = await read(
      await safe(root, "src", "components", "nuda", id, name),
    );
    out[name] = text === null ? null : digest(text);
  }
  return out;
}

export async function stage(root, id, payload, provenance) {
  validatePayload(id, payload);
  const files = blocks(payload);
  const saved = (await manifest(root)).components[id];
  const names = [
    ...new Set([...Object.keys(files), ...Object.keys(saved?.files ?? {})]),
  ].sort();
  const local = await current(root, id, names);
  const incoming = Object.fromEntries(
    Object.entries(files).map(([name, value]) => [name, digest(value)]),
  );
  const baseline = saved?.files ?? {};
  const changed = json(baseline) !== json(incoming);
  const conflicts = names.filter(
    (name) => local[name] !== (saved?.files?.[name] ?? null),
  );
  const plan = {
    id,
    baseline,
    local,
    incoming,
    changed,
    conflicts,
    payloadChecksum: digest(json(payload)),
    provenance,
  };
  const directory = await safe(root, "nudaui", ".staging", id);
  await mkdir(directory, { recursive: true });
  await writeFile(
    await safe(root, "nudaui", ".staging", id, "payload.json"),
    json(payload),
  );
  await writeFile(
    await safe(root, "nudaui", ".staging", id, "plan.json"),
    json(plan),
  );
  for (const [name, value] of Object.entries(files)) {
    await writeFile(await safe(root, "nudaui", ".staging", id, name), value);
  }
  return plan;
}

export async function apply(root, id, keepWorking = false) {
  validateID(id);
  const { plan, payload } = await readStaged(root, id);
  const saved = await manifest(root);
  const previous = saved.components[id];
  const local = await checkUnchanged(root, id, plan, previous);
  if (!plan.changed && previous) return { status: "unchanged" };

  const files = blocks(payload);
  checkApplicable(plan, previous, files, keepWorking);
  await writeComponent(root, id, files, keepWorking, local);

  const raw = await safe(root, "nudaui", "baseline", id, "payload.json");
  await writeFile(raw, json(payload));
  saved.components[id] = {
    ...plan.provenance,
    payloadChecksum: plan.payloadChecksum,
    files: plan.incoming,
    working: await current(root, id, Object.keys(files).sort()),
  };
  await writeFile(await safe(root, "nudaui", "manifest.json"), json(saved));

  const status = keepWorking ? "baseline updated; working code preserved" : "applied";
  return { status };
}

async function readStaged(root, id) {
  const directory = await safe(root, "nudaui", ".staging", id);
  const readJSON = async (name) =>
    JSON.parse(await readFile(await safe(directory, name), "utf8"));

  const plan = await readJSON("plan.json");
  const payload = validatePayload(id, await readJSON("payload.json"));
  if (digest(json(payload)) !== plan.payloadChecksum) {
    throw new Error("Staged payload changed; stage again");
  }
  return { plan, payload };
}

// checkUnchanged rejects a plan whose baseline or working files changed since
// staging, and returns the working file digests.
async function checkUnchanged(root, id, plan, previous) {
  if (json(previous?.files ?? {}) !== json(plan.baseline)) {
    throw new Error("Baseline changed; stage again");
  }

  const local = await current(root, id, Object.keys(plan.local));
  if (json(local) !== json(plan.local)) {
    throw new Error("Working files changed since inspection; stage again");
  }
  return local;
}

function checkApplicable(plan, previous, files, keepWorking) {
  if (plan.conflicts.length && !keepWorking) {
    throw new Error(`Local changes conflict: ${plan.conflicts.join(", ")}`);
  }

  const removed = Object.keys(previous?.files ?? {}).some(
    (name) => !Object.hasOwn(files, name),
  );
  if (removed) {
    throw new Error("Upstream removed a block; reconcile it manually before updating");
  }
}

// writeComponent writes the baseline. It writes working files too, except
// existing ones when keepWorking is set.
async function writeComponent(root, id, files, keepWorking, local) {
  const baseline = await safe(root, "nudaui", "baseline", id);
  const working = await safe(root, "src", "components", "nuda", id);
  await mkdir(baseline, { recursive: true });
  await mkdir(working, { recursive: true });

  for (const [name, value] of Object.entries(files)) {
    await writeFile(await safe(baseline, name), value);
    const keep = keepWorking && local[name] !== null;
    if (!keep) await writeFile(await safe(working, name), value);
  }
}
