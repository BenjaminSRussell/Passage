import { expect, test, type Page } from "@playwright/test";
import { scanRoute } from "../axe";
import allowlist from "../axeAllowlist.json" with { type: "json" };
import { ShellPage } from "../shell.page";
import { DEV_IDENTITY } from "./personas";
import { ANSWER, APPROVAL_ID, ask, BRIEFING, covers, GUARDRAIL_VIEWPORTS, PROMPT_NAME, stubApprovals, stubAsk, stubAvailability, stubBriefings, stubInbox, stubSearch, THEMES } from "./support";

const KILL_SWITCH = { tenantId: "t-1", halted: false, stale: false, tenant: { active: false, version: null, changedAt: null, byUser: null }, global: { active: false, version: null, changedAt: null } };
const USAGE = { tokensIn: 100, tokensOut: 50 };
const BUDGET = { cap: { tokensIn: 1000, tokensOut: 500 }, tokensUsed: USAGE, costUsed: "0.001200", percentConsumed: 10, status: "OK" };
const CELLS = { P0: true, P1: true, P2: false, P3: false, CUI: false };
const PINS = [
  { kind: "page", route: "/copilot/tools", at: "2026-11-10T10:00:00.000Z" },
  { kind: "page", route: "/copilot", at: "2026-11-10T10:00:01.000Z" },
];

interface Surface {
  outcomes: string[];
  fresh?: true;
  open(page: Page): Promise<void>;
}

const SURFACES: Record<string, Surface> = {
  home: {
    outcomes: ["O1"],
    open: async (page) => {
      await stubInbox(page, []);
      await new ShellPage(page).goto("/");
      await expect(page.getByRole("textbox", { name: PROMPT_NAME })).toBeVisible();
    },
  },
  "home-feed": {
    outcomes: ["O2"],
    open: async (page) => {
      await stubInbox(page);
      await new ShellPage(page).goto("/");
      await expect(page.getByRole("link", { name: "Review Customer change CHG-1" })).toBeVisible();
    },
  },
  "command-bar": {
    outcomes: ["O5"],
    open: async (page) => {
      await stubSearch(page);
      const shell = new ShellPage(page);
      await shell.goto("/");
      await shell.openPaletteWithShortcut();
      await shell.paletteInput.fill("acme");
      await expect(shell.palette.getByRole("option", { name: /Party V-0007/ })).toBeVisible();
    },
  },
  "answer-panel": {
    outcomes: ["O3"],
    open: async (page) => {
      await stubAsk(page, ANSWER);
      await ask(page, "overdue invoices");
      await expect(page.getByRole("article", { name: "Copilot answer" })).toBeVisible();
    },
  },
  "proposal-card": {
    outcomes: ["O4"],
    open: async (page) => {
      await stubApprovals(page);
      await new ShellPage(page).goto(`/assistant/proposals/${APPROVAL_ID}`);
      await expect(page.getByText("Proposed action")).toBeVisible();
    },
  },
  briefing: {
    outcomes: ["O7"],
    open: async (page) => {
      await stubBriefings(page, [BRIEFING]);
      await new ShellPage(page).goto("/");
      await expect(page.getByRole("heading", { level: 2, name: "Your briefing" })).toBeVisible();
    },
  },
  pins: {
    outcomes: ["O9"],
    open: async (page) => {
      await page.addInitScript(([key, value]) => window.localStorage.setItem(key, value), [`depot.pins.v1.${DEV_IDENTITY}`, JSON.stringify(PINS)] as const);
      await new ShellPage(page).goto("/");
      await expect(page.getByRole("region", { name: "Pinned" }).getByRole("link", { name: "Tool catalog" })).toBeVisible();
    },
  },
  onboarding: {
    outcomes: ["O10"],
    fresh: true,
    open: async (page) => {
      await new ShellPage(page).goto("/");
      await expect(page.getByText("Step 1 of 5", { exact: true })).toBeVisible();
    },
  },
  "admin-kill-switch": {
    outcomes: ["O8"],
    open: async (page) => {
      await page.route("**/api/v1/agents/kill-switch", (route) => route.fulfill({ json: KILL_SWITCH }));
      await new ShellPage(page).goto("/copilot/kill-switch");
      await expect(page.getByRole("heading", { name: "Your tenant" })).toBeVisible();
    },
  },
  "admin-budgets": {
    outcomes: ["O8"],
    open: async (page) => {
      await page.route("**/api/v1/agents/budgets", (route) =>
        route.fulfill({ json: { month: "2026-11", tenant: { ...BUDGET, scope: "TENANT", agentId: null }, agents: [{ ...BUDGET, scope: "AGENT", agentId: "briefing" }] } }),
      );
      await page.route("**/api/v1/agents/ai-data-policy", (route) => route.fulfill({ json: { version: 1, modules: { ap: CELLS, ar: CELLS, platform: CELLS } } }));
      await new ShellPage(page).goto("/copilot/budgets");
      await expect(page.getByRole("table")).toBeVisible();
    },
  },
};

test("the axe allowlist holds no row for a guardrail surface", () => {
  expect(allowlist.filter((row) => row.route.startsWith("guardrail-"))).toEqual([]);
});

for (const [name, surface] of Object.entries(SURFACES)) {
  for (const colorScheme of THEMES) {
    for (const [label, viewport] of GUARDRAIL_VIEWPORTS) {
      test.describe(`${name}, ${colorScheme}, ${label}`, () => {
        test.use({ colorScheme, viewport });
        if (surface.fresh) test.use({ storageState: { cookies: [], origins: [] } });

        test("has no axe violations", async ({ page }, testInfo) => {
          covers(...surface.outcomes);
          await stubAvailability(page);
          await stubBriefings(page);
          await surface.open(page);
          await scanRoute(page, testInfo, `guardrail-${name}`);
        });
      });
    }
  }
}
