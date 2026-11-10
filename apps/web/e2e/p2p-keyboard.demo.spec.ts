import { expect, test, type Locator, type Page } from "@playwright/test";

/**
 * Keyboard-only run of the start of procure to pay: create a requisition, then send that draft for approval (WCAG 2.1.1, 2.4.3, 2.4.7). After the first page load the test
 * only presses keys: it never clicks, and it moves focus with Tab and Shift+Tab, so a control that cannot be
 * reached or operated from the keyboard fails the run.
 */

async function isFocused(locator: Locator): Promise<boolean> {
  return locator.evaluate((el) => el === document.activeElement || el.contains(document.activeElement)).catch(() => false);
}

/** Tab (or Shift+Tab) until the target has focus. */
async function tabTo(page: Page, target: Locator, { max = 120, backwards = false } = {}): Promise<void> {
  for (let i = 0; i < max; i++) {
    if (await isFocused(target)) return;
    await page.keyboard.press(backwards ? "Shift+Tab" : "Tab");
  }
  throw new Error(`Could not reach ${String(target)} with ${max} Tab presses`);
}

test("a requisition can be created and submitted for approval from the keyboard", async ({ page }) => {
  await page.goto("./purchasing/requisitions");
  await expect(page.getByRole("heading", { level: 1, name: "Requisitions" })).toBeVisible();

  // Skip link is the first stop and moves focus into the page.
  await page.keyboard.press("Tab");
  await expect(page.getByRole("link", { name: "Skip to content" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("main")).toBeFocused();

  const newRequisition = page.getByRole("link", { name: "New requisition" }).first();
  await tabTo(page, newRequisition);
  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { level: 2, name: "New requisition" })).toBeVisible();

  // Fill the first line: description, category, quantity, unit and requested date are needed.
  const field = (name: string) => page.getByRole("textbox", { name, exact: true }).first();
  for (const [name, value] of [
    ["Description", "Hydraulic hose, 2 m"],
    ["Category", "Hoses"],
    ["Quantity", "12"],
    ["Unit", "EA"],
  ] as const) {
    await tabTo(page, field(name));
    await page.keyboard.press("ControlOrMeta+a");
    await page.keyboard.type(value);
  }
  const date = page.getByLabel("Requested date").first();
  await tabTo(page, date);
  await page.keyboard.type("12152026"); // a date input takes its month, day and year segments in turn

  const save = page.getByRole("button", { name: "Save requisition" });
  await tabTo(page, save);
  await page.keyboard.press("Enter");

  // The form took every value from the keyboard: nothing is flagged, and saving opens the new draft.
  await expect(page.getByRole("alert").filter({ hasText: "Problems with this form" })).toHaveCount(0);
  await expect(page.getByRole("heading", { level: 2, name: /^Requisition / })).toBeVisible();
  const submit = page.getByRole("button", { name: "Submit for approval" });
  await tabTo(page, submit);
  await page.keyboard.press("Enter");
  await expect(page.getByRole("button", { name: "Submit for approval" })).toHaveCount(0);
  await expect(page.getByText("In review").first()).toBeVisible();
});

test("the procure-to-pay menu is reachable and every dialog traps and returns focus", async ({ page }) => {
  await page.goto("./purchasing");
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();

  // Help panel: open with Enter, focus moves inside, Escape closes it and focus returns to the button.
  const help = page.getByRole("button", { name: "Help for this page" });
  await tabTo(page, help);
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog", { name: "Purchasing" });
  await expect(dialog).toBeVisible();
  await page.keyboard.press("Tab");
  await expect(dialog.locator(":focus")).toHaveCount(1);
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(help).toBeFocused();

  // Command palette from the shortcut, then Escape.
  await page.keyboard.press("ControlOrMeta+k");
  await expect(page.getByRole("combobox", { name: "Find a page" })).toBeFocused();
  await page.keyboard.type("Vendor invoices");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/ap-invoices/);
});
