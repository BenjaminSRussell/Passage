import { expect, test } from "@playwright/test";

// Tablet layout (#508): warehouse, shop-floor and picking screens on a touch tablet. Runs against the demo build.
// A coarse pointer and 834px width (an 11-inch tablet upright) select the tablet rules in the modules' pages.module.css.
test.use({ viewport: { width: 834, height: 1112 }, hasTouch: true });

const ROUTES = ["/warehouse/work", "/warehouse/receipts", "/warehouse/docks", "/fulfillment/workbench", "/fulfillment/waves", "/shopfloor/workers", "/shopfloor/time-profiles"];

test.describe("tablet layout of warehouse and shop-floor screens", () => {
  for (const route of ROUTES) {
    test(`${route} fits the screen and keeps touch-sized controls`, async ({ page }) => {
      await page.goto(`.${route}`);
      await page.getByRole("heading", { level: 1 }).first().waitFor();
      await page.getByText("Loading…").first().waitFor({ state: "detached", timeout: 5000 }).catch(() => undefined);
      // The page itself never scrolls sideways; a wide table scrolls inside its own box.
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      expect(overflow, `horizontal overflow on ${route}`).toBeLessThanOrEqual(0);
      // Every button, field and select in the page content is at least 44px tall (the touch target). A link-button in a
      // table cell is as tall as its row, which the table sets, so cells are left out.
      const small = await page.evaluate(() =>
        [...document.querySelectorAll<HTMLElement>("main button, main input:not([type=checkbox]):not([type=radio]), main select")]
          .filter((el) => el.offsetParent !== null && !el.closest("td, th"))
          .map((el) => ({ label: el.getAttribute("aria-label") ?? el.textContent?.trim() ?? el.tagName, height: el.getBoundingClientRect().height }))
          .filter((c) => c.height < 44 && !/^Sort|Clear/.test(c.label)),
      );
      expect(small, `controls under 44px on ${route}`).toEqual([]);
    });
  }
});
