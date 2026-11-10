import { expect, test } from "@playwright/test";
import { ROUTES } from "./routes";
import { ShellPage } from "./shell.page";

test.describe("landmarks and headings", () => {
  for (const route of ROUTES) {
    test(`${route} has exactly one h1 and the shell landmarks`, async ({ page }) => {
      const shell = new ShellPage(page);
      await shell.goto(route);
      await expect(shell.title).toHaveCount(1);
      await expect(shell.title).not.toHaveText("");
      await expect(shell.main).toHaveCount(1);
      await expect(shell.workspaceNav).toHaveCount(1);
      await expect(shell.status.first()).toBeAttached();
    });
  }
});

test.describe("duty gating", () => {
  test("a gated URL without the duty shows the not-available page and no nav entry", async ({ page }) => {
    const shell = new ShellPage(page);
    await shell.goto("/inventory/onhand");
    await expect(shell.title).toHaveText("Page not available");
    await expect(page.getByRole("heading", { name: "This page isn't available to you" })).toBeVisible();
    await expect(shell.workspaceNav.getByRole("link", { name: "Check stock on hand" })).toHaveCount(0);
    await shell.goto("/features");
    await expect(page.getByRole("main").getByRole("link", { name: /Check stock on hand/ })).toHaveCount(0);
  });
});

test.describe("skip link", () => {
  test("is the first focusable element and moves focus to main", async ({ page }) => {
    const shell = new ShellPage(page);
    await shell.goto("/");
    await page.keyboard.press("Tab");
    await expect(shell.skipLink).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(shell.main).toBeFocused();
    await expect(page).toHaveURL(/#main$/);
  });
});

test.describe("command palette", () => {
  test("Ctrl/Cmd+K opens it and focuses the combobox", async ({ page }) => {
    const shell = new ShellPage(page);
    await shell.goto("/");
    await shell.openPaletteWithShortcut();
    await expect(shell.palette).toBeVisible();
    await expect(shell.paletteInput).toBeFocused();
  });

  test("typing 'aud' and Enter opens Audit", async ({ page }) => {
    const shell = new ShellPage(page);
    await shell.goto("/");
    await shell.openPaletteWithShortcut();
    await shell.paletteInput.pressSequentially("aud");
    await expect(shell.paletteOptions.first()).toContainText("Audit");
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(/\/admin\/audit$/);
    await expect(shell.title).toHaveText("Audit");
    await expect(shell.palette).toHaveCount(0);
  });

  test("Escape closes it and returns focus to the opener", async ({ page }) => {
    const shell = new ShellPage(page);
    await shell.goto("/");
    await shell.searchButton.focus();
    await shell.openPaletteWithShortcut();
    await page.keyboard.press("Escape");
    await expect(shell.palette).toHaveCount(0);
    await expect(shell.searchButton).toBeFocused();
  });

  test("arrow keys move aria-activedescendant", async ({ page }) => {
    const shell = new ShellPage(page);
    await shell.goto("/");
    await shell.openPaletteWithShortcut();
    await expect(shell.paletteInput).toHaveAttribute("aria-activedescendant", "palette-option-0");
    await page.keyboard.press("ArrowDown");
    await expect(shell.paletteInput).toHaveAttribute("aria-activedescendant", "palette-option-1");
    await expect(shell.paletteOptions.nth(1)).toHaveAttribute("aria-selected", "true");
    await page.keyboard.press("ArrowUp");
    await expect(shell.paletteInput).toHaveAttribute("aria-activedescendant", "palette-option-0");
  });
});

test.describe("mobile drawer", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("traps focus and Escape closes it", async ({ page }) => {
    const shell = new ShellPage(page);
    await shell.goto("/");
    await shell.openDrawer();
    for (let i = 0; i < 40; i++) {
      await page.keyboard.press("Tab");
      await expect(shell.drawer.locator(":focus")).toHaveCount(1);
    }
    await page.keyboard.press("Escape");
    await expect(shell.drawer).toHaveCount(0);
    await expect(shell.menuButton).toBeFocused();
  });
});

test.describe("color scheme", () => {
  test("dark mode renders a different page background", async ({ browser }) => {
    const bg = async (colorScheme: "light" | "dark") => {
      const context = await browser.newContext({ colorScheme, baseURL: "http://localhost:4173" });
      const page = await context.newPage();
      await new ShellPage(page).goto("/");
      const value = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
      await context.close();
      return value;
    };
    const [light, dark] = [await bg("light"), await bg("dark")];
    expect(dark).not.toBe(light);
  });
});
