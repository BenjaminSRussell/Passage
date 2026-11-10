import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page, type TestInfo } from "@playwright/test";
import { ROUTES } from "./routes";
import { ShellPage } from "./shell.page";

const WCAG_TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"];

/** Scan and fail on serious/critical violations; lesser ones are attached for the record. */
async function expectNoSeriousViolations(page: Page, testInfo: TestInfo, label: string): Promise<void> {
  const results = await new AxeBuilder({ page }).withTags(WCAG_TAGS).analyze();
  const blocking = results.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  const minor = results.violations.filter((v) => !blocking.includes(v));
  if (minor.length > 0) {
    await testInfo.attach(`${label}-minor-violations`, {
      body: JSON.stringify(minor, null, 2),
      contentType: "application/json",
    });
  }
  if (blocking.length > 0) {
    await testInfo.attach(`${label}-violations`, {
      body: JSON.stringify(results.violations, null, 2),
      contentType: "application/json",
    });
  }
  expect(
    blocking.map((v) => `${v.id} (${v.impact}): ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`),
    `serious/critical axe violations on ${label}`,
  ).toEqual([]);
}

test.describe("axe: WCAG 2.1 AA per route", () => {
  for (const route of ROUTES) {
    test(`${route} has no serious or critical violations`, async ({ page }, testInfo) => {
      const shell = new ShellPage(page);
      await shell.goto(route);
      await expect(page.getByText("Loading…")).toHaveCount(0);
      await expectNoSeriousViolations(page, testInfo, route);
    });
  }
});

test.describe("axe: dynamic states", () => {
  test("command palette open", async ({ page }, testInfo) => {
    const shell = new ShellPage(page);
    await shell.goto("/");
    await shell.openPaletteWithShortcut();
    await expect(shell.paletteOptions.first()).toBeVisible();
    await expectNoSeriousViolations(page, testInfo, "palette");
  });

  test("command palette with no results", async ({ page }, testInfo) => {
    const shell = new ShellPage(page);
    await shell.goto("/");
    await shell.openPaletteWithShortcut();
    await shell.paletteInput.fill("zzzz");
    await expect(page.getByText("No pages match")).toBeVisible();
    await expectNoSeriousViolations(page, testInfo, "palette-empty");
  });

  test("mobile drawer open", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 844 });
    const shell = new ShellPage(page);
    await shell.goto("/");
    await shell.openDrawer();
    // Let the drawer finish sliding in: axe reads colours mid-fade as low contrast.
    await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== "running"));
    await expectNoSeriousViolations(page, testInfo, "drawer");
  });
});
