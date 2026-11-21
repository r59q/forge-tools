import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { chromium } from "playwright";
import { agentId, chatToken, forgeToken, operatorToken, personal, reply, startFixture, target } from "./fixture.mjs";

const phone = {
  viewport: { width: 320, height: 640 },
  isMobile: true,
  hasTouch: true,
  executablePath: process.env.CHROMIUM_EXECUTABLE,
};

test("touch onboarding reaches a repository chat and restores the selection after reopening", { timeout: 35_000 }, async t => {
  const { url, management, chat } = await startFixture(t);
  const profile = await mkdtemp(join(tmpdir(), "sinterguild-app-"));
  let context = await chromium.launchPersistentContext(profile, phone);
  t.after(async () => {
    await context.close();
    await rm(profile, { force: true, recursive: true });
  });

  const page = await openEntry(context, url);
  await signIn(page, "Management operator token", "Begin setup", operatorToken);
  await setUpRepositories(page);
  assert.deepEqual(management.attempts.map(({ name, credential }) => [name, credential]), [
    ["Work", "rejected-forge-token"], ["Work", forgeToken], ["Personal", forgeToken],
  ]);
  assert.ok(management.requests.every(({ headers }) => headers.authorization === `Bearer ${operatorToken}`));

  const chatLabel = "Chat service token";
  await signIn(page, chatLabel, "Open chats", operatorToken);
  await page.getByRole("alert").getByText("The service did not accept this token.").waitFor();
  assert.equal(await page.getByText("New chat", { exact: true }).count(), 0);
  await signIn(page, chatLabel, "Open chats", chatToken);
  await startConversation(page);
  assert.deepEqual(chat.creates.map(({ workspace }) => workspace), [
    { kind: "checkout", repository: { source: target.sourceId, path: target.path } },
  ]);
  assert.deepEqual(chat.messages, ["How do I run the tests?"]);
  assert.equal(chat.streams.size, 1, "the reply arrived while the stream was open");
  const stream = chat.requests.find(({ url }) => url === `/v1/agents/${agentId}/stream?after=0`);
  assert.equal(stream?.headers.authorization, `Bearer ${chatToken}`);
  assert.deepEqual(await page.evaluate(() => [Object.values(localStorage).map(value => JSON.parse(value)), sessionStorage.length]),
    [[[personal, target]], 0]);

  const previousManagementRequests = management.requests.length;
  await context.close();
  context = await chromium.launchPersistentContext(profile, phone);
  const reopened = await openEntry(context, url);
  await reopened.getByLabel(chatLabel).waitFor();
  assert.equal(await reopened.getByLabel("Management operator token").count(), 0);
  assert.equal(await reopened.getByText("New chat", { exact: true }).count(), 0);
  await signIn(reopened, chatLabel, "Open chats", chatToken);
  await reopened.getByText("New chat", { exact: true }).tap();
  await assertConversationChoices(reopened);
  assert.equal(management.requests.length, previousManagementRequests);
  assert.equal(reopened.url(), `${url}/`);
});

test("a saved connection survives an interrupted setup and repository listing can be retried", { timeout: 25_000 }, async t => {
  const { url, management } = await startFixture(t);
  const browser = await chromium.launch({ executablePath: phone.executablePath });
  t.after(() => browser.close());
  const page = await openEntry(await browser.newContext(phone), url);
  await signIn(page, "Management operator token", "Begin setup", operatorToken);
  await page.getByLabel("Access token").fill(forgeToken);
  await page.getByRole("button", { name: "Connect forge" }).tap();
  await repositoryRow(page, target).waitFor();
  assert.equal(await page.evaluate(() => localStorage.length), 0);

  await page.route("**/api/management/v1/repositories", route => route.fulfill({ status: 503 }), { times: 1 });
  await page.reload();
  await signIn(page, "Management operator token", "Begin setup", operatorToken);
  await page.getByRole("status").getByText("Could not load repositories.", { exact: true }).waitFor();
  assert.equal(await page.getByLabel("Access token").count(), 0);

  const retry = page.getByRole("button", { name: "Reload repositories" });
  await assertPhoneLayout(page, retry);
  await retry.tap();
  await repositoryRow(page, target).tap();
  await page.getByRole("button", { name: "Confirm selection" }).tap();
  await page.getByLabel("Chat service token").waitFor();
  assert.equal(management.attempts.length, 1);
});

async function openEntry(context, url) {
  const page = context.pages()[0] ?? await context.newPage();
  page.setDefaultTimeout(5_000);
  await page.goto(url);
  return page;
}

async function signIn(page, label, action, token) {
  const field = page.getByLabel(label);
  const button = page.getByRole("button", { name: action, exact: true });
  await field.fill(token);
  await assertPhoneLayout(page, field, button);
  await button.tap();
}

async function setUpRepositories(page) {
  const forge = page.getByRole("combobox", { name: "Forge", exact: true });
  const token = page.getByLabel("Access token");
  const name = page.getByLabel("Connection name (optional)");
  const connect = page.getByRole("button", { name: "Connect forge", exact: true });
  await name.fill("Work");
  await token.fill("rejected-forge-token");
  await assertPhoneLayout(page, forge, page.getByLabel("Host (optional)"), name, token, connect);
  await connect.tap();
  await page.getByRole("alert").getByText(/The forge rejected/).waitFor();
  assert.equal(await name.inputValue(), "Work");
  assert.equal(await token.inputValue(), "");
  await token.fill(forgeToken);
  await connect.tap();

  await addAnotherConnection(page);
  const confirm = page.getByRole("button", { name: "Confirm selection", exact: true });
  for (const repository of [personal, target]) {
    const row = repositoryRow(page, repository);
    await row.waitFor();
    await assertPhoneLayout(page, row);
    await row.tap();
    assert.ok(await row.getByRole("checkbox").isChecked());
  }
  await assertPhoneLayout(page, confirm);
  await confirm.tap();
}

async function addAnotherConnection(page) {
  const add = page.getByRole("button", { name: "Add a forge connection" });
  await add.waitFor();
  await assertPhoneLayout(page, add);
  await add.tap();
  const back = page.getByRole("button", { name: "Back to repositories" });
  await assertPhoneLayout(page, back);
  await back.tap();
  await repositoryRow(page, target).waitFor();
  await add.tap();
  await page.getByLabel("Connection name (optional)").fill("Personal");
  await page.getByLabel("Access token").fill(forgeToken);
  await page.getByRole("button", { name: "Connect forge" }).tap();
}

function repositoryRow(page, { path, sourceId }) {
  return page.locator("label").filter({ hasText: `Source ${sourceId}` }).filter({ hasText: path });
}

async function assertConversationChoices(page) {
  const repository = page.getByLabel("Repository", { exact: true });
  await repository.waitFor();
  assert.deepEqual(await repository.locator("option").allTextContents(), [
    "Choose a repository",
    ...[personal, target].map(({ sourceId, path }) => `Connection ${sourceId} · ${path}`),
  ]);
  assert.equal(await page.getByLabel("Source", { exact: true }).count(), 0);
  return repository;
}

async function startConversation(page) {
  const newChat = page.getByText("New chat", { exact: true });
  await newChat.waitFor();
  await assertPhoneLayout(page, newChat);
  await newChat.tap();
  const repository = await assertConversationChoices(page);
  const create = page.getByRole("button", { name: "Create chat", exact: true });
  await assertPhoneLayout(page, repository, create);
  await repository.tap();
  await repository.selectOption({ label: `Connection ${target.sourceId} · ${target.path}` });
  await create.tap();

  const message = page.getByRole("textbox", { name: "Message", exact: true });
  const send = page.getByRole("button", { name: "Send", exact: true });
  await message.waitFor();
  await assertPhoneLayout(page, message, send);
  await message.tap();
  await message.fill("How do I run the tests?");
  await send.tap();
  await page.locator(".transcript").getByText(reply, { exact: true }).waitFor();
  await assertPhoneLayout(page, message, send);
}

async function assertPhoneLayout(page, ...controls) {
  assert.deepEqual(await page.evaluate(() => [
    document.documentElement.clientWidth,
    document.documentElement.scrollWidth,
  ]), [320, 320]);
  for (const control of controls) {
    const box = await control.boundingBox();
    assert.ok(box && box.height >= 44 && box.x >= 0 && box.x + box.width <= 320);
  }
}
