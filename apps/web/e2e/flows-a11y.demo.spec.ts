import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { FLOW_ROUTES } from "./flowRoutes";

// Runs against the demo build (fake backend, every duty held) so the pages render real data, not error states.
// WCAG 2.2 AA: the 2.0 and 2.1 sets plus the 2.2 additions axe can test (target size, focus not obscured).
const WCAG_TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"];

test.describe("axe: WCAG 2.2 AA on procure-to-pay and order-to-cash pages", () => {
  for (const route of FLOW_ROUTES) {
    test(`${route} has no serious or critical violations`, async ({ page }, testInfo) => {
      await page.goto(`.${route}`);
      await page.getByRole("heading", { level: 1 }).first().waitFor();
      // Pages the demo backend has no data for keep their loading state; that state is scanned as it is.
      await page.getByText("Loading…").first().waitFor({ state: "detached", timeout: 5000 }).catch(() => undefined);
      const results = await new AxeBuilder({ page }).withTags(WCAG_TAGS).analyze();
      const blocking = results.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
      if (results.violations.length > 0) {
        await testInfo.attach("violations", { body: JSON.stringify(results.violations, null, 2), contentType: "application/json" });
      }
      expect(
        blocking.map((v) => `${v.id} (${v.impact}): ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`),
        `serious/critical axe violations on ${route}`,
      ).toEqual([]);
    });
  }
});
