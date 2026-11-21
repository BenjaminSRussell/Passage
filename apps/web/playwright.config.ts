import { defineConfig, devices } from "@playwright/test";
import { DEV_IDENTITY, OPERATOR, PERSONAS, personaNames } from "./e2e/guardrails/personas";

// No @types/node in this package; declare only what the config reads.
declare const process: { env: Record<string, string | undefined>; argv: string[] };

// Workers re-read this file; the environment, unlike argv, reaches them.
const GUARDRAILS = process.env.DEPOT_GUARDRAILS === "1" || process.argv.some((arg) => arg.includes("guardrails"));
if (GUARDRAILS) process.env.DEPOT_GUARDRAILS = "1";

const PORT = 4173;
const DEMO_PORT = 4174;

const E2E_DUTIES = [
  "agents.run.read",
  "audit.read",
  "batch.read",
  "engineering.read",
  "hr.profile.self",
  "hr.team.approve",
  "identity.feature.maintain",
  "identity.legal-entity.maintain",
  "identity.security-role.maintain",
  "identity.setup.maintain",
  "inbox.read",
  "mcp.copilot.use",
  "tax.code.maintain",
  "metadata.read",
  "notify.write",
  "numbering.read",
  "numbering.write",
  "open-item.read",
  "party.approve",
  "sales.order.read",
  "sales.order.write",
  "fulfillment.warehouse.view",
  "billing.invoice.view",
  "billing.invoice.maintain",
  "receivables.view",
  "print.document.render",
  "receiving.document-flow.read",
  "procurement.purchase-order.inquire",
  "receiving.receipt.maintain",
  "receiving.ap.inquire",
  "receiving.vendor-invoice.post",
  "payment-proposal.read",
  "payment-proposal.approve",
  "payment-run.execute",
  "payment-run.read",
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

const onboardingCompletedOn = (port: number) => ({
  cookies: [],
  origins: [{ origin: `http://localhost:${port}`, localStorage: [{ name: `depot.onboarding.v1.${DEV_IDENTITY}`, value: "completed" }] }],
});
const ONBOARDING_COMPLETED = onboardingCompletedOn(PORT);

const guardrailServer = (port: number, duties: string) => ({
  command: `npx vite --port ${port} --strictPort`,
  url: `http://localhost:${port}`,
  env: { DEPOT_DEV_DUTIES: duties, DEPOT_DEV_SHOW_ALL_NAV: "" },
  reuseExistingServer: !process.env.CI,
  timeout: 60_000,
});

const guardrailProjects = [
  {
    name: "guardrails",
    use: { ...devices["Desktop Chrome"], baseURL: `http://localhost:${OPERATOR.port}`, storageState: onboardingCompletedOn(OPERATOR.port) },
    testMatch: /guardrails\/.*\.spec\.ts/,
    testIgnore: /guardrails\/permissions\.spec/,
    retries: 0,
  },
  ...personaNames.map((persona) => ({
    name: `guardrails-${persona}`,
    use: { ...devices["Desktop Chrome"], baseURL: `http://localhost:${PERSONAS[persona].port}`, storageState: onboardingCompletedOn(PERSONAS[persona].port) },
    testMatch: /guardrails\/permissions\.spec/,
    metadata: { persona },
    retries: 0,
  })),
];

const guardrailServers = [
  guardrailServer(OPERATOR.port, [E2E_DUTIES, ...OPERATOR.extraDuties].join(",")),
  ...personaNames.map((persona) => guardrailServer(PERSONAS[persona].port, PERSONAS[persona].duties.join(","))),
];

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: [["list"], ["html", { open: "never" }], ...(GUARDRAILS ? [["json", { outputFile: "test-results/guardrails/results.json" }] as const] : [])],
  use: {
    baseURL: `http://localhost:${PORT}`,
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    reducedMotion: "reduce",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"], storageState: ONBOARDING_COMPLETED },
      testIgnore: [/warehouse-mobile/, /\.demo\.spec/, /guardrails\//],
    },
    // Procure-to-pay and order-to-cash flows run against the demo build: a fake backend and every duty held.
    { name: "demo-flows", use: { ...devices["Desktop Chrome"], baseURL: `http://localhost:${DEMO_PORT}/Depot/` }, testMatch: /\.demo\.spec/ },
    {
      name: "demo-phone",
      use: { ...devices["Desktop Chrome"], viewport: { width: 390, height: 844 }, baseURL: `http://localhost:${DEMO_PORT}/Depot/` },
      testMatch: /a11y-routes\.demo\.spec/,
    },
    { name: "warehouse-mobile", use: { ...devices["Pixel 5"] }, testMatch: /warehouse-mobile/ },
    ...(GUARDRAILS ? guardrailProjects : []),
  ],
  webServer: [
    {
      command: `npx vite --port ${PORT} --strictPort`,
      url: `http://localhost:${PORT}`,
      env: { DEPOT_DEV_DUTIES: E2E_DUTIES, DEPOT_DEV_SHOW_ALL_NAV: "", DEPOT_MOBILE_STUB: "1" },
      reuseExistingServer: !process.env.CI,
      timeout: 60_000,
    },
    {
      command: `npx vite --mode demo --port ${DEMO_PORT} --strictPort`,
      url: `http://localhost:${DEMO_PORT}/Depot/`,
      reuseExistingServer: !process.env.CI,
      timeout: 60_000,
    },
    ...(GUARDRAILS ? guardrailServers : []),
  ],
});
