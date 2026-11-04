import { build } from "esbuild";
import { mkdir, readFile, writeFile, copyFile } from "node:fs/promises";
import { execFileSync } from "node:child_process";
import { appEntry, appShell, appStyles, common } from "./app-assets.mjs";

await mkdir("dist/app", { recursive: true });
await build({
  ...common,
  entryPoints: ["src/index.ts"],
  outfile: "dist/explorer.js",
});
await build({
  ...common,
  entryPoints: [appEntry],
  outfile: "dist/app/app.js",
  minify: true,
});
await build({
  ...common,
  entryPoints: ["examples/embedding-app.ts"],
  outfile: "dist/app/embedding.js",
});
await copyFile("examples/embedding.html", "dist/app/embedding.html");
execFileSync(
  "node",
  [
    "node_modules/typescript/bin/tsc",
    "--emitDeclarationOnly",
    "--rootDir",
    "src",
    "--project",
    "tsconfig.build.json",
  ],
  { stdio: "inherit" },
);
const { styles } = await import("../dist/explorer.js");
await writeFile("dist/styles.css", styles);
await copyFile(appShell.page, "dist/app/index.html");
await writeFile("dist/app/app.css", await appStyles());
await copyFile(appShell.license, "dist/app/NUDAUI-LICENSE.txt");
// Include upstream notices in the consumable JS bundle as well as the package.
const license = await readFile(appShell.license, "utf8");
for (const file of [
  "dist/explorer.js",
  "dist/app/app.js",
  "dist/app/embedding.js",
]) {
  await writeFile(
    file,
    `/* NudaUI component styles\n${license}*/\n` +
      (await readFile(file, "utf8")),
  );
}
