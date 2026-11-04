// Runs the standalone app with live reload against a running explorer
// service. See README.md, "Development server".
import { context } from "esbuild";
import { watch } from "node:fs";
import { copyFile, mkdir, readFile, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { fileURLToPath } from "node:url";
import { appEntry, appShell, appStyleFiles, appStyles, common } from "./app-assets.mjs";
import { createDevServer, devPage } from "./dev-server.mjs";

const out = "dist/dev";
const upstream = process.env.EXPLORER_URL ?? "http://127.0.0.1:8090";
const port = Number(process.env.EXPLORER_DEV_PORT ?? 8091);
const token = process.env.FORGE_EXPLORER_TOKEN;

if (!token) {
  console.error("Set FORGE_EXPLORER_TOKEN to the explorer service token.");
  process.exit(1);
}

const server = createDevServer({
  root: out,
  upstream,
  token,
  client: fileURLToPath(new URL("dev-client.js", import.meta.url)),
});
const reload = debounce(server.reload, 100);

await mkdir(out, { recursive: true });
await writeShell();
watchShell();
await watchApp();

createServer(server.handle).listen(port, "127.0.0.1", () => {
  console.log(`Explorer dev server: http://127.0.0.1:${port}/ (API: ${upstream})`);
});

// writeShell writes the files esbuild does not build: page, styles, license.
async function writeShell() {
  const page = await readFile(appShell.page, "utf8");
  await writeFile(`${out}/index.html`, devPage(page));
  await writeFile(`${out}/app.css`, await appStyles());
  await copyFile(appShell.license, `${out}/NUDAUI-LICENSE.txt`);
}

function watchShell() {
  const files = new Set([appShell.page, ...appStyleFiles]);
  const rewrite = debounce(async () => {
    await writeShell().catch((error) => console.error(error.message));
    reload();
  }, 50);

  // Watch folders, not files, so editors that replace files on save work.
  for (const folder of ["app", "src/components"]) {
    watch(folder, { recursive: true }, (_event, name) => {
      if (files.has(`${folder}/${name}`)) {
        rewrite();
      }
    });
  }
}

async function watchApp() {
  const app = await context({
    ...common,
    entryPoints: [appEntry],
    outfile: `${out}/app.js`,
    sourcemap: "inline",
    logLevel: "info",
    plugins: [reloadAfterBuild()],
  });
  await app.watch();
}

function reloadAfterBuild() {
  return {
    name: "reload-after-build",
    setup(build) {
      build.onEnd((result) => {
        if (result.errors.length === 0) {
          reload();
        }
      });
    },
  };
}

function debounce(run, wait) {
  let timer;

  return () => {
    clearTimeout(timer);
    timer = setTimeout(run, wait);
  };
}
