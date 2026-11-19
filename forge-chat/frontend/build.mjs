import { copyFile, cp, mkdir } from "node:fs/promises";

await copyFile("src/styles.css", "dist/src/styles.css");
await mkdir("dist/web/app", { recursive: true });
await copyFile("app/index.html", "dist/web/index.html");
await copyFile("dist/app/main.js", "dist/web/app/main.js");
await cp("dist/src", "dist/web/src", { recursive: true, filter: path => !path.endsWith(".d.ts") });
