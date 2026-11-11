import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Balance, CheckOutcome, Encumbrance, RegisterEntry, Rule } from "./api/client";
import { BudgetCheckPage, CheckDetailPage } from "./CheckPage";
import { EncumbrancePage } from "./EncumbrancePage";
import { RegisterEntryPage } from "./RegisterEntryPage";
import { RuleDetailPage } from "./RuleDetailPage";
import { CreateRulePage, RulesPage } from "./RulesPage";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const LE = "11111111-1111-4111-8111-111111111111";
const R1 = "22222222-2222-4222-8222-222222222222";
const E1 = "33333333-3333-4333-8333-333333333333";
const D1 = "44444444-4444-4444-8444-444444444444";
const L1 = "55555555-5555-4555-8555-555555555555";
const C1 = "66666666-6666-4666-8666-666666666666";

const rule: Rule = { id: R1, legalEntityId: LE, code: "DEPT", version: 1, warnPct: "0", blockPct: "5", currency: "USD", effectiveFrom: "2026-01-01" };
const balance: Balance = {
  id: "b1",
  ruleVersionId: R1,
  controlDimensionKey: "dept-10",
  timeSpanKey: "FY2026",
  budgetTotal: "100000",
  actuals: "60000",
  encumbrances: "30000",
  preEncumbrances: "0",
  reservations: "0",
  available: "10000",
  version: 3,
};
const entry: RegisterEntry = {
  id: "e1",
  ruleVersionId: R1,
  entryType: "Original",
  status: "Posted",
  total: "100000",
  postedAt: "2026-10-01T08:00:00Z",
  lines: [{ dimensionKey: "dept-10", spanKey: "FY2026", amount: "100000" }],
};
const encumbrance: Encumbrance = {
  id: E1,
  ruleVersionId: R1,
  controlDimensionKey: "dept-10",
  timeSpanKey: "FY2026",
  sourceType: "PurchaseOrder",
  sourceDocumentId: D1,
  sourceLineId: L1,
  reserved: "500",
  relieved: "0",
  released: "0",
  remaining: "500",
  status: "Open",
  history: [{ type: "Reserve", amount: "500", documentId: D1, at: "2026-10-02T08:00:00Z" }],
  createdAt: "2026-10-02T08:00:00Z",
};
const failed: CheckOutcome = {
  id: C1,
  ruleVersionId: R1,
  documentId: D1,
  documentType: "PurchaseOrder",
  result: "fail",
  overridden: false,
  checkedAt: "2026-10-03T08:00:00Z",
  lines: [
    {
      lineId: L1,
      dimensionKey: "dept-10",
      spanKey: "FY2026",
      amount: "12000",
      prior: "0",
      result: "fail",
      available: "10000",
      net: "12000",
      overPct: "20",
      trace: ["relieve-prior", "available", "threshold:fail"],
    },
  ],
};

type Handler = (url: string, init: RequestInit) => Response | Promise<Response>;
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status });

/** Routes fetch by "METHOD path-prefix"; the longest matching key wins. */
function stubRoutes(routes: Record<string, Handler>) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const method = init.method ?? "GET";
    const key = Object.keys(routes)
      .filter((k) => {
        const [m, prefix] = k.split(" ");
        return m === method && url.startsWith(prefix);
      })
      .sort((a, b) => b.length - a.length)[0];
    if (!key) return json(500, { title: "unrouted", code: "TEST-0000", detail: `${method} ${url}` });
    return routes[key](url, init);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function calls(fetchMock: ReturnType<typeof vi.fn>, method: string, fragment: string) {
  return (fetchMock.mock.calls as unknown as [string, RequestInit][]).filter(([url, init]) => (init?.method ?? "GET") === method && url.includes(fragment));
}

function at(path: string) {
  return render(
    createElement(
      MemoryRouter,
      { initialEntries: [path] },
      createElement(
        Routes,
        null,
        createElement(Route, { path: "/budgeting", element: createElement(RulesPage) }),
        createElement(Route, { path: "/budgeting/rules/new", element: createElement(CreateRulePage) }),
        createElement(Route, { path: "/budgeting/rules/:id", element: createElement(RuleDetailPage) }),
        createElement(Route, { path: "/budgeting/rules/:id/entries/new", element: createElement(RegisterEntryPage) }),
        createElement(Route, { path: "/budgeting/check", element: createElement(BudgetCheckPage) }),
        createElement(Route, { path: "/budgeting/checks/:id", element: createElement(CheckDetailPage) }),
        createElement(Route, { path: "/budgeting/encumbrances/:id", element: createElement(EncumbrancePage) }),
      ),
    ),
  );
}

const detailRoutes = (extra: Record<string, Handler> = {}) =>
  stubRoutes({
    [`GET /api/v1/budget/control-rules/${R1}`]: () => json(200, rule),
    "GET /api/v1/budget/available": () => json(200, { items: [balance] }),
    "GET /api/v1/budget/register-entries": () => json(200, { items: [entry] }),
    "GET /api/v1/budget/encumbrances?": () => json(200, { items: [encumbrance] }),
    "GET /api/v1/budget/checks?": () => json(200, { items: [failed] }),
    ...extra,
  });

afterEach(() => vi.unstubAllGlobals());

describe("RulesPage", () => {
  it("lists rule versions and opens one", async () => {
    detailRoutes({ "GET /api/v1/budget/control-rules": () => json(200, { items: [rule] }) });
    const { container } = at("/budgeting");
    const table = await findTable({ name: "Budget control rules" });
    expect(await within(table).findByText(LE)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "DEPT" }));
    expect(await screen.findByRole("heading", { name: "Rule DEPT v1" })).toBeInTheDocument();
  });

  it("shows the empty state, and an error with a retry", async () => {
    let fail = true;
    const fetchMock = stubRoutes({
      "GET /api/v1/budget/control-rules": () => (fail ? json(503, { title: "down", code: "COM-0500", detail: "internal error" }) : json(200, { items: [] })),
    });
    at("/budgeting");
    expect(await findAlert()).toHaveTextContent("Something went wrong on our side");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No budget control rules yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    await waitFor(() => expect(calls(fetchMock, "GET", `control-rules?legalEntityId=${LE}`)).toHaveLength(1));
  });
});

describe("CreateRulePage", () => {
  it("validates on the page, then posts with an Idempotency-Key and opens the new version", async () => {
    const fetchMock = detailRoutes({ "POST /api/v1/budget/control-rules": () => json(201, rule) });
    at("/budgeting/rules/new");
    fireEvent.change(screen.getByLabelText("Warn threshold %"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Save rule version" }));
    expect(await screen.findByText("The block threshold cannot be below the warn threshold.")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", "control-rules")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Rule code"), { target: { value: "DEPT" } });
    fireEvent.change(screen.getByLabelText("Warn threshold %"), { target: { value: "0" } });
    fireEvent.change(screen.getByLabelText("Block threshold %"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Save rule version" }));
    expect(await screen.findByRole("heading", { name: "Rule DEPT v1" })).toBeInTheDocument();
    const [, init] = calls(fetchMock, "POST", "control-rules")[0];
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
    expect(JSON.parse(init.body as string)).toMatchObject({ legalEntityId: LE, code: "DEPT", warnPct: "0", blockPct: "5" });
  });
});

describe("RuleDetailPage", () => {
  it("shows available funds, entries, encumbrances and checks, and filters encumbrances", async () => {
    const fetchMock = detailRoutes();
    const { container } = at(`/budgeting/rules/${R1}`);
    const funds = await findTable({ name: "Available funds" });
    expect(await within(funds).findByText("10000.00")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Funds summary" })).toHaveTextContent("Available 10000.00");
    expect(await within(screen.getByRole("table", { name: "Register entries" })).findByText("Original")).toBeInTheDocument();
    expect(await within(screen.getByRole("table", { name: "Budget checks" })).findByText("Over budget: blocked")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Encumbrance status"), { target: { value: "Open" } });
    await waitFor(() => expect(calls(fetchMock, "GET", `encumbrances?rule=${R1}&status=Open`)).toHaveLength(1));
    fireEvent.click(within(await findTable({ name: "Encumbrances" })).getByRole("button", { name: D1 }));
    expect(await screen.findByRole("heading", { name: "Encumbrance" })).toBeInTheDocument();
  });

  it("shows empty tables for a rule with no budget yet", async () => {
    stubRoutes({
      [`GET /api/v1/budget/control-rules/${R1}`]: () => json(200, rule),
      "GET /api/v1/budget/available": () => json(200, { items: [] }),
      "GET /api/v1/budget/register-entries": () => json(200, { items: [] }),
      "GET /api/v1/budget/encumbrances?": () => json(200, { items: [] }),
      "GET /api/v1/budget/checks?": () => json(200, { items: [] }),
    });
    at(`/budgeting/rules/${R1}`);
    expect(await screen.findByText("No budget on this rule yet")).toBeInTheDocument();
    expect(await screen.findByText("No register entries yet")).toBeInTheDocument();
  });

  it("shows the problem when the rule cannot be loaded", async () => {
    stubRoutes({ [`GET /api/v1/budget/control-rules/${R1}`]: () => json(404, { title: "Not Found", code: "BUD-0001", detail: "budget control rule version not found" }) });
    at(`/budgeting/rules/${R1}`);
    expect(await findAlert()).toHaveTextContent("budget control rule version not found");
  });
});

describe("RegisterEntryPage", () => {
  it("refuses an unbalanced transfer on the page and posts a balanced one", async () => {
    const fetchMock = detailRoutes({ "POST /api/v1/budget/register-entries": () => json(201, entry) });
    at(`/budgeting/rules/${R1}/entries/new`);
    fireEvent.change(screen.getByLabelText("Entry type"), { target: { value: "Transfer" } });
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    for (const [i, dim, amount] of [
      [0, "dept-10", "-5000"],
      [1, "dept-20", "4000"],
    ] as const) {
      fireEvent.change(document.getElementById(`entry-dim-${i}`)!, { target: { value: dim } });
      fireEvent.change(document.getElementById(`entry-span-${i}`)!, { target: { value: "FY2026" } });
      fireEvent.change(document.getElementById(`entry-amount-${i}`)!, { target: { value: amount } });
    }
    fireEvent.click(screen.getByRole("button", { name: "Post entry" }));
    expect(await screen.findByText(/must net to zero/)).toBeInTheDocument();
    expect(calls(fetchMock, "POST", "register-entries")).toHaveLength(0);

    fireEvent.change(document.getElementById("entry-amount-1")!, { target: { value: "5000" } });
    fireEvent.click(screen.getByRole("button", { name: "Post entry" }));
    expect(await screen.findByRole("heading", { name: "Rule DEPT v1" })).toBeInTheDocument();
    const [, init] = calls(fetchMock, "POST", "register-entries")[0];
    expect(JSON.parse(init.body as string)).toMatchObject({ ruleVersionId: R1, entryType: "Transfer", lines: [{ amount: "-5000" }, { amount: "5000" }] });
  });
});

describe("BudgetCheckPage", () => {
  it("runs a check and shows the blocked line with its trace", async () => {
    const fetchMock = stubRoutes({
      "GET /api/v1/budget/control-rules": () => json(200, { items: [rule] }),
      "POST /api/v1/budget/checks": () => json(200, failed),
    });
    const { container } = at(`/budgeting/check?rule=${R1}`);
    expect(await screen.findByRole("option", { name: /DEPT v1/ })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Dimension"), { target: { value: "dept-10" } });
    fireEvent.change(screen.getByLabelText("Time span"), { target: { value: "FY2026" } });
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "12000" } });
    fireEvent.click(screen.getByRole("button", { name: "Run check" }));
    const lines = await findTable({ name: "Check lines" });
    expect(within(lines).getByText("20.00%")).toBeInTheDocument();
    expect(within(lines).getByText("Over budget: blocked")).toBeInTheDocument();
    expect(screen.getByText(/threshold:fail/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    const body = JSON.parse(calls(fetchMock, "POST", "/checks")[0][1].body as string);
    expect(body).toMatchObject({ ruleVersionId: R1, documentType: "PurchaseOrder", lines: [{ dimensionKey: "dept-10", amount: "12000" }] });
    expect(body.lines[0]).not.toHaveProperty("prior");
  });

  it("validates before sending and shows a refused check's problem", async () => {
    const fetchMock = stubRoutes({
      "GET /api/v1/budget/control-rules": () => json(200, { items: [rule] }),
      "POST /api/v1/budget/checks": () => json(409, { title: "Conflict", code: "BUD-0003", detail: "line is already reserved against another control combination" }),
    });
    at("/budgeting/check");
    fireEvent.click(screen.getByRole("button", { name: "Run check" }));
    expect(await screen.findByText("Choose a control rule version.")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", "/checks")).toHaveLength(0);
    await screen.findByRole("option", { name: /DEPT v1/ });
    fireEvent.change(screen.getByLabelText("Control rule version"), { target: { value: R1 } });
    fireEvent.change(screen.getByLabelText("Dimension"), { target: { value: "dept-20" } });
    fireEvent.change(screen.getByLabelText("Time span"), { target: { value: "FY2026" } });
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Run check" }));
    expect(await screen.findByText(/already reserved against another control combination/)).toBeInTheDocument();
  });

  it("shows a recorded check", async () => {
    stubRoutes({ [`GET /api/v1/budget/checks/${C1}`]: () => json(200, failed) });
    at(`/budgeting/checks/${C1}`);
    expect(await findTable({ name: "Check lines" })).toHaveTextContent("10000.00");
  });
});

describe("EncumbrancePage", () => {
  it("relieves from an invoice and reports the price excess (AC-38.3)", async () => {
    const fetchMock = stubRoutes({
      [`GET /api/v1/budget/encumbrances/${E1}`]: () => json(200, encumbrance),
      [`POST /api/v1/budget/encumbrances/${E1}:relieve`]: () =>
        json(200, { encumbranceId: E1, relieved: "500", remaining: "0", excess: "20", status: "Relieved" }),
    });
    const { container } = at(`/budgeting/encumbrances/${E1}`);
    expect(await findTable({ name: "Encumbrance history" })).toHaveTextContent("Reserve");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Relieve encumbrance" }));
    expect(await screen.findByText("Enter the invoice id (a UUID).")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Invoice id"), { target: { value: D1 } });
    fireEvent.change(screen.getByLabelText("Invoiced quantity"), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText("PO unit price"), { target: { value: "50.00" } });
    fireEvent.change(screen.getByLabelText("Invoice amount"), { target: { value: "520.00" } });
    fireEvent.click(screen.getByRole("button", { name: "Relieve encumbrance" }));
    expect(await screen.findByText(/exceeds the reservation by 20.00/)).toBeInTheDocument();
    const [, init] = calls(fetchMock, "POST", ":relieve")[0];
    expect(JSON.parse(init.body as string)).toEqual({ invoiceId: D1, invoiceQuantity: "10", poUnitPrice: "50.00", invoiceAmount: "520.00" });
    await waitFor(() => expect(calls(fetchMock, "GET", `/encumbrances/${E1}`).length).toBeGreaterThan(1));
  });

  it("releases the open remainder on a PO close after a confirmation (FR-38.5)", async () => {
    let released = false;
    const fetchMock = stubRoutes({
      [`GET /api/v1/budget/encumbrances/${E1}`]: () =>
        json(
          200,
          released
            ? { ...encumbrance, relieved: "320", released: "180", remaining: "0", status: "Released", history: [...encumbrance.history, { type: "Release", amount: "180", documentId: D1, at: "2026-10-04T08:00:00Z" }] }
            : { ...encumbrance, relieved: "320", remaining: "180", status: "PartiallyRelieved" },
        ),
      [`POST /api/v1/budget/encumbrances/${E1}:release`]: () => {
        released = true;
        return json(200, { encumbranceId: E1, reason: "Closed", released: "180", relieved: "320", remaining: "0", status: "Released" });
      },
    });
    const { container } = at(`/budgeting/encumbrances/${E1}`);
    expect(await screen.findByRole("heading", { name: "Release on PO cancellation or close" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    expect(screen.getByLabelText("Purchase order")).toHaveAttribute("data-value", D1);
    fireEvent.click(screen.getByRole("button", { name: "Clear purchase order" }));
    fireEvent.click(screen.getByRole("button", { name: "Release remaining funds" }));
    expect(await screen.findByText("Choose the purchase order.")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", ":release")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Purchase order"), { target: { value: D1 } });
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "Closed" } });
    fireEvent.click(screen.getByRole("button", { name: "Release remaining funds" }));
    expect(await screen.findByText("Release 180.00?")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", ":release")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Confirm release" }));
    expect(await screen.findByText(/The open remainder went back to available funds/)).toBeInTheDocument();
    const [, init] = calls(fetchMock, "POST", ":release")[0];
    expect(JSON.parse(init.body as string)).toEqual({ documentId: D1, reason: "Closed" });
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    expect(screen.getByRole("button", { name: "Relieve encumbrance" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Release remaining funds" })).not.toBeInTheDocument();
  });

  it("shows the service's refusal when nothing is left to release", async () => {
    stubRoutes({
      [`GET /api/v1/budget/encumbrances/${E1}`]: () => json(200, encumbrance),
      [`POST /api/v1/budget/encumbrances/${E1}:release`]: () =>
        json(409, { title: "Conflict", status: 409, code: "BUD-0010", detail: "the encumbrance has nothing open to release" }),
    });
    at(`/budgeting/encumbrances/${E1}`);
    fireEvent.click(await screen.findByRole("button", { name: "Release remaining funds" }));
    fireEvent.click(await screen.findByRole("button", { name: "Confirm release" }));
    expect(await screen.findByText("the encumbrance has nothing open to release")).toBeInTheDocument();
  });
});
