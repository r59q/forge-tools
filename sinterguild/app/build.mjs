import { copyFile } from "node:fs/promises";

await Promise.all([
  copyFile("index.html", "dist/web/index.html"),
  copyFile("../connection/src/styles.css", "dist/web/sinterguild/connection/src/styles.css"),
  copyFile("../../forge-chat/frontend/src/styles.css", "dist/web/forge-chat/frontend/src/styles.css"),
  copyFile("src/styles.css", "dist/web/sinterguild/app/src/styles.css"),
]);
