import { expect, test, type Page, type Route } from "@playwright/test";
import { ShellPage } from "../shell.page";
import registry from "./registry.json" with { type: "json" };

export const PROMPT_NAME = "Ask or do anything...";
export const THEMES = ["light", "dark"] as const;
export const GUARDRAIL_VIEWPORTS = [
  ["mobile", { width: 375, height: 812 }],
  ["desktop", { width: 1280, height: 800 }],
] as const;
export const FAULTS = ["503", "timeout", "kill-switch", "budget"] as const;
export type Fault = (typeof FAULTS)[number];

export const LE = "0195f0a0-0000-7000-8000-0000000000e1";
export const VENDOR = "0195f0a0-0000-7000-8000-0000000000e2";
export const LEDGER = "0195f0a0-0000-7000-8000-0000000000e3";
export const RECEIPT = "0195f0a0-0000-7000-8000-0000000000e4";
export const CHANGE_ID = "0195f0a0-0000-7000-8000-0000000000e5";
export const APPROVAL_ID = "6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b11";
const CONVERSATION_ID = "6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b12";
const BRIEFINGS = /\/api\/v1\/agents\/briefings(\?|$)/;

export const AVAILABLE = {
  killSwitch: { halted: false, stale: false, tenant: { active: false, version: null, changedAt: null, byUser: null }, global: { active: false, version: null, changedAt: null } },
  budgetStatus: "OK",
  copilotEnabled: true,
};

export const covers = (...ids: string[]): void => {
  const known = registry.outcomes.map((outcome) => outcome.id);
  if (ids.length === 0) throw new Error("covers needs at least one outcome id");
  for (const id of ids) {
    if (!known.includes(id)) throw new Error(`covers: ${id} is not an outcome in registry.json`);
    test.info().annotations.push({ type: "outcome", description: id });
  }
};

export const stubAvailability = (page: Page, availability: object = AVAILABLE) =>
  page.route("**/api/v1/agents/availability", (route) => route.fulfill({ json: availability }));

export async function stubFault(page: Page, fault: Fault): Promise<void> {
  if (fault === "kill-switch") {
    await stubAvailability(page, { ...AVAILABLE, killSwitch: { ...AVAILABLE.killSwitch, halted: true } });
    return;
  }
  if (fault === "budget") {
    await stubAvailability(page, { ...AVAILABLE, budgetStatus: "HARD" });
    return;
  }
  const down = (route: Route) => (fault === "503" ? route.fulfill({ status: 503, json: { code: "AGT-0202", detail: "The assistant is switched off." } }) : route.abort("timedout"));
  await page.route("**/api/v1/agents/**", down);
  await page.route("**/api/v1/copilot/**", down);
}

export const SEARCH_HIT = { type: "party", id: "v-1", number: "V-0007", title: "Acme Tooling", subtitle: "organization", status: "active", link: "/party/v-1" };

export const stubSearch = (page: Page) => page.route("**/api/v1/search?**", (route) => route.fulfill({ json: { results: [SEARCH_HIT] } }));

export const CHANGE_ITEM = {
  id: CHANGE_ID,
  source: "party",
  kind: "Customer change",
  reason: "Credit limit",
  number: "CHG-1",
  link: "/party/changes",
  actions: ["approve", "reject"],
  createdAt: "2026-11-10T10:00:00Z",
};

export async function stubInbox(page: Page, items: object[] = [CHANGE_ITEM]): Promise<void> {
  let current = items;
  await page.route("**/api/v1/inbox**", (route) => route.fulfill({ json: { items: current, unavailable: [], truncated: [] } }));
  await page.route("**/api/v1/party/pending-changes/*:approve", (route) => {
    current = [];
    return route.fulfill({ json: {} });
  });
}

export const approval = (actionId: string = APPROVAL_ID) => ({
  runId: "run-1",
  actionId,
  tool: "depot.receivables.payment.propose_apply",
  state: "PENDING",
  amount: { amount: "4200.00", currency: "USD" },
  requestedAt: "2026-11-19T10:00:00.000Z",
  expiresAt: "2026-11-20T10:00:00.000Z",
});

export async function stubApprovals(page: Page, actionId: string = APPROVAL_ID): Promise<void> {
  await page.route(/\/api\/v1\/agents\/approvals\?/, (route) => route.fulfill({ json: { items: [{ ...approval(actionId), agentId: "cash_app" }] } }));
  await page.route(new RegExp(`/api/v1/agents/approvals/${actionId}$`), (route) => route.fulfill({ json: approval(actionId) }));
}

export const BRIEFING = {
  id: "0b9a6b5e-2f4c-4f6e-9c1d-3d2b1a0f9e88",
  status: "READY",
  narrative: true,
  createdAt: "2026-11-19T10:00:00.000Z",
  completedAt: "2026-11-19T10:00:05.000Z",
  readAt: null,
  changes: [{ agentId: "ap_coding", factId: "f0", text: "3 invoices were coded" }],
  risks: [{ agentId: "anomaly", factId: "f1", text: "One journal looks unusual" }],
  recommendedActions: [{ agentId: "cash_app", factId: "f2", text: "Apply the 4,200.00 USD payment from Acme", actionId: APPROVAL_ID, route: `/assistant/proposals/${APPROVAL_ID}` }],
  notes: [],
};

export const stubBriefings = (page: Page, items: object[] = []) =>
  page.route(BRIEFINGS, (route) => route.fulfill({ json: { items, nextCursor: null, nextScheduledAt: null } }));

export const ANSWER = {
  answer: "Overdue invoices total 48,200.00 USD [[cite:record:ledger/ap-invoice/INV-1042]].",
  citations: [{ sourceType: "RECORD", sourceRef: "record:ledger/ap-invoice/INV-1042", label: "Invoice INV-1042" }],
  toolCalls: [{ tool: "analytics.query", status: "OK" }],
  refusal: null,
  generated: true,
};

export async function stubAsk(page: Page, done: unknown = ANSWER): Promise<void> {
  await page.route("**/api/v1/copilot/chat", (route) => route.fulfill({ json: { kind: "ask", confidence: 0.95 } }));
  await page.route("**/api/v1/agents/copilot/conversations", (route) => route.fulfill({ status: 201, json: { conversationId: CONVERSATION_ID } }));
  await page.route(`**/api/v1/agents/copilot/conversations/${CONVERSATION_ID}/messages`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      body: `event: token\ndata: ${JSON.stringify({ text: "Overdue invoices " })}\n\nevent: done\ndata: ${JSON.stringify(done)}\n\n`,
    }),
  );
}

export async function ask(page: Page, question: string): Promise<void> {
  await new ShellPage(page).goto("/");
  await page.getByRole("textbox", { name: PROMPT_NAME }).fill(question);
  await page.keyboard.press("Enter");
}

export const PO = "0195f0a0-0000-7000-8000-000000000071";

export async function stubRecordAssist(page: Page): Promise<void> {
  await stubAvailability(page);
  await stubAsk(page, { ...ANSWER, toolCalls: ["depot.platform.audit_trail.get", "depot.platform.document_flow.get", "depot.workflow.history.get"].map((tool) => ({ tool, status: "OK" })) });
  await page.route("**/api/v1/party/**", (route) => route.fulfill({ status: 404, json: { code: "COM-0404", detail: "none" } }));
  await page.route("**/api/v1/receiving/**", (route) => route.fulfill({ json: { items: [] } }));
  await page.route("**/api/v1/procurement/purchase-orders/*/**", (route) => route.fulfill({ json: { items: [] } }));
  await page.route(`**/api/v1/procurement/purchase-orders/${PO}`, (route) =>
    route.fulfill({
      json: { id: PO, number: "PO-1042", legalEntityId: LE, vendorId: VENDOR, currency: "USD", approvalStatus: "CONFIRMED", documentStatus: "OPEN", versionNo: 0, changeManagement: false, subtotal: "0", discountTotal: "0", chargesTotal: "0", taxTotal: "0", total: "0", rowVersion: 1, lines: [], charges: [], createdAt: "2026-11-10T10:00:00Z", updatedAt: "2026-11-10T10:00:00Z" },
      headers: { ETag: '"1"' },
    }),
  );
}

const PROFILE = {
  id: "0195f0a0-0000-7000-8000-000000000001",
  personnelNumber: "W-1",
  workerType: "EMPLOYEE",
  partyPersonId: "0195f0a0-0000-7000-8000-000000000001",
  legalName: "Rita Ray",
  createdAt: "2026-01-01T00:00:00Z",
  updatedAt: "2026-01-01T00:00:00Z",
  pii: { nationalId: "***-**-6789", masked: true },
  asOf: { workerId: "0195f0a0-0000-7000-8000-000000000001", date: "2026-11-05", employments: [], positionId: null, positionNumber: "P-1", fteShare: "1.00", managerWorkerId: null },
};

export interface CoreApi {
  created: unknown[];
  posted: unknown[];
}

export async function stubCoreApi(page: Page): Promise<CoreApi> {
  const core: CoreApi = { created: [], posted: [] };
  await stubSearch(page);
  await stubInbox(page);
  await page.route("**/api/v1/hr/**", (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace("/api/v1/hr", "");
    if (path === "/me") return route.fulfill({ json: PROFILE });
    if (path === "/me/profile-changes" && request.method() === "POST") {
      const body = request.postDataJSON() as { preferredName: string };
      core.created.push(body);
      return route.fulfill({
        status: 201,
        json: { id: "0195f0a0-0000-7000-8000-000000000031", workerId: PROFILE.id, workerName: "Rita Ray", personnelNumber: "W-1", kind: "PREFERRED_NAME", status: "APPLIED", summary: body.preferredName, requestedBy: "u1", createdAt: "2026-11-05T09:00:00Z" },
      });
    }
    return route.fulfill({ json: { items: [] } });
  });
  await page.route("**/api/v1/org/legal-entities**", (route) => route.fulfill({ json: { items: [{ id: LE, name: "Acme Inc", code: "ACME" }] } }));
  await page.route("**/api/v1/party/vendors:search**", (route) => route.fulfill({ json: { items: [{ id: VENDOR, name: "Globex", accountNumber: "V-1", legalEntityId: LE }] } }));
  await page.route("**/api/v1/ledger/ledgers**", (route) => route.fulfill({ json: { items: [{ id: LEDGER, name: "Main ledger", code: "GL", currency: "USD", legalEntityId: LE }] } }));
  await page.route("**/api/v1/receiving/receipts**", (route) => route.fulfill({ json: { items: [{ id: RECEIPT, receiptNumber: "RCV-1", legalEntityId: LE, currency: "USD" }] } }));
  await page.route("**/api/v1/receiving/invoices:post", (route) => {
    core.posted.push(route.request().postDataJSON());
    return route.fulfill({
      status: 201,
      json: {
        id: "inv-1",
        status: "POSTED",
        vendorId: VENDOR,
        legalEntityId: LE,
        invoiceNumber: "INV-100",
        invoiceDate: "2026-11-10",
        postingDate: "2026-11-10",
        currency: "USD",
        ledgerId: LEDGER,
        fiscalPeriodId: "fp-1",
        total: "15.00",
        ledgerEntryId: "le-1",
        createdAt: "2026-11-10T10:00:00Z",
        lines: [{ lineNo: 1, receiptId: RECEIPT, receiptLineNo: 1, qty: "6", unitPrice: "2.5", amount: "15.00", standardCost: false }],
      },
    });
  });
  return core;
}

export async function pick(page: Page, name: string, option: string): Promise<void> {
  await page.getByRole("combobox", { name }).click();
  await page.getByRole("option", { name: option }).click();
}

export async function postInvoice(page: Page): Promise<void> {
  await new ShellPage(page).goto("/ap-invoices/post");
  await pick(page, "Legal entity", "Acme Inc");
  await pick(page, "Vendor", "Globex");
  await page.getByLabel("Invoice number").fill("INV-100");
  await page.getByLabel("Invoice date").fill("2026-11-10");
  await page.getByLabel("Posting date").fill("2026-11-10");
  await page.getByLabel("Currency").fill("USD");
  await pick(page, "Ledger", "Main ledger");
  await pick(page, "Line 1 receipt", "RCV-1");
  await page.getByLabel("Line 1 quantity").fill("6");
  await page.getByLabel("Line 1 unit price").fill("2.5");
  await page.getByRole("button", { name: "Post invoice" }).click();
  await expect(page.getByText("Invoice INV-100 posted")).toBeVisible();
}
