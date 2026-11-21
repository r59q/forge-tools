import assert from "node:assert/strict";
import { test } from "node:test";
import { chromium } from "playwright";
import { fixture } from "./browser-fixture.mjs";

test("standalone chat handles a live turn, reconnect, inputs and session navigation", { timeout: 25_000 }, async t => {
  const server = await fixture();
  t.after(() => server.close());
  const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_EXECUTABLE });
  t.after(() => browser.close());
  const page = await browser.newPage();
  page.setDefaultTimeout(4_000);

  await page.goto(server.url);
  const token = page.getByLabel("Chat service token");
  await token.fill("wrong");
  await page.getByRole("button", { name: "Open chats" }).click();
  await page.getByText("The service did not accept this token.").waitFor();
  await token.fill("browser-test");
  await page.getByRole("button", { name: "Open chats" }).click();
  const chats = page.locator(".chat-list");
  await chats.getByRole("button", { name: /Claude Code · parent/ }).waitFor();
  assert.match(await chats.getByRole("button").first().innerText(), /codex · child/);
  await chats.getByRole("button", { name: /Claude Code · parent/ }).click();
  await page.getByText("Child result: the build passes").waitFor();

  await page.getByRole("textbox", { name: "Message", exact: true }).fill("Check the build");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  const activity = page.locator("details.activity");
  await activity.getByText("Read · Call · README.md", { exact: true }).first().waitFor();
  assert.equal(await activity.getAttribute("open"), null);
  await activity.locator("summary").click();
  server.interrupt();
  await activity.locator("summary").filter({ hasText: "Read · Result · Build steps" }).waitFor();
  assert.notEqual(await activity.getAttribute("open"), null);
  assert.deepEqual(server.cursors, ["0", "3"]);

  await page.getByRole("textbox", { name: "Answer", exact: true }).fill("Linux");
  await page.getByRole("button", { name: "Answer", exact: true }).click();
  await page.getByRole("button", { name: "Allow", exact: true }).click();
  assert.equal(await page.getByRole("link", { name: "the diff" }).getAttribute("href"), "https://example.test/diff");
  await page.getByText("<img src=/bad>", { exact: false }).waitFor();
  assert.equal(await page.locator(".transcript img").count(), 0);
  assert.deepEqual(server.answers, [{ input: "question", text: "Linux" }, { input: "permission", allow: true }]);

  const family = page.locator(".session-tree");
  await family.getByRole("button", { name: "codex · child" }).click();
  await page.getByRole("heading", { name: "codex · child" }).waitFor();
  await family.getByRole("button", { name: "Claude Code · parent" }).click();
  await page.getByLabel("Link another chat").selectOption("child");
  await page.locator(".linked-chat").getByRole("button", { name: "codex · child", exact: true }).waitFor();
  await page.getByRole("button", { name: "Archive", exact: true }).click();
  await chats.getByRole("button", { name: /Claude Code · parent/ }).waitFor({ state: "detached" });
  assert.equal(server.agents.find(agent => agent.id === "parent").state, "idle");
  await page.getByRole("button", { name: "Show archived" }).click();
  await chats.getByRole("button", { name: /Claude Code · parent/ }).waitFor();
  await page.getByRole("button", { name: "Stop", exact: true }).click();
  await page.locator(".state-pill").filter({ hasText: "stopped" }).waitFor();
});

test("create scratch and default branch worktree chats", { timeout: 15_000 }, async t => {
  const server = await fixture();
  t.after(() => server.close());
  const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_EXECUTABLE });
  t.after(() => browser.close());
  const page = await browser.newPage();
  page.setDefaultTimeout(4_000);

  await page.goto(server.url);
  await page.getByLabel("Chat service token").fill("browser-test");
  await page.getByRole("button", { name: "Open chats" }).click();
  await page.getByText("New chat", { exact: true }).click();
  await page.getByRole("button", { name: "Create chat" }).click();
  await page.getByRole("heading", { name: "Claude Code · new-1" }).waitFor();
  assert.equal(server.creates[0].workspace, undefined);

  await page.locator("details.create-chat:not([open])").waitFor();
  await page.getByText("New chat", { exact: true }).click();
  await page.getByLabel("Client", { exact: true }).selectOption("codex");
  await page.getByLabel("Model", { exact: true }).fill("test-model");
  await page.getByLabel("Effort", { exact: true }).selectOption("high");
  await page.getByLabel("Workspace", { exact: true }).selectOption("forge");
  await page.getByLabel("Source", { exact: true }).fill("test-source");
  await page.getByLabel("Repository", { exact: true }).fill("owner/repo");
  await page.getByRole("button", { name: "Create chat" }).click();
  await page.getByRole("heading", { name: "codex · new-2" }).waitFor();
  assert.deepEqual(server.creates[1], {
    client: "codex", model: "test-model", effort: "high",
    workspace: { kind: "checkout", repository: { source: "test-source", path: "owner/repo" } },
  });
});

test("touch host starts and streams chats for selected repositories with the same path", { timeout: 15_000 }, async t => {
  const server = await fixture();
  t.after(() => server.close());
  const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_EXECUTABLE });
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width: 320, height: 568 }, isMobile: true, hasTouch: true });
  page.setDefaultTimeout(4_000);
  await page.goto(`${server.url}/selected-repositories`);

  const path = "example-organization/shared-repository-with-a-long-name";
  const repository = page.getByLabel("Repository", { exact: true });
  const transcript = page.locator(".transcript");
  for (const [index, source] of ["github", "forgejo"].entries()) {
    await page.getByText("New chat", { exact: true }).tap();
    await repository.tap();
    assert.equal(await page.getByLabel("Source", { exact: true }).count(), 0);
    await repository.selectOption({ label: `Connection ${source} · ${path}` });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth), 320);
    await page.getByRole("button", { name: "Create chat", exact: true }).tap();
    const id = `new-${index + 1}`;
    await page.getByRole("heading", { name: `Claude Code · ${id}` }).waitFor();
    assert.deepEqual(server.creates[index].workspace, { kind: "checkout", repository: { source, path } });

    const text = `Review ${source}`;
    const composer = page.getByRole("textbox", { name: "Message", exact: true });
    await composer.tap();
    await composer.fill(text);
    await page.getByRole("button", { name: "Send", exact: true }).tap();
    await transcript.getByText(`Reply from ${id}`, { exact: true }).waitFor();
    assert.deepEqual(server.messages[index], { id, text });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth), 320);
  }
});
