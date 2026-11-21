import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { loadModules, type DepotModule } from "../../modules/registry";
import { AVAILABILITY_PATH } from "../assistant/availability";
import { copy } from "../copy";
import { activityKey } from "../personal/activityStore";
import { recentKey } from "../recent";
import { Shell } from "../Shell";
import { availableAssistant, json, pageViolations, server, useFakeServer } from "../testing/fakeServer";
import { completedOnboardingStorage } from "../testing/onboardingFixtures";
import type { WorkspaceConfig } from "../workspaces/model";
import { HomePage } from "./HomePage";

useFakeServer();

const IDENTITY = "ann";
const COPILOT_DUTY = "mcp.copilot.use";
const INVOICE_DUTY = "billing.invoice.read";
const REPORT_DUTY = "ledger.report.read";
const AT = "2026-11-19T10:00:00.000Z";

const modules: DepotModule[] = [
  {
    id: "ledger",
    title: "General ledger",
    routes: [{ path: "/ledger/transactions", element: <div>Transactions page</div> }],
    nav: [
      { label: "Journal batches", to: "/ledger/journal-batches" },
      { label: "Open items", to: "/ledger/open-items" },
      { label: "Transactions", to: "/ledger/transactions" },
      { label: "Reports", to: "/ledger/reports", duty: REPORT_DUTY },
    ],
  },
  { id: "billing", title: "Billing", routes: [{ path: "/billing/invoices/:id", element: <div>Invoice</div>, handle: { title: "Invoice", duty: INVOICE_DUTY } }], nav: [] },
];

const workspace = (id: string, entry: string) => ({ id, label: id, persona: id, description: `${id} work`, entries: [entry] });
const workspaceConfig: WorkspaceConfig = {
  areas: [],
  workspaces: [
    workspace("Alpha", "/ledger/journal-batches"),
    workspace("Beta", "/ledger/open-items"),
    workspace("Gamma", "/ledger/transactions"),
    workspace("Secret", "/ledger/reports"),
  ],
};

const page = (route: string, at = AT) => ({ kind: "page", route, at });
const invoice = { kind: "record", recordType: "customer_invoice", id: "inv-1", at: AT };
const seed = (rows: unknown) => localStorage.setItem(activityKey(IDENTITY), typeof rows === "string" ? rows : JSON.stringify(rows));
const transactionsActivity = [page("/ledger/transactions"), page("/ledger/transactions", "2026-11-18T10:00:00.000Z"), invoice];

function renderHome(duties: string[], options: { shellModules?: DepotModule[]; config?: WorkspaceConfig | null; entry?: string } = {}) {
  const { shellModules = modules, config = workspaceConfig, entry = "/" } = options;
  const inner = server.handler;
  server.handler = (url, init) => (url.pathname === AVAILABILITY_PATH ? json(200, availableAssistant) : inner(url, init));
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route
          element={<Shell modules={shellModules} duties={duties} identity={IDENTITY} workspaceConfig={config ?? undefined} onboardingStorage={completedOnboardingStorage(IDENTITY)} />}
        >
          <Route path="/" element={<HomePage />} />
          <Route path="/ledger/transactions" element={<div>Transactions page</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

const main = () => within(screen.getByRole("main"));
const askBox = () => main().findByRole("textbox", { name: copy.home.assistantLabel });
const suggestions = async () => {
  await askBox();
  return within(main().getByRole("group", { name: copy.home.suggestionsTitle })).getAllByRole("button").map((b) => b.textContent);
};
const workspaceOrder = () => within(main().getByRole("region", { name: copy.home.browseTitle })).getAllByRole("heading", { level: 3 }).map((h) => h.textContent);

beforeEach(() => localStorage.clear());
afterEach(() => {
  cleanup();
  localStorage.clear();
});

describe("role- and activity-aware home", () => {
  it("shows an AP clerk and a planner different prompts and workspace order", async () => {
    const all = loadModules();
    const ap = ["receiving.ap.inquire", "receiving.vendor-invoice.post", "payment-proposal.read", "procurement.purchase-order.inquire", COPILOT_DUTY];
    const planner = ["planning.run", "planning.read", "scheduling.read", "procurement.purchase-order.inquire", COPILOT_DUTY];
    renderHome(ap, { shellModules: all, config: null });
    const apPrompts = await suggestions();
    const apOrder = workspaceOrder();
    cleanup();
    renderHome(planner, { shellModules: all, config: null });
    const plannerPrompts = await suggestions();
    expect(apPrompts[0]).toBe(copy.home.suggestionPrompts.invoicesToMatch);
    expect(plannerPrompts[0]).toBe(copy.home.suggestionPrompts.planningMessages);
    expect(workspaceOrder()).not.toEqual(apOrder);
  });

  it("uses role defaults for a new user, with no empty section and no error", async () => {
    renderHome([COPILOT_DUTY]);
    expect(await suggestions()).toEqual([copy.home.suggestionPrompts.attention]);
    expect(workspaceOrder()).toEqual(["Alpha", "Beta", "Gamma"]);
    expect(main().getByText(copy.home.recentEmpty)).toBeInTheDocument();
    expect(main().queryByRole("list", { name: copy.home.recentRecords })).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("ranks the workspace and the record the person opened recently first", async () => {
    seed(transactionsActivity);
    renderHome([COPILOT_DUTY, INVOICE_DUTY]);
    await askBox();
    expect(workspaceOrder()).toEqual(["Alpha", "Gamma", "Beta"]);
    const records = within(main().getByRole("list", { name: copy.home.recentRecords }));
    expect(records.getByRole("link", { name: "Customer invoices" })).toHaveAttribute("href", "/billing/invoices/inv-1");
  });

  it("keeps the rendered order identical to the order in the document and has no axe violations", async () => {
    seed(transactionsActivity);
    const { container } = renderHome([COPILOT_DUTY, INVOICE_DUTY]);
    await askBox();
    const cards = [...container.querySelectorAll(`ul[aria-label="${copy.home.browseTitle}"] > li`)];
    expect(cards.map((c) => c.querySelector("h3")?.textContent)).toEqual(workspaceOrder());
    expect(container.querySelector("[style*='order']")).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });

  it("never shows a workspace or record tied to a duty the person lacks, even when it is in their history", async () => {
    seed([page("/ledger/reports"), page("/ledger/reports", "2026-11-18T10:00:00.000Z"), page("/ledger/reports", "2026-11-17T10:00:00.000Z"), invoice]);
    renderHome([COPILOT_DUTY]);
    await askBox();
    expect(workspaceOrder()).toEqual(["Alpha", "Beta", "Gamma"]);
    expect(main().queryByText("Secret")).not.toBeInTheDocument();
    expect(main().queryByRole("list", { name: copy.home.recentRecords })).not.toBeInTheDocument();
    expect(main().queryByRole("link", { name: "Customer invoices" })).not.toBeInTheDocument();
  });

  it.each([["not json", "{"], ["an object", '{"kind":"page"}'], ["wrong rows", JSON.stringify([1, null, { kind: "page", route: 7 }])]])("falls back to defaults when the stored value is %s", async (_name, value) => {
    seed(value);
    renderHome([COPILOT_DUTY]);
    await askBox();
    expect(workspaceOrder()).toEqual(["Alpha", "Beta", "Gamma"]);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("stores only the entity type, the route or id and a time when a person opens a page", async () => {
    renderHome([COPILOT_DUTY], { entry: "/ledger/transactions" });
    await screen.findByText("Transactions page");
    await waitFor(() => expect(localStorage.getItem(activityKey(IDENTITY))).not.toBeNull());
    const rows: Record<string, unknown>[] = JSON.parse(localStorage.getItem(activityKey(IDENTITY)) ?? "[]");
    expect(rows).toHaveLength(1);
    expect(Object.keys(rows[0] ?? {}).sort()).toEqual(["at", "kind", "route"]);
    expect(rows[0]).toMatchObject({ kind: "page", route: "/ledger/transactions" });
  });
});

describe("reset personalization", () => {
  const UNRELATED = "depot.pins.v1.ann";
  const RECENT = [{ to: "/ledger/transactions", label: "Transactions" }];
  const seedAll = () => {
    seed(transactionsActivity);
    localStorage.setItem(recentKey(IDENTITY), JSON.stringify(RECENT));
    localStorage.setItem(UNRELATED, "keep me");
  };
  const open = async () => {
    renderHome([COPILOT_DUTY, INVOICE_DUTY]);
    await askBox();
    await userEvent.click(main().getByRole("button", { name: copy.home.resetPersonalization }));
    return screen.getByRole("alertdialog", { name: copy.home.resetTitle });
  };

  it("keeps everything when the person cancels or presses Escape", async () => {
    seedAll();
    const dialog = await open();
    await userEvent.click(within(dialog).getByRole("button", { name: copy.home.resetCancel }));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    await userEvent.click(main().getByRole("button", { name: copy.home.resetPersonalization }));
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(workspaceOrder()).toEqual(["Alpha", "Gamma", "Beta"]);
    expect(localStorage.getItem(activityKey(IDENTITY))).not.toBeNull();
    expect(localStorage.getItem(recentKey(IDENTITY))).not.toBeNull();
  });

  it("restores the defaults on confirm and clears only the activity and recents keys", async () => {
    seedAll();
    const dialog = await open();
    await userEvent.click(within(dialog).getByRole("button", { name: copy.home.resetConfirm }));
    await waitFor(() => expect(workspaceOrder()).toEqual(["Alpha", "Beta", "Gamma"]));
    expect(await suggestions()).toEqual([copy.home.suggestionPrompts.attention]);
    expect(main().getByText(copy.home.recentEmpty)).toBeInTheDocument();
    expect(main().queryByRole("list", { name: copy.home.recentRecords })).not.toBeInTheDocument();
    expect(screen.getAllByRole("status").map((el) => el.textContent)).toContain(copy.home.resetDone);
    expect(localStorage.getItem(activityKey(IDENTITY))).toBeNull();
    expect(localStorage.getItem(recentKey(IDENTITY))).toBeNull();
    expect(localStorage.getItem(UNRELATED)).toBe("keep me");
  });

  it("completes when the stored activity is corrupt", async () => {
    seed("{");
    const dialog = await open();
    await userEvent.click(within(dialog).getByRole("button", { name: copy.home.resetConfirm }));
    await waitFor(() => expect(localStorage.getItem(activityKey(IDENTITY))).toBeNull());
    expect(workspaceOrder()).toEqual(["Alpha", "Beta", "Gamma"]);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("has no axe violations with the control shown", async () => {
    const { container } = renderHome([COPILOT_DUTY, INVOICE_DUTY]);
    await askBox();
    expect(main().getByRole("button", { name: copy.home.resetPersonalization })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });
});
