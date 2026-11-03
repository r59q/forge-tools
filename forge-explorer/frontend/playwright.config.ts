import { defineConfig } from "@playwright/test";

const url = process.env.EXPLORER_BROWSER_URL;
export default defineConfig({
  testDir: "./test/browser",
  workers: 1,
  timeout: 30_000,
  use: {
    baseURL: url ?? "http://127.0.0.1:18189",
    headless: true,
    launchOptions: process.env.EXPLORER_CHROMIUM
      ? { executablePath: process.env.EXPLORER_CHROMIUM }
      : {},
  },
  webServer: url
    ? undefined
    : {
        command: "node test/server.mjs",
        url: "http://127.0.0.1:18189",
        reuseExistingServer: false,
      },
});
