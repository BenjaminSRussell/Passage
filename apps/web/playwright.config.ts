import { defineConfig, devices } from "@playwright/test";

// No @types/node in this package; declare only what the config reads.
declare const process: { env: Record<string, string | undefined> };

const PORT = 4173;

const E2E_DUTIES = [
  "audit.read",
  "batch.read",
  "engineering.read",
  "identity.legal-entity.maintain",
  "identity.security-role.maintain",
  "tax.code.maintain",
  "metadata.read",
  "notify.write",
  "numbering.write",
  "period.check",
  "product.read",
  "product.released.write",
  "production.order.read",
  "settlement.read",
  "shopfloor.kiosk.login",
  "shopfloor.view",
  "tenant.administer",
  "identity.effective-permission.view",
  "workflow.approve",
  "workflow.read",
  "workflow.write",
  "warehouse.mobile-flow.admin",
].join(",");

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: [["list"], ["html", { open: "never" }]],
  use: {
    baseURL: `http://localhost:${PORT}`,
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    reducedMotion: "reduce",
  },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] }, testIgnore: /warehouse-mobile/ },
    { name: "warehouse-mobile", use: { ...devices["Pixel 5"] }, testMatch: /warehouse-mobile/ },
  ],
  webServer: {
    command: `npx vite --port ${PORT} --strictPort`,
    url: `http://localhost:${PORT}`,
    env: { PASSAGE_DEV_DUTIES: E2E_DUTIES, PASSAGE_DEV_SHOW_ALL_NAV: "", PASSAGE_MOBILE_STUB: "1" },
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
  },
});
