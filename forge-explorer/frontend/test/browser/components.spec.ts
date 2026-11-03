import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";

async function connect(page: Page): Promise<void> {
  await page.goto("/");
  await page.getByLabel("Service token").fill("test-only");
  await page.getByRole("button", { name: "Connect", exact: true }).click();
  await expect(page.locator("#connection-status")).toContainText("Connected.");
}

test("file parts preserve tab relationships, breadcrumbs and focus after the last close", async ({ page }) => {
  await connect(page);
  const explorer = page.locator("#explorer");
  await explorer.locator("summary").filter({ hasText: "sub" }).press("Enter");
  await explorer.getByRole("button", { name: "other.ts", exact: true }).click();
  const tab = explorer.getByRole("tab", { name: "sub/other.ts", exact: true });
  const panel = explorer.getByRole("tabpanel");
  await expect(panel).toHaveAttribute("aria-labelledby", await tab.getAttribute("id") as string);
  await expect(tab).toHaveAttribute("aria-controls", await panel.getAttribute("id") as string);
  await expect(panel).toHaveAccessibleName("sub/other.ts");
  const crumbs = explorer.getByRole("navigation", { name: "Breadcrumbs" });
  await expect(crumbs.locator('[aria-current="page"]')).toHaveText("other.ts");
  await expect(crumbs.locator('[aria-hidden="true"]')).toHaveCount(2);
  await crumbs.getByRole("button", { name: "sub", exact: true }).click();
  await expect(explorer.getByRole("navigation", { name: "Files", exact: true }).getByRole("button")).toHaveCount(1);
  await tab.press("Delete");
  await expect(explorer.getByRole("tab")).toHaveCount(0);
  await expect(explorer.getByRole("button", { name: "other.ts", exact: true })).toBeFocused();
  await expect(panel).toHaveAccessibleName("File content");
  await expect(panel.getByRole("status")).toHaveText("No file open.");
});

test("tree rows retain disclosure, unsupported, partial, empty and error states", async ({ page }) => {
  let mode = "partial";
  await page.route("**/tree?**", async (route) => {
    if (mode === "error") {
      await route.fulfill({ status: 500, json: { code: 13 } });
      return;
    }
    const response = await route.fetch();
    const body = await response.json();
    if (mode === "partial") body.directory.entries.push({ path: "<img src=x>", kind: "unsupported" });
    else body.directory.entries = [];
    body.directory.state = { skipped: 1, truncated: true, canceled: true };
    await route.fulfill({ json: body });
  });
  await connect(page);
  const explorer = page.locator("#explorer");
  const tree = explorer.getByRole("navigation", { name: "Files", exact: true });
  await expect(tree.getByRole("button", { name: "<img src=x>", exact: true })).toBeDisabled();
  await expect(tree.locator("img")).toHaveCount(0);
  await expect(tree.getByRole("status")).toContainText("Canceled; results may be incomplete.");
  mode = "empty";
  await explorer.getByRole("button", { name: "Refresh tree" }).click();
  await expect(tree.getByRole("status")).toContainText("No results.");
  mode = "error";
  await explorer.getByRole("button", { name: "Refresh tree" }).click();
  await expect(tree.locator(".error")).toBeVisible();
  await expect(tree).toHaveAttribute("aria-busy", "false");
});

test("search parts render previews as text and move focus between results", async ({ page }) => {
  await page.route("**/search?**", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    const first = body.results.matches[0];
    body.results.matches = [first, { ...first, preview: "<img src=x onerror=alert(1)>" }];
    body.results.state.canceled = true;
    await route.fulfill({ json: body });
  });
  await connect(page);
  const explorer = page.locator("#explorer");
  await explorer.getByLabel("Search query").fill("needle");
  await explorer.getByLabel("Search query").press("Enter");
  const results = explorer.locator(".search-results");
  await expect(results.locator("li")).toHaveCount(2);
  await expect(results.locator("code").last()).toHaveText("<img src=x onerror=alert(1)>");
  await expect(results.locator("img")).toHaveCount(0);
  await expect(explorer.getByText(/2 results.*Canceled/)).toBeVisible();
  await results.getByRole("button").first().press("ArrowDown");
  await expect(results.getByRole("button").last()).toBeFocused();
  await expect(explorer.getByRole("button", { name: "Cancel search" })).toBeDisabled();
});

test("shared styles stay shadow scoped and themes remain per instance", async ({ page }) => {
  await page.goto("/embedding.html");
  await page.getByLabel("Service token").fill("test-only");
  await page.getByRole("button", { name: "Connect both" }).click();
  const old = page.locator("#old");
  const next = page.locator("#new");
  await old.evaluate((host) => host.style.setProperty("--explorer-accent", "rgb(255, 128, 0)"));
  await next.evaluate((host) => host.style.setProperty("--explorer-accent", "rgb(0, 255, 128)"));
  await page.addStyleTag({ content: "button { background: rgb(255, 0, 0) !important; }" });
  for (const host of [old, next]) {
    await host.getByRole("button", { name: "hello.go", exact: true }).click();
    await expect(host.getByRole("tabpanel")).toHaveAccessibleName("hello.go");
  }
  await expect(old.getByRole("tab")).toHaveCSS("background-color", "rgb(255, 128, 0)");
  await expect(next.getByRole("tab")).toHaveCSS("background-color", "rgb(0, 255, 128)");
  await old.getByRole("tab").press("Delete");
  await expect(old.getByRole("tab")).toHaveCount(0);
  await expect(next.getByRole("tab")).toHaveAttribute("aria-selected", "true");
  await expect(next.getByRole("tabpanel")).toHaveAccessibleName("hello.go");
});
