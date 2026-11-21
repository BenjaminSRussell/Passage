import type { Page } from "@playwright/test";
import { expect, test, type WriteGuard } from "../_shared/writeGuard";
import { ShellPage } from "../shell.page";
import { DEV_IDENTITY } from "./personas";
import registry from "./registry.json" with { type: "json" };
import { ask, APPROVAL_ID, BRIEFING, covers, PO, PROMPT_NAME, stubApprovals, stubAsk, stubAvailability, stubBriefings, stubInbox, stubRecordAssist, stubSearch } from "./support";

interface Scenario {
  fresh?: true;
  run(page: Page, writeGuard: WriteGuard): Promise<void>;
}

const KILL_SWITCH = { tenantId: "t-1", halted: false, stale: false, tenant: { active: false, version: null, changedAt: null, byUser: null }, global: { active: false, version: null, changedAt: null } };
const PINS = [{ kind: "page", route: "/copilot/tools", at: "2026-11-10T10:00:00.000Z" }];

const SCENARIOS: Record<string, Scenario> = {
  ask: {
    run: async (page, writeGuard) => {
      await stubAsk(page);
      await ask(page, "what is the total of our overdue invoices?");
      await expect(page.getByRole("article", { name: "Copilot answer" })).toContainText("Overdue invoices total");
      writeGuard.expectNoWrites();
    },
  },
  navigate: {
    run: async (page, writeGuard) => {
      await stubAsk(page);
      await page.route("**/api/v1/copilot/chat", (route) => route.fulfill({ json: { kind: "navigate", confidence: 0.9 } }));
      await ask(page, "take me to the glossary");
      await expect(page.getByRole("article", { name: "Copilot answer" })).toBeVisible();
      writeGuard.expectNoWrites();
    },
  },
  "attention-feed": {
    run: async (page, writeGuard) => {
      await stubInbox(page);
      await new ShellPage(page).goto("/");
      await expect(page.getByRole("link", { name: "Review Customer change CHG-1" })).toBeVisible();
      writeGuard.expectNoWrites();
      await new ShellPage(page).goto("/workflow/inbox");
      await page.getByRole("button", { name: "Approve Customer change CHG-1" }).click();
      await writeGuard.expectWritesOnlyAfterApproval(page.getByRole("button", { name: "Confirm approval" }));
      await expect(page.getByText("Approved: CHG-1.")).toBeVisible();
    },
  },
  "act-proposal-view": {
    run: async (page, writeGuard) => {
      await stubBriefings(page, [BRIEFING]);
      await stubApprovals(page);
      await new ShellPage(page).goto("/");
      await page.getByRole("link", { name: "Apply the 4,200.00 USD payment from Acme" }).click();
      await expect(page).toHaveURL(new RegExp(`/assistant/proposals/${APPROVAL_ID}$`));
      await expect(page.getByText("Proposed action")).toBeVisible();
      await expect(page.getByRole("button", { name: /^(approve|decline)/i })).toHaveCount(0);
      writeGuard.expectNoWrites();
    },
  },
  "contextual-help": {
    run: async (page, writeGuard) => {
      await stubRecordAssist(page);
      await new ShellPage(page).goto(`/purchasing/purchase-orders/${PO}`);
      await page.getByRole("button", { name: "Ask about this record" }).click();
      const dialog = page.getByRole("dialog", { name: "Ask about this purchase order" });
      await dialog.getByRole("button", { name: "Explain this record" }).click();
      await expect(dialog.getByRole("article", { name: "Copilot answer" })).toBeVisible();
      writeGuard.expectNoWrites();
    },
  },
  briefing: {
    run: async (page, writeGuard) => {
      await stubBriefings(page, [BRIEFING]);
      await new ShellPage(page).goto("/");
      await page.getByRole("button", { name: "Risks (1)" }).click();
      await expect(page.getByText("One journal looks unusual")).toBeVisible();
      writeGuard.expectNoWrites();
    },
  },
  "command-bar": {
    run: async (page, writeGuard) => {
      await stubSearch(page);
      const shell = new ShellPage(page);
      await shell.goto("/");
      await shell.openPaletteWithShortcut();
      await shell.paletteInput.fill("acme");
      await shell.palette.getByRole("option", { name: /Party V-0007/ }).click();
      await expect(page).toHaveURL(/\/party\/v-1$/);
      writeGuard.expectNoWrites();
    },
  },
  pins: {
    run: async (page, writeGuard) => {
      await page.addInitScript(([key, value]) => window.localStorage.setItem(key, value), [`depot.pins.v1.${DEV_IDENTITY}`, JSON.stringify(PINS)] as const);
      await new ShellPage(page).goto("/");
      await page.getByRole("region", { name: "Pinned" }).getByRole("link", { name: "Tool catalog" }).click();
      await expect(page).toHaveURL(/\/copilot\/tools$/);
      writeGuard.expectNoWrites();
    },
  },
  "personalization-reset": {
    run: async (page, writeGuard) => {
      await page.addInitScript(([key, value]) => window.localStorage.setItem(key, value), [`depot.activity.v1.${DEV_IDENTITY}`, JSON.stringify(PINS)] as const);
      await new ShellPage(page).goto("/");
      await page.getByRole("button", { name: "Reset personalization" }).click();
      await page.getByRole("alertdialog").getByRole("button", { name: "Keep it" }).click();
      await page.getByRole("button", { name: "Reset personalization" }).click();
      await page.getByRole("alertdialog").getByRole("button", { name: "Reset", exact: true }).click();
      await expect(page.getByRole("status").filter({ hasText: "Personalization reset" })).toBeVisible();
      writeGuard.expectNoWrites();
    },
  },
  onboarding: {
    fresh: true,
    run: async (page, writeGuard) => {
      await new ShellPage(page).goto("/");
      await page.getByRole("button", { name: "Skip tour" }).click();
      await expect(page.getByRole("textbox", { name: PROMPT_NAME })).toBeVisible();
      writeGuard.expectNoWrites();
    },
  },
  "admin-controls": {
    run: async (page, writeGuard) => {
      await page.route("**/api/v1/agents/kill-switch", (route) => route.fulfill({ json: KILL_SWITCH }));
      await new ShellPage(page).goto("/copilot/kill-switch");
      await expect(page.getByRole("heading", { name: "Your tenant" })).toBeVisible();
      writeGuard.expectNoWrites();
    },
  },
};

const present = registry.paths.filter((path) => path.status === "present").map((path) => path.id);
const pending = registry.paths.filter((path) => path.status === "pending");

test.describe("write-guard sweep consistency", () => {
  test("every path listed as present has a scenario", () => {
    expect(present.filter((id) => !(id in SCENARIOS))).toEqual([]);
  });

  test("every scenario names a path listed as present", () => {
    expect(Object.keys(SCENARIOS).filter((id) => !present.includes(id))).toEqual([]);
  });

  test("every pending path names its ticket", () => {
    expect(pending.filter((path) => !("ticket" in path) || !path.ticket)).toEqual([]);
  });
});

for (const id of present) {
  const scenario = SCENARIOS[id];
  if (scenario === undefined) continue;
  test.describe(`write guard on ${id}`, () => {
    if (scenario.fresh) test.use({ storageState: { cookies: [], origins: [] } });

    test("writes nothing before an explicit approval", async ({ page, writeGuard }) => {
      covers(...(registry.paths.find((path) => path.id === id)?.outcomes ?? []));
      await stubAvailability(page);
      await stubBriefings(page);
      await scenario.run(page, writeGuard);
    });
  });
}
