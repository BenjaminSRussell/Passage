import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { ac4010, validatePayment } from "./CentralPaymentsPage";
import { ac4011, validateCharge } from "./ChargesPage";
import intercompany from "./module";
import { ac4016, validatePlanning } from "./PlanningPage";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, body: unknown) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; body: unknown }[];

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      const body = init.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method: init.method ?? "GET", url, body });
      const reply = handler(url, body) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {intercompany.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const cells = (row: HTMLElement) => within(row).getAllByRole("cell").map((c) => c.textContent);
const bodyRows = (table: HTMLElement) => within(table).getAllByRole("row").slice(1);

const chargePreview = {
  pairId: "01890a5d-ac96-774b-bcce-b302099a8057",
  currency: "EUR",
  transferPrice: { method: "CostPlus", markupPct: "10", effectiveFrom: "2026-01-01" },
  cost: "480.00",
  price: "528.00",
  margin: "48.00",
  balanced: true,
  legs: [
    { entity: "S", account: "5100-PROJECT-COST", side: "debit", amount: "528.00", currency: "EUR" },
    { entity: "S", account: "2510-DUE-TO-P", side: "credit", amount: "528.00", currency: "EUR" },
    { entity: "P", account: "1510-DUE-FROM-S", side: "debit", amount: "528.00", currency: "EUR" },
    { entity: "P", account: "4800-COST-RECOVERY", side: "credit", amount: "480.00", currency: "EUR" },
    { entity: "P", account: "4810-IC-MARGIN", side: "credit", amount: "48.00", currency: "EUR" },
  ],
};

describe("intercompany charges page", () => {
  it("prices AC-40.11 and lists both entities' legs", async () => {
    handler = (url) => (url.pathname === "/api/v1/interco/charges:preview" ? { status: 200, body: chargePreview } : undefined);
    const { container } = renderAt("/intercompany/charges");
    expect(screen.getByRole("heading", { name: "No charge priced yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Price charge" }));
    await screen.findByText("Balanced in each entity");
    expect(calls[0].method).toBe("POST");
    expect(calls[0].body).toEqual({
      lendingEntity: "P",
      borrowingEntity: "S",
      category: "Hour",
      resourceRole: "Consultant",
      transactionDate: "2026-11-14",
      quantity: "8",
      unitCost: "60.00",
      currency: "EUR",
      transferPrices: [{ category: "Hour", resourceRole: "Consultant", method: "CostPlus", markupPct: "10", effectiveFrom: "2026-01-01" }],
      costAccount: "5100-PROJECT-COST",
      recoveryAccount: "4800-COST-RECOVERY",
      marginAccount: "4810-IC-MARGIN",
      matrix: [{ entityA: "P", entityB: "S", dueFrom: "1510-DUE-FROM-S", dueTo: "2510-DUE-TO-P" }],
    });
    const tiles = screen.getByRole("list", { name: "Charge pricing" });
    expect(tiles).toHaveTextContent("Cost 480.00 EUR");
    expect(tiles).toHaveTextContent("Price 528.00");
    expect(tiles).toHaveTextContent("IC margin 48.00");
    expect(screen.getByText("Balanced in each entity").parentElement).toHaveTextContent("Priced by Cost plus 10%, effective from 2026-01-01.");
    const rows = bodyRows(screen.getByRole("table", { name: "Charge legs" }));
    expect(rows.map(cells)).toEqual([
      ["S", "5100-PROJECT-COST", "528.00", "—", "EUR"],
      ["S", "2510-DUE-TO-P", "—", "528.00", "EUR"],
      ["P", "1510-DUE-FROM-S", "528.00", "—", "EUR"],
      ["P", "4800-COST-RECOVERY", "—", "480.00", "EUR"],
      ["P", "4810-IC-MARGIN", "—", "48.00", "EUR"],
    ]);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a missing transfer price (IC-0821) and keeps the form", async () => {
    handler = () => ({ status: 422, body: { code: "IC-0821", detail: "no transfer price for Hour / Consultant on 2025-12-31" } });
    renderAt("/intercompany/charges");
    fireEvent.change(screen.getByLabelText("Transaction date"), { target: { value: "2025-12-31" } });
    fireEvent.click(screen.getByRole("button", { name: "Price charge" }));
    await waitFor(() => expect(screen.getByText("The charge was not priced")).toBeInTheDocument());
    expect(screen.getByText("The charge was not priced").closest("[role=alert]")).toHaveTextContent(
      "no transfer price for Hour / Consultant on 2025-12-31 (IC-0821)",
    );
    expect(screen.getByLabelText("Transaction date")).toHaveValue("2025-12-31");
    expect(screen.queryByRole("table", { name: "Charge legs" })).not.toBeInTheDocument();
  });

  it("puts the service's field error on the transfer price row", async () => {
    handler = () => ({
      status: 400,
      body: { code: "COM-0001", detail: "request failed validation", errors: { "transferPrices[0].method": "must be Cost, CostPlus or Table" } },
    });
    renderAt("/intercompany/charges");
    fireEvent.click(screen.getByRole("button", { name: "Price charge" }));
    expect(await screen.findByText("must be Cost, CostPlus or Table")).toBeInTheDocument();
  });

  it("checks the charge before sending", () => {
    const table = { ...ac4011, prices: [{ ...ac4011.prices[0], method: "Table", markupPct: "10", unitPrice: "70" }] };
    expect(validateCharge(table).request?.transferPrices[0]).toEqual({
      category: "Hour",
      resourceRole: "Consultant",
      method: "Table",
      unitPrice: "70",
      effectiveFrom: "2026-01-01",
    });
    expect(validateCharge({ ...ac4011, prices: [{ ...ac4011.prices[0], markupPct: "" }] }).errors["transferPrices[0].markupPct"]).toBeDefined();
    expect(validateCharge({ ...ac4011, prices: [] }).errors.transferPrices).toBe("Add at least one transfer price.");
    expect(validateCharge({ ...ac4011, line: { ...ac4011.line, borrowingEntity: "P" } }).errors.borrowingEntity).toBeDefined();
    expect(validateCharge({ ...ac4011, line: { ...ac4011.line, currency: "eur" } }).request?.currency).toBe("EUR");
    renderAt("/intercompany/charges");
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Price charge" }));
    expect(screen.getByText("Enter a quantity above 0.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});

describe("centralized payments page", () => {
  it("previews AC-40.10 with both entities under one pair", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/interco/centralized-payments:preview"
        ? {
            status: 200,
            body: {
              pairId: "01890a5d-ac96-774b-bcce-b302099a8057",
              balanced: true,
              legs: [
                { entity: "P", account: "1510-DUE-FROM-S", side: "debit", amount: "10000.00", currency: "EUR" },
                { entity: "P", account: "1100-BANK", side: "credit", amount: "10000.00", currency: "EUR" },
                { entity: "S", account: "2000-VENDOR", side: "debit", amount: "10000.00", currency: "EUR" },
                { entity: "S", account: "2510-DUE-TO-P", side: "credit", amount: "10000.00", currency: "EUR" },
              ],
            },
          }
        : undefined;
    const { container } = renderAt("/intercompany/centralized-payments");
    expect(screen.getByRole("heading", { name: "No payment postings yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Preview postings" }));
    await screen.findByText("Balanced in each entity");
    expect(calls[0].body).toMatchObject({ payingEntity: "P", owningEntity: "S", direction: "Pay", amount: "10000.00", currency: "EUR" });
    expect((calls[0].body as { matrix: unknown[] }).matrix).toHaveLength(2);
    const rows = bodyRows(screen.getByRole("table", { name: "Payment legs" }));
    expect(rows.map(cells)).toEqual([
      ["P", "1510-DUE-FROM-S", "10000.00", "—", "EUR"],
      ["P", "1100-BANK", "—", "10000.00", "EUR"],
      ["S", "2000-VENDOR", "10000.00", "—", "EUR"],
      ["S", "2510-DUE-TO-P", "—", "10000.00", "EUR"],
    ]);
    expect(pageViolations(container)).toEqual([]);
  });

  it("sends a receipt on behalf and explains a missing matrix row", async () => {
    handler = () => ({ status: 422, body: { code: "IC-0805", detail: "no IC accounting matrix entry for S to P" } });
    renderAt("/intercompany/centralized-payments");
    fireEvent.change(screen.getByLabelText("Direction"), { target: { value: "Receive" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview postings" }));
    await waitFor(() => expect(screen.getByText("The payment postings were not derived")).toBeInTheDocument());
    expect((calls[0].body as { direction: string }).direction).toBe("Receive");
    expect(screen.getByText("The payment postings were not derived").closest("[role=alert]")).toHaveTextContent("(IC-0805)");
  });

  it("checks the payment before sending", () => {
    expect(validatePayment({ ...ac4010, payment: { ...ac4010.payment, owningEntity: "P" } }).errors.owningEntity).toBeDefined();
    expect(validatePayment({ ...ac4010, payment: { ...ac4010.payment, amount: "0" } }).errors.amount).toBeDefined();
    expect(validatePayment({ ...ac4010, matrix: [{ ...ac4010.matrix[0], dueTo: " " }] }).errors["matrix[0].dueTo"]).toBeDefined();
    renderAt("/intercompany/centralized-payments");
    fireEvent.change(screen.getByLabelText("Payment method"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview postings" }));
    expect(screen.getByText("Enter the payment method.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});

describe("intercompany planning page", () => {
  it("sequences AC-40.16 and shows the supplier demand after lead time", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/interco/planning-groups:sequence"
        ? {
            status: 200,
            body: {
              order: ["A", "B", "C"],
              demand: [{ buyer: "A", supplier: "B", item: "PUMP-100", quantity: "100", dueDate: "2026-12-07" }],
              staleSuppliers: [],
              warnings: [],
            },
          }
        : undefined;
    const { container } = renderAt("/intercompany/planning");
    expect(screen.getByRole("heading", { name: "No run order yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Sequence group" }));
    await findTable({ name: "Intercompany demand" });
    expect(calls[0].body).toEqual({
      members: [{ entityId: "A" }, { entityId: "B" }, { entityId: "C" }],
      edges: [
        { buyer: "A", supplier: "B", leadTimeDays: 3 },
        { buyer: "B", supplier: "C", leadTimeDays: 1 },
      ],
      plannedOrders: [{ buyer: "A", supplier: "B", item: "PUMP-100", quantity: "100", requestedDate: "2026-12-10" }],
    });
    expect(bodyRows(screen.getByRole("table", { name: "Run order" })).map(cells)).toEqual([
      ["1", "A"],
      ["2", "B"],
      ["3", "C"],
    ]);
    expect(bodyRows(screen.getByRole("table", { name: "Intercompany demand" })).map(cells)).toEqual([["B", "A", "PUMP-100", "100", "2026-12-07"]]);
    expect(pageViolations(container)).toEqual([]);
  });

  it("warns about stale suppliers when a buyer is replanned", async () => {
    handler = () => ({
      status: 200,
      body: {
        order: ["A", "B", "C"],
        demand: [],
        staleSuppliers: ["B"],
        warnings: [{ code: "IC-0815", message: "Supplier plan for B is stale after buyer replanning" }],
      },
    });
    renderAt("/intercompany/planning");
    fireEvent.change(screen.getByLabelText("Buyer replanned after the run"), { target: { value: "A" } });
    fireEvent.click(screen.getByRole("button", { name: "Sequence group" }));
    expect(await screen.findByText("Stale supplier plan")).toBeInTheDocument();
    expect((calls[0].body as { replannedBuyer: string }).replannedBuyer).toBe("A");
    expect(screen.getByText("Stale supplier plan").parentElement).toHaveTextContent("Supplier plan for B is stale after buyer replanning (IC-0815)");
  });

  it("explains a cycle without sequence (IC-0816)", async () => {
    handler = () => ({ status: 422, body: { code: "IC-0816", detail: "planning group cycle [A B] has no explicit sequence for A" } });
    renderAt("/intercompany/planning");
    fireEvent.click(screen.getByRole("button", { name: "Sequence group" }));
    await waitFor(() => expect(screen.getByText("The planning group was not sequenced")).toBeInTheDocument());
    expect(screen.getByText("The planning group was not sequenced").closest("[role=alert]")).toHaveTextContent("(IC-0816)");
    expect(screen.queryByRole("table", { name: "Run order" })).not.toBeInTheDocument();
  });

  it("checks the group before sending", () => {
    const seq = { ...ac4016, members: [{ entityId: "A", sequence: "2" }, ...ac4016.members.slice(1)] };
    expect(validatePlanning(seq).request?.members[0]).toEqual({ entityId: "A", sequence: 2 });
    expect(validatePlanning({ ...ac4016, members: [...ac4016.members, { entityId: "A", sequence: "" }] }).errors["members[3].entityId"]).toBeDefined();
    expect(validatePlanning({ ...ac4016, edges: [{ buyer: "A", supplier: "X", leadTimeDays: "" }] }).errors["edges[0].supplier"]).toBe(
      "Add this entity to the group first.",
    );
    expect(validatePlanning({ ...ac4016, edges: [{ ...ac4016.edges[0], leadTimeDays: "-1" }] }).errors["edges[0].leadTimeDays"]).toBeDefined();
    expect(
      validatePlanning({ ...ac4016, orders: [{ ...ac4016.orders[0], supplier: "C" }] }).errors["plannedOrders[0].supplier"],
    ).toBe("Add a planning edge from the buyer to this supplier.");
    renderAt("/intercompany/planning");
    fireEvent.change(screen.getByLabelText("Requested date"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Sequence group" }));
    expect(screen.getByText("Enter the requested date.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});
