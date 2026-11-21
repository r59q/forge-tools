import assert from "node:assert/strict";
import { test } from "node:test";
import { chromium } from "playwright";
import { serveFixture } from "./fixture.mjs";

const hostPage = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="stylesheet" href="/src/styles.css">
<main></main>
<script type="module">
  import { mountConnectionSetup } from "/dist/setup.js";
  window.accepted = [];
  mountConnectionSetup(document.querySelector("main"), {
    managementFetch: (url, options) => fetch(url, {
      ...options,
      headers: { ...options.headers, Authorization: "Bearer browser-fixture" },
    }),
    onConnected: connection => window.accepted.push(connection),
  });
</script>`;

async function assertTouchTargets(page) {
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
  for (const control of await page.locator("input, select, button").all()) {
    const box = await control.boundingBox();
    assert.ok(box && box.height >= 44 && box.x >= 0 && box.x + box.width <= 320);
    await control.tap({ trial: true });
  }
}

test("phone setup rejects a token and hands off the accepted ID after retry", { timeout: 15_000 }, async t => {
  const url = await serveFixture(t, hostPage, ["setup", "management"]);
  const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_EXECUTABLE });
  t.after(() => browser.close());
  const page = await browser.newPage({
    viewport: { width: 320, height: 640 },
    isMobile: true,
    hasTouch: true,
  });
  page.setDefaultTimeout(4_000);

  const submissions = [];
  await page.route("**/v1/connections", route => {
    const request = route.request();
    const submitted = request.postDataJSON();
    submissions.push({ method: request.method(), auth: request.headers().authorization, ...submitted });
    if (submitted.credential !== "accepted-test-token") {
      return route.fulfill({ status: 400, json: { message: `Rejected ${submitted.credential}` } });
    }
    return route.fulfill({ json: { id: "public-test-id", name: submitted.name, login: "fixture" } });
  });

  await page.goto(url);
  const submit = page.getByRole("button", { name: "Connect forge" });
  const token = page.getByLabel("Access token");
  const forge = page.getByRole("combobox", { name: "Forge", exact: true });
  const host = page.getByLabel("Host (optional)");
  const name = page.getByLabel("Connection name (optional)");
  await submit.waitFor();
  await assertTouchTargets(page);
  await forge.selectOption("gitlab");
  await host.fill("gitlab.example.test");
  await name.fill("Work");
  await token.fill("rejected-test-token");
  await submit.tap();
  const alert = page.getByRole("alert");
  await alert.waitFor();
  assert.match(await alert.innerText(), /forge rejected/);
  assert.deepEqual(await page.evaluate(() => window.accepted), []);
  assert.deepEqual(await Promise.all([forge.inputValue(), host.inputValue(), name.inputValue(), token.inputValue()]),
    ["gitlab", "gitlab.example.test", "Work", ""]);
  await assertTouchTargets(page);

  await token.fill("accepted-test-token");
  await submit.tap();
  await page.waitForFunction(() => window.accepted.length === 1);
  assert.deepEqual(await page.evaluate(() => window.accepted), [{ id: "public-test-id" }]);
  assert.equal(await token.inputValue(), "");
  assert.equal(await alert.isVisible(), false);
  assert.equal(page.url(), `${url}/`);
  assert.doesNotMatch(await page.content(), /(?:rejected|accepted)-test-token/);
  assert.deepEqual(await page.evaluate(() => [localStorage.length, sessionStorage.length]), [0, 0]);
  assert.deepEqual(submissions, ["rejected-test-token", "accepted-test-token"].map(credential => ({
    method: "POST", auth: "Bearer browser-fixture", forge: "gitlab", host: "gitlab.example.test", name: "Work", credential,
  })));
});
