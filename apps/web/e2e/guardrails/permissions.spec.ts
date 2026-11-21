import { expect, test, type Page } from "@playwright/test";
import { ShellPage } from "../shell.page";
import { holds, type PersonaName } from "./personas";
import { APPROVAL_ID, ask, BRIEFING, covers, stubApprovals, stubAsk, stubAvailability, stubBriefings, stubInbox, stubSearch } from "./support";

const NOT_AVAILABLE = "This page isn't available to you";
const MESSAGE = { id: "msg-1", status: "open", version: 1, action: "delay", itemId: "ITEM-1", supplyRef: "PO-PLAN-77", delayDays: 2, qty: "5", text: "Move PO-PLAN-77 out" };

const FEED_SOURCES = [
  { duty: "agents.run.read", request: /\/api\/v1\/agents\/approvals\?/, link: /6f1c1f0e/ },
  { duty: "inbox.read", request: /\/api\/v1\/inbox/, link: /CHG-1/ },
  { duty: "planning.read", request: /\/api\/v1\/planning\/messages\?/, link: /PO-PLAN-77/ },
];

const persona = (): PersonaName => test.info().project.metadata.persona as PersonaName;

async function arrange(page: Page) {
  const requested: string[] = [];
  page.on("request", (request) => requested.push(request.url()));
  await stubAvailability(page);
  await stubBriefings(page, [BRIEFING]);
  await stubInbox(page);
  await stubApprovals(page);
  await stubSearch(page);
  await page.route(/\/api\/v1\/planning\/messages\?/, (route) => route.fulfill({ json: { items: [MESSAGE] } }));
  return (pattern: RegExp) => requested.some((url) => pattern.test(url));
}

test.describe("duty profile", () => {
  test("the home feed and briefing call and show only sources the profile holds", async ({ page }) => {
    covers("O2", "O7", "O9");
    const requestedFrom = await arrange(page);
    await new ShellPage(page).goto("/");
    await page.waitForLoadState("networkidle");
    for (const source of FEED_SOURCES) {
      const allowed = holds(persona(), source.duty);
      expect(requestedFrom(source.request), `${source.duty} request`).toBe(allowed);
      await expect(page.getByRole("link", { name: source.link }), `${source.duty} item`).toHaveCount(allowed ? 1 : 0);
    }
    const briefingAllowed = holds(persona(), "agents.run.read");
    expect(requestedFrom(/\/api\/v1\/agents\/briefings/)).toBe(briefingAllowed);
    await expect(page.getByRole("heading", { level: 2, name: "Your briefing" })).toHaveCount(briefingAllowed ? 1 : 0);
  });

  test("offers Do mode only with the agents.invoke duty", async ({ page }) => {
    covers("O4");
    await arrange(page);
    await new ShellPage(page).goto("/");
    const act = page.getByRole("group", { name: "What should the assistant do?" }).getByRole("radio", { name: "Do" });
    if (holds(persona(), "agents.invoke")) await expect(act).toBeEnabled();
    else await expect(act).toBeDisabled();
  });

  test("links an answer's citation only when the profile may open the record", async ({ page }) => {
    covers("O3");
    await arrange(page);
    await stubAsk(page);
    await ask(page, "overdue invoices");
    const answer = page.getByRole("article", { name: "Copilot answer" });
    await expect(answer).toContainText("Invoice INV-1042");
    await expect(answer.locator('a[href*="/ap-invoices/"]')).toHaveCount(holds(persona(), "receiving.ap.inquire") ? 1 : 0);
  });

  test("keeps admin pages out of the command bar", async ({ page }) => {
    covers("O5", "O8");
    await arrange(page);
    const shell = new ShellPage(page);
    await shell.goto("/");
    await shell.openPaletteWithShortcut();
    for (const [query, label, duty] of [
      ["kill switch", /Agent kill switch/, "agents.admin"],
      ["ai controls", /AI controls/, "agentbudget.read"],
    ] as const) {
      await shell.paletteInput.fill(query);
      await expect(shell.palette.getByRole("option", { name: new RegExp(`Ask the assistant: ${query}`) })).toBeVisible();
      await expect(shell.palette.getByRole("option", { name: label })).toHaveCount(holds(persona(), duty) ? 1 : 0);
    }
  });

  test("opens a proposal only with the agents.run.read duty", async ({ page }) => {
    covers("O4");
    const requestedFrom = await arrange(page);
    await page.goto(`/assistant/proposals/${APPROVAL_ID}`);
    const allowed = holds(persona(), "agents.run.read");
    if (allowed) await expect(page.getByText("Proposed action")).toBeVisible();
    else await expect(page.getByText(NOT_AVAILABLE)).toBeVisible();
    await page.waitForLoadState("networkidle");
    expect(requestedFrom(new RegExp(`/api/v1/agents/approvals/${APPROVAL_ID}$`))).toBe(allowed);
  });

  test("keeps the kill switch and budget pages behind their duties", async ({ page }) => {
    covers("O8");
    const requestedFrom = await arrange(page);
    for (const [path, duty, request] of [
      ["/copilot/kill-switch", "agents.admin", /\/api\/v1\/agents\/kill-switch/],
      ["/copilot/budgets", "agentbudget.read", /\/api\/v1\/agents\/budgets/],
    ] as const) {
      await page.goto(path);
      const allowed = holds(persona(), duty);
      if (!allowed) await expect(page.getByText(NOT_AVAILABLE)).toBeVisible();
      await page.waitForLoadState("networkidle");
      expect(requestedFrom(request), path).toBe(allowed);
    }
  });
});
