import { findTable } from "../../test-utils";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Adjustment, ReconciliationRun, ValuationLedger } from "./api/client";
import { AdjustmentDetailPage, AdjustmentsPage, buildAdjustmentRequest, CreateAdjustmentPage } from "./AdjustmentsPage";
import { CreateReconciliationPage, ReconciliationDetailPage, ReconciliationsPage } from "./ReconciliationsPage";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const LE = "11111111-1111-4111-8111-111111111111";
const P1 = "22222222-2222-4222-8222-222222222222";
const S1 = "44444444-4444-4444-8444-444444444444";
const S2 = "55555555-5555-4555-8555-555555555555";
const ADJ = "66666666-6666-4666-8666-666666666666";
const RUN = "77777777-7777-4777-8777-777777777777";

const gaap: ValuationLedger = {
  id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
  legalEntityId: LE,
  ledgerGroupId: S2,
  code: "USGAAP",
  method: "FIFO",
  currency: "USD",
  rateType: "SPOT",
  primary: false,
  createdAt: "2026-10-01T08:00:00Z",
};

const preview: Adjustment = {
  legalEntityId: LE,
  kind: "ON_HAND",
  mode: "UNIT_COST",
  productId: P1,
  siteId: S1,
  currency: "EUR",
  financialDate: "2026-01-31",
  unitCost: "10",
  qty: "50",
  oldValue: "550.00",
  newValue: "500.00",
  delta: "-50.00",
  oldUnitCost: "11",
  newUnitCost: "10",
};
const posted: Adjustment = { ...preview, id: ADJ, reason: "NRV write-down", costTransactionId: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", createdAt: "2026-02-01T09:00:00Z" };

const run: ReconciliationRun = {
  id: RUN,
  legalEntityId: LE,
  asOf: "2026-03-31",
  currency: "EUR",
  groupBy: ["site"],
  differences: 1,
  totalDiff: "5.00",
  ties: false,
  rowCount: 2,
  rows: [
    { key: { site: S1 }, inventory: "1700.00", ledger: "1700.00", diff: "0.00" },
    { key: { site: S2 }, inventory: "50.00", ledger: "45.00", diff: "5.00" },
  ],
  createdAt: "2026-04-01T02:15:00Z",
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

function calls(fetchMock: ReturnType<typeof vi.fn>, method: string, fragment: string): [string, RequestInit][] {
  return (fetchMock.mock.calls as unknown as [string, RequestInit][]).filter(([url, init]) => (init?.method ?? "GET") === method && url.includes(fragment));
}

function at(path: string) {
  const route = (p: string, el: () => React.JSX.Element) => createElement(Route, { path: p, element: createElement(el) });
  return render(
    createElement(
      MemoryRouter,
      { initialEntries: [path] },
      createElement(
        Routes,
        null,
        route("/costing/adjustments", AdjustmentsPage),
        route("/costing/adjustments/new", CreateAdjustmentPage),
        route("/costing/adjustments/:id", AdjustmentDetailPage),
        route("/costing/reconciliations", ReconciliationsPage),
        route("/costing/reconciliations/new", CreateReconciliationPage),
        route("/costing/reconciliations/:id", ReconciliationDetailPage),
      ),
    ),
  );
}

afterEach(() => vi.unstubAllGlobals());

const form = {
  legalEntityId: LE,
  kind: "ON_HAND" as const,
  mode: "UNIT_COST" as const,
  productId: P1,
  siteId: S1,
  warehouseId: "",
  valuationType: "",
  valuationLedgerId: "",
  receiptTransactionId: "",
  financialDate: "2026-01-31",
  value: "10",
  reason: "",
};

describe("buildAdjustmentRequest", () => {
  it("builds an on-hand request, and a receipt request by amount whatever mode was chosen", () => {
    expect(buildAdjustmentRequest(form, false)).toEqual({
      ok: true,
      value: { legalEntityId: LE, kind: "ON_HAND", mode: "UNIT_COST", financialDate: "2026-01-31", productId: P1, siteId: S1, unitCost: "10" },
    });
    const receipt = buildAdjustmentRequest({ ...form, kind: "RECEIPT", receiptTransactionId: ADJ, value: "-12.5", reason: "freight" }, true);
    expect(receipt).toEqual({
      ok: true,
      value: { legalEntityId: LE, kind: "RECEIPT", mode: "AMOUNT", financialDate: "2026-01-31", receiptTransactionId: ADJ, amount: "-12.5", reason: "freight" },
    });
  });

  it("needs a reason to post, a positive unit cost and a signed amount of at most 4 decimals", () => {
    expect(buildAdjustmentRequest(form, true)).toMatchObject({ ok: false, errors: { reason: expect.any(String) } });
    expect(buildAdjustmentRequest({ ...form, value: "0" }, false)).toMatchObject({ ok: false, errors: { unitCost: expect.any(String) } });
    expect(buildAdjustmentRequest({ ...form, value: "-1" }, false)).toMatchObject({ ok: false, errors: { unitCost: expect.any(String) } });
    expect(buildAdjustmentRequest({ ...form, mode: "AMOUNT", value: "1.00001" }, false)).toMatchObject({ ok: false, errors: { amount: expect.any(String) } });
    expect(buildAdjustmentRequest({ ...form, productId: "p1", warehouseId: "w1" }, false)).toMatchObject({
      ok: false,
      errors: { productId: expect.any(String), warehouseId: expect.any(String) },
    });
  });
});

describe("AdjustmentsPage", () => {
  it("lists adjustments with their change and opens one", async () => {
    const fetchMock = stubRoutes({
      "GET /api/v1/costing/adjustments": () => json(200, { items: [posted] }),
      [`GET /api/v1/costing/adjustments/${ADJ}`]: () => json(200, posted),
    });
    const { container } = at(`/costing/adjustments?legalEntityId=${LE}`);
    const table = await findTable({ name: "Cost adjustments" });
    expect(within(table).getByText("-50.00 EUR")).toBeInTheDocument();
    expect(within(table).getByText("NRV write-down")).toBeInTheDocument();
    expect(calls(fetchMock, "GET", "/adjustments?")[0][0]).toContain(`legalEntityId=${LE}`);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText(/Adjusts/), { target: { value: "RECEIPT" } });
    await waitFor(() => expect(calls(fetchMock, "GET", "kind=RECEIPT")).toHaveLength(1));
  });

  it("shows the empty state, and a retryable error when the service fails", async () => {
    stubRoutes({ "GET /api/v1/costing/adjustments": () => json(200, { items: [] }) });
    at("/costing/adjustments");
    expect(await screen.findByText("No cost adjustments yet")).toBeInTheDocument();
    cleanup();
    vi.unstubAllGlobals();

    let attempts = 0;
    stubRoutes({
      "GET /api/v1/costing/adjustments": () => (++attempts === 1 ? json(503, { title: "down", code: "COM-0503", detail: "unavailable" }) : json(200, { items: [posted] })),
    });
    at("/costing/adjustments");
    expect(await screen.findByText("Could not load the cost adjustments")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Cost adjustments" })).toBeInTheDocument();
  });
});

describe("CreateAdjustmentPage", () => {
  function fill() {
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: P1 } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: S1 } });
    fireEvent.change(screen.getByLabelText(/New unit cost/), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText(/Financial date/), { target: { value: "2026-01-31" } });
  }

  it("previews against the current value, then posts with the previewed value and an Idempotency-Key", async () => {
    const fetchMock = stubRoutes({
      "GET /api/v1/costing/valuation-ledgers": () => json(200, { items: [gaap] }),
      "POST /api/v1/costing/adjustments:preview": () => json(200, preview),
      "POST /api/v1/costing/adjustments": () => json(201, posted),
      [`GET /api/v1/costing/adjustments/${ADJ}`]: () => json(200, posted),
    });
    const { container } = at("/costing/adjustments/new");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Preview adjustment" }));
    expect(await screen.findByRole("heading", { name: "Preview" })).toBeInTheDocument();
    expect(screen.getByText("500.00")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Post adjustment" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    const [, previewInit] = calls(fetchMock, "POST", "adjustments:preview")[0];
    expect(JSON.parse(String(previewInit.body))).toEqual({ legalEntityId: LE, kind: "ON_HAND", mode: "UNIT_COST", financialDate: "2026-01-31", productId: P1, siteId: S1, unitCost: "10" });

    // Posting needs a reason.
    fireEvent.click(screen.getByRole("button", { name: "Post adjustment" }));
    expect(await screen.findByText("Give a reason of 1 to 500 characters.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Reason/), { target: { value: "NRV write-down" } });
    fireEvent.click(screen.getByRole("button", { name: "Post adjustment" }));
    expect(await screen.findByRole("heading", { name: "Cost adjustment on 2026-01-31" })).toBeInTheDocument();
    const posts = (fetchMock.mock.calls as unknown as [string, RequestInit][]).filter(([url, init]) => init?.method === "POST" && url === "/api/v1/costing/adjustments");
    expect(posts).toHaveLength(1);
    const [, init] = posts[0];
    expect(JSON.parse(String(init.body))).toMatchObject({ expectedValue: "550.00", reason: "NRV write-down", unitCost: "10" });
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/[0-9a-f-]{36}/);
  });

  it("checks the form before calling, and drops a stale preview when the value moved", async () => {
    const fetchMock = stubRoutes({
      "POST /api/v1/costing/adjustments:preview": () => json(200, preview),
      "POST /api/v1/costing/adjustments": () => json(409, { title: "Conflict", code: "CST-ADJ-006", detail: "the value changed since the preview: it is now 500.00 EUR" }),
    });
    at("/costing/adjustments/new");
    fireEvent.click(screen.getByRole("button", { name: "Preview adjustment" }));
    expect(await screen.findByText("Enter the legal entity's id (a UUID).")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", "adjustments")).toHaveLength(0);

    fill();
    fireEvent.change(screen.getByLabelText(/Reason/), { target: { value: "NRV" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview adjustment" }));
    fireEvent.click(await screen.findByRole("button", { name: "Post adjustment" }));
    expect(await screen.findByText(/CST-ADJ-006/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Post adjustment" })).not.toBeInTheDocument();
  });

  it("asks only for the receipt when a receipt is adjusted, and editing clears the preview", async () => {
    stubRoutes({ "POST /api/v1/costing/adjustments:preview": () => json(200, { ...preview, kind: "RECEIPT", mode: "AMOUNT", qty: "100", oldValue: "1000.00", newValue: "1012.50", delta: "12.50" }) });
    at("/costing/adjustments/new");
    fireEvent.change(screen.getByLabelText("Adjust"), { target: { value: "RECEIPT" } });
    expect(screen.queryByLabelText("Product")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText(/Receipt transaction/), { target: { value: ADJ } });
    fireEvent.change(screen.getByLabelText(/Change in value/), { target: { value: "12.50" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview adjustment" }));
    expect(await screen.findByText("1012.50")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Change in value/), { target: { value: "15" } });
    expect(screen.queryByRole("heading", { name: "Preview" })).not.toBeInTheDocument();
  });
});

describe("AdjustmentDetailPage", () => {
  it("shows the adjustment and drills to its cost transaction", async () => {
    stubRoutes({ [`GET /api/v1/costing/adjustments/${ADJ}`]: () => json(200, posted) });
    const { container } = at(`/costing/adjustments/${ADJ}`);
    expect(await screen.findByRole("heading", { name: "Cost adjustment on 2026-01-31" })).toBeInTheDocument();
    expect(screen.getByText("cccccccc-cccc-4ccc-8ccc-cccccccccccc")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Cost transactions" }).getAttribute("href")).toContain("kind=MANUAL_ADJ");
    expect(pageViolations(container)).toEqual([]);
  });

  it("says when the adjustment is not found", async () => {
    stubRoutes({ [`GET /api/v1/costing/adjustments/${ADJ}`]: () => json(404, { title: "Not Found", code: "CST-ADJ-007", detail: "cost adjustment not found" }) });
    at(`/costing/adjustments/${ADJ}`);
    expect(await screen.findByText("Cost adjustment not found")).toBeInTheDocument();
  });
});

describe("Reconciliation pages", () => {
  it("lists runs and shows the empty state", async () => {
    stubRoutes({ "GET /api/v1/costing/reconciliations": () => json(200, { items: [{ ...run, rows: undefined }] }) });
    const { container } = at("/costing/reconciliations");
    const table = await findTable({ name: "Reconciliations" });
    expect(within(table).getByText("Differs")).toBeInTheDocument();
    expect(within(table).getByText("5.00 EUR")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    cleanup();
    vi.unstubAllGlobals();

    stubRoutes({ "GET /api/v1/costing/reconciliations": () => json(200, { items: [] }) });
    at("/costing/reconciliations");
    expect(await screen.findByText("No reconciliations yet")).toBeInTheDocument();
  });

  it("runs a reconciliation from pasted balances and opens the stored run", async () => {
    const fetchMock = stubRoutes({
      "GET /api/v1/costing/valuation-ledgers": () => json(200, { items: [] }),
      "POST /api/v1/costing/reconciliations": () => json(201, run),
      [`GET /api/v1/costing/reconciliations/${RUN}`]: () => json(200, run),
    });
    const { container } = at("/costing/reconciliations/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("As of date"), { target: { value: "2026-03-31" } });
    fireEvent.click(screen.getByRole("button", { name: "Run and store" }));
    expect(await screen.findByText("Enter at least one ledger balance below the header.")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", "reconciliations")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText(/Ledger balances/), { target: { value: `site,balance\n${S1},1700.00\n${S2},45.00` } });
    fireEvent.click(screen.getByRole("button", { name: "Run and store" }));
    expect(await screen.findByRole("heading", { name: "Reconciliation as of 2026-03-31" })).toBeInTheDocument();
    const [, init] = calls(fetchMock, "POST", "reconciliations")[0];
    expect(JSON.parse(String(init.body))).toEqual({
      legalEntityId: LE,
      asOf: "2026-03-31",
      groupBy: ["site"],
      glBalances: [
        { dimensions: { site: S1 }, balance: "1700.00" },
        { dimensions: { site: S2 }, balance: "45.00" },
      ],
    });
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    const groups = screen.getByRole("table", { name: "Reconciliation by group" });
    expect(within(groups).getByText("5.00")).toBeInTheDocument();
  });

  it("follows the grouping in the balances header and reports a refused run", async () => {
    stubRoutes({
      "GET /api/v1/costing/valuation-ledgers": () => json(200, { items: [] }),
      "POST /api/v1/costing/reconciliations": () => json(422, { title: "Unprocessable", code: "CST-RPT-002", detail: "ledger balance 0 must name exactly the grouping dimensions [site product]" }),
    });
    at("/costing/reconciliations/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.click(screen.getByLabelText("Product"));
    const balances = screen.getByLabelText(/Ledger balances/) as HTMLTextAreaElement;
    expect(balances.value.split("\n")[0]).toBe("product,site,balance");
    fireEvent.change(balances, { target: { value: `product,site,balance\n${P1},${S1},10` } });
    fireEvent.click(screen.getByRole("button", { name: "Run and store" }));
    expect(await screen.findByText(/CST-RPT-002/)).toBeInTheDocument();
  });

  it("says when a run is not found", async () => {
    stubRoutes({ [`GET /api/v1/costing/reconciliations/${RUN}`]: () => json(404, { title: "Not Found", code: "CST-REC-002", detail: "reconciliation run not found" }) });
    at(`/costing/reconciliations/${RUN}`);
    expect(await screen.findByText("Reconciliation not found")).toBeInTheDocument();
  });
});
