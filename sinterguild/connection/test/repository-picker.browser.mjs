import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { chromium } from "playwright";
import { serveFixture } from "./fixture.mjs";

const hostPage = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="stylesheet" href="/src/styles.css">
<main></main>
<script type="module">
  import { mountRepositoryPicker } from "/dist/repository-picker.js";
  const deploymentId = new URLSearchParams(location.search).get("deployment") || "fixture";
  window.completed = [];
  mountRepositoryPicker(document.querySelector("main"), {
    deploymentId,
    managementFetch: (url, options) => fetch(url, {
      ...options,
      headers: { Authorization: "Bearer browser-fixture" },
    }),
    onComplete: repositories => window.completed.push(repositories),
  });
</script>`;

const executablePath = process.env.CHROMIUM_EXECUTABLE;
const phone = { viewport: { width: 320, height: 640 }, isMobile: true, hasTouch: true };
const path = `Owner/${"Repository".repeat(8)}`;
const personal = { sourceId: "Personal-1", path };
const work = { sourceId: `work-${"source".repeat(12)}`, path };
const repositories = [personal, work];

async function openPicker(context, url) {
  const page = context.pages()[0] ?? await context.newPage();
  page.setDefaultTimeout(4_000);
  await page.route("**/v1/repositories", route => {
    assert.equal(route.request().method(), "GET");
    assert.equal(route.request().headers().authorization, "Bearer browser-fixture");
    return route.fulfill({ json: {
      configured: true,
      repositories: [{ ...personal, credential: "fixture-token" }, work],
    } });
  });
  await page.goto(url);
  return page;
}

function choices(page) {
  return repositories.map(({ sourceId }) => page.getByRole("checkbox", { name: `${path} Source ${sourceId}`, exact: true }));
}

function confirmButton(page) {
  return page.getByRole("button", { name: "Confirm selection" });
}

test("confirms equal paths independently by touch and restores the completed selection on reopen", { timeout: 25_000 }, async t => {
  const url = await serveFixture(t, hostPage, ["repository-picker", "repositories", "repository-selection"]);
  const profile = await mkdtemp(join(tmpdir(), "sinterguild-picker-"));
  let context = await chromium.launchPersistentContext(profile, { ...phone, executablePath });
  t.after(async () => {
    await context.close();
    await rm(profile, { force: true, recursive: true });
  });

  const page = await openPicker(context, url);
  const submit = confirmButton(page);
  await submit.waitFor();
  assert.ok(await submit.isDisabled());
  await page.locator("form").evaluate(form => form.requestSubmit());
  assert.deepEqual(await page.evaluate(() => window.completed), []);
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));

  const [personalChoice, workChoice] = choices(page);
  const rows = repositories.map(({ sourceId }) => page.getByText(`Source ${sourceId}`, { exact: true }).locator("xpath=ancestor::label"));
  for (const control of [...rows, submit]) {
    const box = await control.boundingBox();
    assert.ok(box && box.height >= 44 && box.x >= 0 && box.x + box.width <= 320);
  }
  await rows[0].tap();
  assert.ok(await personalChoice.isChecked());
  assert.equal(await workChoice.isChecked(), false);
  await rows[0].tap();
  assert.ok(await submit.isDisabled());
  await rows[1].tap();
  assert.ok(await workChoice.isChecked());
  assert.equal(await personalChoice.isChecked(), false);
  await rows[0].tap();
  assert.equal(await page.evaluate(() => localStorage.length), 0);
  await submit.tap();
  assert.deepEqual(await page.evaluate(() => window.completed), [repositories]);
  assert.deepEqual(await page.evaluate(() => Object.values(localStorage).map(value => JSON.parse(value))), [repositories]);

  await context.close();
  context = await chromium.launchPersistentContext(profile, { ...phone, executablePath });
  const reopened = await openPicker(context, url);
  for (const checkbox of choices(reopened)) {
    await checkbox.waitFor();
    assert.ok(await checkbox.isChecked());
  }
  assert.ok(await confirmButton(reopened).isEnabled());
  assert.deepEqual(await reopened.evaluate(() => window.completed), []);
  await choices(reopened)[0].uncheck();
  assert.deepEqual(await reopened.evaluate(() => Object.values(localStorage).map(value => JSON.parse(value))), [repositories]);
  await confirmButton(reopened).tap();
  assert.deepEqual(await reopened.evaluate(() => window.completed), [[work]]);
  assert.deepEqual(await reopened.evaluate(() => Object.values(localStorage).map(value => JSON.parse(value))), [[work]]);

  await reopened.goto(`${url}/?deployment=another`);
  await confirmButton(reopened).waitFor();
  assert.ok(await confirmButton(reopened).isDisabled());
  for (const checkbox of choices(reopened)) {
    assert.equal(await checkbox.isChecked(), false);
  }
});

test("only a successful empty listing reports that no repositories are visible", { timeout: 15_000 }, async t => {
  const url = await serveFixture(t, hostPage, ["repository-picker", "repositories", "repository-selection"]);
  const browser = await chromium.launch({ executablePath });
  t.after(() => browser.close());
  const page = await browser.newPage();
  page.setDefaultTimeout(4_000);
  const states = [
    [{ json: { configured: true, repositories: [] } }, "No repositories are visible to the connected forge accounts."],
    [{ json: { configured: false, repositories: [] } }, "The repository service is unconfigured."],
    [{ status: 503, body: "backend-fixture-error" }, "Could not load repositories."],
  ];
  for (const [reply, message] of states) {
    await page.route("**/v1/repositories", route => route.fulfill(reply), { times: 1 });
    await page.goto(url);
    await page.getByRole("status").getByText(message, { exact: true }).waitFor();
    assert.equal(await confirmButton(page).isVisible(), false);
    assert.doesNotMatch(await page.content(), /backend-fixture-error/);
  }
});
