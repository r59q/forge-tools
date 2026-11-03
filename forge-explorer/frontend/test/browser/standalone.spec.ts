import { expect, test } from "@playwright/test";

test("standalone components share compact controls and retain native form and attribution", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Connect", exact: true }).click();
  await expect(page.getByLabel("Service token")).toBeFocused();
  await expect(page.locator("#connection-status")).toHaveText("Enter the service token to connect.");
  await page.getByLabel("Service token").fill("test-only");
  await page.getByLabel("Service token").press("Enter");
  const explorer = page.locator("#explorer");
  await expect(explorer.getByText("No file open.", { exact: true })).toBeVisible();
  await expect(page.getByLabel("Service token")).toHaveAttribute("type", "password");
  await expect(page.getByRole("link", { name: "MIT license" })).toHaveAttribute("href", "/NUDAUI-LICENSE.txt");
  const appButton = page.getByRole("button", { name: "Connect", exact: true });
  const embeddedButton = explorer.getByRole("button", { name: "Refresh tree" });
  const appearance = (element: Element) => {
    const style = getComputedStyle(element);
    return [style.padding, style.borderRadius, style.backgroundColor, style.color, style.fontSize];
  };
  expect(await appButton.evaluate(appearance)).toEqual(await embeddedButton.evaluate(appearance));
  await expect(explorer.getByRole("button", { name: "Back", exact: true })).toBeDisabled();
  await page.getByLabel("Source", { exact: true }).selectOption({ label: "old" });
  await expect(explorer.getByRole("heading", { name: "old", exact: true })).toBeVisible();
  await expect(explorer.getByRole("tab")).toHaveCount(0);
  await page.emulateMedia({ reducedMotion: "reduce" });
  await appButton.hover();
  expect(await appButton.evaluate((el) => getComputedStyle(el, "::after").animationName)).toBe("none");
});

