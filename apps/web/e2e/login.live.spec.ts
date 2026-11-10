import { expect, test, type Page } from "@playwright/test";

// No @types/node in this package; declare only what the spec reads.
declare const process: { env: Record<string, string | undefined> };

const LIVE_URL = process.env.PASSAGE_E2E_LIVE_URL;

test.skip(!LIVE_URL, "needs the stack from `make dev`: set PASSAGE_E2E_LIVE_URL=http://localhost:5173");

async function signIn(page: Page, username: string, password: string) {
  await page.goto(`${LIVE_URL}/`);
  await page.locator("#username").fill(username);
  await page.locator("#password").fill(password);
  await page.locator("#kc-login").click();
  await page.waitForURL(`${LIVE_URL}/`);
}

async function apiStatus(page: Page, path: string): Promise<number> {
  return page.evaluate(async (url) => (await fetch(url)).status, path);
}

test("an admin signs in through Keycloak, sees admin navigation and may call admin routes", async ({ page }) => {
  await signIn(page, "admin", "passage-admin-dev");
  // The workspace navigation lists the admin entries once the admin area is open.
  await page.goto(`${LIVE_URL}/admin`);
  await expect(page.getByRole("link", { name: "Audit" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Sequences" })).toBeVisible();
  expect([401, 403]).not.toContain(await apiStatus(page, "/api/v1/numbering/sequences/PARTY"));
});

test("a clerk signs in, sees no admin navigation and is refused by the API with 403", async ({ page }) => {
  await signIn(page, "clerk", "passage-clerk-dev");
  await page.goto(`${LIVE_URL}/admin`);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await expect(page.getByRole("link", { name: "Audit" })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "Sequences" })).toHaveCount(0);
  expect(await apiStatus(page, "/api/v1/numbering/sequences/PARTY")).toBe(403);
});
