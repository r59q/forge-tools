// Compose the independently buildable backend and frontend for browser integration.
import { spawn } from "node:child_process";
import { mkdtemp, mkdir, writeFile, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("./", import.meta.url));
const temporary = await mkdtemp(join(tmpdir(), "forge-explorer-browser-"));
let service;
async function run(command, args, cwd, env = process.env) {
  await new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, env, stdio: "inherit" });
    child.on("error", reject);
    child.on("exit", (code) =>
      code === 0 ? resolve() : reject(new Error(`${command} exited ${code}`)),
    );
  });
}
try {
  for (const side of ["old", "new", "local"]) {
    await mkdir(join(temporary, side, "sub"), { recursive: true });
    await writeFile(
      join(temporary, side, "hello.go"),
      `package ${side}\n// needle\nvar text = "<img src=x onerror=alert(1)>"`,
    );
    await writeFile(join(temporary, side, "sub/other.ts"), "const value = 42;");
  }
  await writeFile(
    join(temporary, "local", "README.md"),
    await readFile(
      join(root, "../examples/fullstack/explorer-content/README.md"),
    ),
  );
  const binary = join(temporary, "service");
  await run(
    "go",
    ["build", "-o", binary, "./cmd/forge-explorer-service"],
    join(root, "service"),
  );
  await run("npm", ["run", "build"], join(root, "frontend"));
  const url = "http://127.0.0.1:18190";
  service = spawn(
    binary,
    [
      "--roots",
      `old=${temporary}/old;new=${temporary}/new;local=${temporary}/local`,
      "--assets",
      join(root, "frontend/dist/app"),
      "--http-listen",
      "127.0.0.1:18190",
      "--grpc-listen",
      "127.0.0.1:19190",
    ],
    {
      env: { ...process.env, FORGE_EXPLORER_TOKEN: "test-only" },
      stdio: "inherit",
    },
  );
  service.on("error", (error) => {
    console.error(error.message);
  });
  let ready = false;
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      ready = (await fetch(url)).ok;
    } catch {
      /* Service is still starting. */
    }
    if (ready) break;
    if (service.exitCode !== null)
      throw new Error("Service stopped during startup");
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  if (!ready) throw new Error("Service did not start");
  await run(
    "sh",
    [join(root, "../scripts/explorer-integration-test.sh")],
    root,
    {
      ...process.env,
      FORGE_EXPLORER_TEST_TOKEN: "test-only",
      FORGE_EXPLORER_TEST_URL: url,
    },
  );
  await run("npm", ["run", "test:browser"], join(root, "frontend"), {
    ...process.env,
    EXPLORER_BROWSER_URL: url,
  });
} finally {
  if (service && service.exitCode === null) {
    service.kill("SIGTERM");
    await new Promise((resolve) => service.once("exit", resolve));
  }
  await rm(temporary, { recursive: true, force: true });
}
