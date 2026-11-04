import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { publishPackage } from "../scripts/publish.mjs";

const manifest = JSON.parse(
  readFileSync(new URL("../package.json", import.meta.url), "utf8"),
);

function npmError(code) {
  return Object.assign(new Error(code), {
    stdout: JSON.stringify({ error: { code } }),
  });
}

test("a missing version publishes to GitHub without lifecycle scripts", () => {
  const calls = [];
  const result = publishPackage(manifest, (command, args) => {
    calls.push({ command, args });
    if (args[0] === "view") throw npmError("E404");
  });
  assert.equal(result, "published");
  assert.deepEqual(calls, [
    {
      command: "npm",
      args: [
        "view", `@r59q/explorer@${manifest.version}`, "version", "--json",
        "--registry", "https://npm.pkg.github.com",
      ],
    },
    {
      command: "npm",
      args: ["publish", "--ignore-scripts", "--registry", "https://npm.pkg.github.com"],
    },
  ]);
});

test("reruns skip existing versions without attempting to overwrite them", () => {
  let calls = 0;
  const result = publishPackage(manifest, () => {
    calls++;
    return JSON.stringify(manifest.version);
  });
  assert.equal(result, "exists");
  assert.equal(calls, 1);
});

test("registry failures and malformed responses fail instead of publishing", () => {
  const errors = ["E401", "E403", "E500"].map(npmError);
  errors.push(new Error("offline"));
  for (const error of errors) {
    let calls = 0;
    assert.throws(
      () => publishPackage(manifest, () => {
        calls++;
        throw error;
      }),
      (caught) => caught === error,
    );
    assert.equal(calls, 1);
  }
  for (const response of ["", "not JSON", '"unexpected"']) {
    assert.throws(() => publishPackage(manifest, () => response));
  }
});

test("publish failures propagate and incorrect destinations are rejected", () => {
  const denied = npmError("E403");
  assert.throws(
    () => publishPackage(manifest, (_, args) => {
      throw args[0] === "view" ? npmError("E404") : denied;
    }),
    (error) => error === denied,
  );
  for (const changes of [{ name: "@other/explorer" }, { publishConfig: {} }]) {
    assert.throws(
      () => publishPackage({ ...manifest, ...changes }),
      /Unexpected package/,
    );
  }
});
