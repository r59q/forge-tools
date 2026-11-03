import { expect, test } from "@playwright/test";

test("standalone browsing, highlighting, search, selection and history", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto("/");
  await page.getByLabel("Service token").fill("test-only");
  await page.getByRole("button", { name: "Connect", exact: true }).click();
  const explorer = page.locator("#explorer");
  await explorer.getByRole("button", { name: "hello.go", exact: true }).click();
  await expect(explorer.locator(".syntax-keyword").first()).toHaveText(
    "package",
  );
  await expect(explorer.locator("img")).toHaveCount(0);
  await expect(
    explorer.getByRole("button", { name: "Select line 1", exact: true }),
  ).toBeFocused();
  await explorer
    .getByRole("button", { name: "Select line 1", exact: true })
    .press("Shift+ArrowDown");
  await expect(
    explorer.getByText("Selected lines 1–2", { exact: true }),
  ).toBeVisible();
  await explorer.getByLabel("Search query").fill("needle");
  await explorer.getByRole("button", { name: "Search", exact: true }).click();
  await explorer
    .getByRole("button", { name: "hello.go:2", exact: true })
    .click();
  await expect(
    explorer.getByRole("button", { name: "Select line 2", exact: true }),
  ).toBeFocused();
  await explorer.locator("summary").filter({ hasText: "sub" }).click();
  await explorer.getByRole("button", { name: "other.ts", exact: true }).click();
  await expect(
    explorer.getByRole("tab", { name: "sub/other.ts", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await explorer.getByRole("button", { name: "Back", exact: true }).click();
  await expect(
    explorer.getByRole("tab", { name: "hello.go", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await explorer.getByRole("button", { name: "Forward", exact: true }).click();
  // Navigation replaces the tabs and moves focus when the file read finishes.
  await expect(
    explorer.getByRole("tab", { name: "sub/other.ts", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await explorer
    .getByRole("tab", { name: "sub/other.ts", exact: true })
    .focus();
  await page.keyboard.press("ArrowLeft");
  await expect(
    explorer.getByRole("tab", { name: "hello.go", exact: true }),
  ).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(
    explorer.getByRole("tab", { name: "hello.go", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await expect(explorer.getByText(/definitions: unsupported/)).toBeVisible();
  await explorer.getByRole("tab", { name: "hello.go", exact: true }).focus();
  await page.keyboard.press("Delete");
  await expect(
    explorer.getByRole("tab", { name: "sub/other.ts", exact: true }),
  ).toBeFocused();
  await expect(explorer.getByRole("tab")).toHaveCount(1);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(explorer.getByLabel("Search query")).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
  expect(errors).toEqual([]);
});

test("two instances preserve sides, host routing and independent lifecycle", async ({
  page,
}) => {
  await page.goto("/embedding.html");
  await page.getByLabel("Service token").fill("test-only");
  await page.getByRole("button", { name: "Connect both" }).click();
  const old = page.locator("#old");
  const next = page.locator("#new");
  await old.getByRole("button", { name: "hello.go", exact: true }).click();
  await expect(old.locator(".code")).toContainText("package old");
  await expect(next.getByRole("tab")).toHaveCount(0);
  await page.getByRole("button", { name: "Open combined location" }).click();
  await expect(next.locator(".code")).toContainText("package new");
  await old.getByLabel("Search query").fill("needle");
  await old.getByRole("button", { name: "Search", exact: true }).click();
  await expect(old.getByRole("button", { name: "hello.go:2" })).toBeVisible();
  await expect(next.getByLabel("Search query")).toHaveValue("");
  await old.getByRole("button", { name: "hello.go:2" }).click();
  await expect(old.locator(".code")).toContainText("package old");
  await page.getByRole("button", { name: "Dispose old explorer" }).click();
  await expect(old.locator(".explorer")).toHaveCount(0);
  await next.getByRole("button", { name: "Reload file" }).click();
  await expect(next.locator(".code")).toContainText("package new");
  await expect(next.getByRole("tabpanel")).toHaveAttribute(
    "aria-busy",
    "false",
  );
  await page.emulateMedia({ reducedMotion: "reduce" });
  expect(
    await next
      .locator(".nuda-tabs__tab")
      .evaluate((el) => getComputedStyle(el).transitionDuration),
  ).toBe("0s");
});

test("loading, partial, stale and cancellation states are visible", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByLabel("Service token").fill("test-only");
  await page.getByRole("button", { name: "Connect", exact: true }).click();
  const explorer = page.locator("#explorer");
  await page.route("**/search?**", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.results.state = { skipped: 3, truncated: true, canceled: false };
    body.results.matches[0].location.version = "stale";
    await route.fulfill({ json: body });
  });
  await explorer.getByLabel("Search query").fill("needle");
  await explorer.getByRole("button", { name: "Search", exact: true }).click();
  await expect(explorer.getByText(/3 skipped/)).toBeVisible();
  await expect(explorer.getByText(/Truncated at/)).toBeVisible();
  await explorer.getByRole("button", { name: "hello.go:2" }).click();
  await expect(explorer.getByText(/Content changed/)).toBeVisible();
  await expect(explorer.getByRole("tab")).toHaveCount(0);
  await explorer.getByRole("button", { name: "Reload file" }).click();
  await expect(explorer.getByRole("tab")).toHaveCount(1);
  await page.unroute("**/search?**");
  await page.route("**/search?**", async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 500));
    await route.abort();
  });
  await explorer.getByRole("button", { name: "Search", exact: true }).click();
  await expect(explorer.getByText("Searching…", { exact: true })).toBeVisible();
  await explorer
    .getByRole("button", { name: "Cancel search", exact: true })
    .click();
  await expect(explorer.getByText(/Search canceled/)).toBeVisible();
});

test("authentication, empty lookup, path filtering and tree keyboard focus", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByLabel("Service token").fill("wrong");
  await page.getByRole("button", { name: "Connect", exact: true }).click();
  await expect(
    page.getByText("Authentication required.", { exact: true }),
  ).toBeVisible();
  await page.getByLabel("Service token").fill("test-only");
  await page.getByRole("button", { name: "Connect", exact: true }).click();
  const explorer = page.locator("#explorer");
  await explorer.getByRole("button", { name: "hello.go", exact: true }).focus();
  await page.keyboard.press("ArrowDown");
  await expect(
    explorer.locator("summary").filter({ hasText: "sub" }),
  ).toBeFocused();
  await explorer.getByLabel("Search query").fill("no-such-file");
  await explorer.getByRole("button", { name: "Search", exact: true }).click();
  await expect(
    explorer.getByText("No results.", { exact: true }),
  ).toBeVisible();
  await explorer.getByLabel("Search mode").selectOption("files");
  await explorer.getByLabel("Search query").fill("other");
  await explorer.getByLabel("Path filter").fill("sub/*.ts");
  await explorer.getByRole("button", { name: "Search", exact: true }).click();
  await explorer
    .getByRole("button", { name: "sub/other.ts", exact: true })
    .click();
  await expect(
    explorer.getByRole("tab", { name: "sub/other.ts", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
});

test("code rendering stays bounded and keyboard navigation reaches later lines", async ({
  page,
}) => {
  await page.route("**/file?**", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.file.text = Array.from(
      { length: 1000 },
      (_, index) => `const line${index + 1} = ${index + 1};`,
    ).join("\n");
    body.file.location.version = "large-fixture";
    await route.fulfill({ json: body });
  });
  await page.goto("/");
  await page.getByLabel("Service token").fill("test-only");
  await page.getByRole("button", { name: "Connect", exact: true }).click();
  const explorer = page.locator("#explorer");
  await explorer.getByRole("button", { name: "hello.go", exact: true }).click();
  await expect(explorer.locator(".code-line")).toHaveCount(200);
  await explorer.getByLabel("Go to line").fill("999");
  await explorer.getByLabel("Go to line").press("Enter");
  await expect(
    explorer.getByRole("button", { name: "Select line 999", exact: true }),
  ).toBeFocused();
  await page.keyboard.press("Shift+ArrowDown");
  await expect(
    explorer.getByText("Selected lines 999–1000", { exact: true }),
  ).toBeVisible();
  await expect(explorer.locator(".code-line")).toHaveCount(200);
});
