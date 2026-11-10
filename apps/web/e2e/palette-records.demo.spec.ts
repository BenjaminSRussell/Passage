import { expect, test } from "@playwright/test";
import { ShellPage } from "./shell.page";

/**
 * Record search in the command palette (#506). Runs against the demo build: its search route answers from the demo
 * company's records with the search service's own matching rules (src/shell/search/match.ts), so what is found
 * here is what the service finds: a number by its start, a name by what it holds or a near miss.
 */
test.describe("palette record search", () => {
  let shell: ShellPage;

  test.beforeEach(async ({ page }) => {
    shell = new ShellPage(page);
    await shell.goto("./");
    await shell.openPaletteWithShortcut();
  });

  const group = (name: string) => shell.palette.getByRole("group", { name });

  test("finds a vendor by a misspelt name and opens it", async ({ page }) => {
    await shell.paletteInput.fill("ironbrige foundry");
    await expect(group("Customers and vendors").getByRole("option", { name: /V-20004 Ironbridge Foundry/ })).toBeVisible();
    await page.keyboard.press("Enter");
    await expect(shell.palette).toHaveCount(0);
    await expect(page).toHaveURL(/\/party\/[^/]+$/);
    await expect(page.getByText("Ironbridge Foundry").first()).toBeVisible();
  });

  test("finds production orders by the start of the number, not by digits from its middle", async () => {
    await shell.paletteInput.fill("mo-2461");
    const orders = group("Production orders");
    await expect(orders.getByRole("option", { name: /^Production order MO-24611, Started/ })).toBeVisible();
    await expect(orders.getByRole("option", { name: /^Production order MO-24615, Released/ })).toBeVisible();
    // The exact number comes first.
    await shell.paletteInput.fill("mo-24611");
    await expect(shell.paletteOptions.first()).toHaveAccessibleName(/^Production order MO-24611/);
    await expect(orders.getByRole("option")).toHaveCount(1);
    await shell.paletteInput.fill("24611");
    await expect(orders).toHaveCount(0);
  });

  test("opens records from the keyboard and keeps them as recent records", async ({ page }) => {
    await shell.paletteInput.fill("mo-24611");
    await expect(group("Production orders").getByRole("option")).toHaveCount(1);
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(/\/production\/MO-24611$/);
    await expect(page.getByRole("heading", { name: "Production order MO-24611" })).toBeVisible();

    await shell.openPaletteWithShortcut();
    await expect(group("Recent records").getByRole("option", { name: /^Production order MO-24611/ })).toBeVisible();

    await shell.paletteInput.fill("keystone seals");
    await expect(group("Customers and vendors").getByRole("option", { name: /V-20017 Keystone Seals & Gaskets/ })).toBeVisible();
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(/\/party\/[^/]+$/);

    await shell.openPaletteWithShortcut();
    const recent = group("Recent records").getByRole("option");
    await expect(recent).toHaveCount(2);
    await expect(recent.first()).toHaveAccessibleName(/V-20017 Keystone Seals & Gaskets/);
    await expect(recent.last()).toHaveAccessibleName(/^Production order MO-24611/);
  });

  test("keeps a favourite record at the top of the empty palette", async ({ page }) => {
    await shell.paletteInput.fill("v-20011");
    await expect(group("Customers and vendors").getByRole("option", { name: /V-20011 Volta Motor Works/ })).toBeVisible();
    await page.keyboard.press("Alt+f");
    await expect(shell.status.filter({ hasText: "V-20011" }).first()).toBeAttached();
    await shell.paletteInput.fill("");
    await expect(group("Favourite records").getByRole("option", { name: /V-20011 Volta Motor Works.*Favourite/ })).toBeVisible();
  });
});
