import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readFile, writeFile, mkdir, symlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { stage, apply } from "../tools/nuda-store.mjs";
import { fromRegistry, validatePayload } from "../tools/nuda-source.mjs";

const payload = (css = ".sample {}") => ({
  schemaVersion: "1.0",
  id: "sample",
  license: "MIT",
  code: [
    { language: "html", code: "<button>Sample</button>" },
    { language: "css", code: css },
  ],
});
const metadata = {
  sourceURL: "fixture",
  retrievedAt: "2026-01-01T00:00:00Z",
  revision: null,
};
const fixture = () => mkdtemp(join(tmpdir(), "explorer-import-"));

test("malformed IDs and payloads never write outside managed paths", async () => {
  const root = await fixture();
  for (const id of ["../escape", "/absolute", "", "bad/name"])
    await assert.rejects(stage(root, id, payload(), metadata));
  for (const value of [
    null,
    {},
    { ...payload(), id: "wrong" },
    { ...payload(), license: "unknown" },
    { ...payload(), code: [{ language: "../../escape", code: "oops" }] },
    { ...payload(), code: [...payload().code, payload().code[0]] },
    { ...payload(), code: [{ language: "html", code: 123 }] },
  ]) {
    assert.throws(() => validatePayload("sample", value));
  }
});

test("stage is non-destructive, repeat imports are deterministic, modified local code is preserved", async () => {
  const root = await fixture();
  const staged = await stage(root, "sample", payload(), metadata);
  assert.equal(staged.changed, true);
  assert.equal(
    await readFile(join(root, "nudaui/.staging/sample/code.css"), "utf8"),
    ".sample {}",
  );
  await assert.rejects(
    readFile(join(root, "src/components/nuda/sample/code.css")),
  );
  await apply(root, "sample");
  const before = await readFile(join(root, "nudaui/manifest.json"), "utf8");
  await writeFile(
    join(root, "src/components/nuda/sample/code.css"),
    ".local {}",
  );
  const again = await stage(root, "sample", payload(), {
    ...metadata,
    retrievedAt: "later",
  });
  assert.deepEqual(again.conflicts, ["code.css"]);
  assert.equal((await apply(root, "sample")).status, "unchanged");
  assert.equal(
    await readFile(join(root, "nudaui/manifest.json"), "utf8"),
    before,
  );
  assert.equal(
    await readFile(join(root, "src/components/nuda/sample/code.css"), "utf8"),
    ".local {}",
  );
});

test("updates compare baseline, working and incoming, and preserve conflicts explicitly", async () => {
  const root = await fixture();
  await stage(root, "sample", payload(), metadata);
  await apply(root, "sample");
  await writeFile(
    join(root, "src/components/nuda/sample/code.css"),
    ".local {}",
  );
  const plan = await stage(root, "sample", payload(".upstream {}"), metadata);
  assert.deepEqual(plan.conflicts, ["code.css"]);
  await assert.rejects(apply(root, "sample"), /conflict/);
  assert.equal(
    await readFile(join(root, "nudaui/baseline/sample/code.css"), "utf8"),
    ".sample {}",
  );
  await apply(root, "sample", true);
  assert.equal(
    await readFile(join(root, "src/components/nuda/sample/code.css"), "utf8"),
    ".local {}",
  );
  assert.equal(
    await readFile(join(root, "nudaui/baseline/sample/code.css"), "utf8"),
    ".upstream {}",
  );
  await stage(root, "sample", payload(".third {}"), metadata);
  await assert.rejects(apply(root, "sample"), /conflict/);
});

test("edits after staging, tampered stages and symlink outputs are rejected", async () => {
  const root = await fixture();
  await stage(root, "sample", payload(), metadata);
  await apply(root, "sample");
  await stage(root, "sample", payload(".incoming {}"), metadata);
  await writeFile(
    join(root, "src/components/nuda/sample/code.css"),
    ".late {}",
  );
  await assert.rejects(apply(root, "sample"), /since inspection/);
  await stage(root, "sample", payload(".incoming {}"), metadata);
  await writeFile(
    join(root, "nudaui/.staging/sample/payload.json"),
    JSON.stringify(payload(".tampered {}")),
  );
  await assert.rejects(apply(root, "sample"), /payload changed/);
  const second = await fixture();
  await mkdir(join(second, "nudaui"));
  await symlink(await fixture(), join(second, "nudaui/.staging"));
  await assert.rejects(stage(second, "sample", payload(), metadata), /Symlink/);
});

test("official registry extraction reads exact literal code without executing JSX or scripts", () => {
  const registry =
    'const components = [{ id: "sample", preview: <div/>, code: [' +
    '{ language: "html", code: `<button>Sample</button>` }, { language: "css", code: `.sample {}` }] }];';
  const result = fromRegistry("sample", registry);
  assert.deepEqual(
    result.code.map((block) => block.code),
    payload().code.map((block) => block.code),
  );
  assert.throws(
    () =>
      fromRegistry("sample", registry.replace("`.sample {}`", "malicious()")),
    /literal/,
  );
  assert.throws(() => fromRegistry("missing", registry));
});

test("committed baselines and provenance checksums match", async () => {
  const { digest, json, blocks } = await import("../tools/nuda-source.mjs");
  const manifest = JSON.parse(await readFile("nudaui/manifest.json", "utf8"));
  for (const [id, entry] of Object.entries(manifest.components)) {
    const source = JSON.parse(
      await readFile(`nudaui/baseline/${id}/payload.json`, "utf8"),
    );
    validatePayload(id, source);
    assert.equal(digest(json(source)), entry.payloadChecksum);
    assert.match(entry.revision, /^[a-f0-9]{40}$/);
    for (const [name, code] of Object.entries(blocks(source))) {
      assert.equal(
        await readFile(`nudaui/baseline/${id}/${name}`, "utf8"),
        code,
      );
      assert.equal(digest(code), entry.files[name]);
    }
  }
});
