import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { loadModules } from "../registry";
import { amount } from "./format";
import { ac404, validateJournal } from "./JournalPreviewPage";
import intercompany from "./module";
import { sample, validateRecon } from "./ReconciliationPage";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, body: unknown) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; body: unknown; init: RequestInit }[];

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      const body = init.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method: init.method ?? "GET", url, body, init });
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

describe("intercompany module", () => {
  it("is discovered by the shell and keeps every route and nav entry under /intercompany with PRD duties", () => {
    expect(loadModules().some((m) => m.id === "intercompany")).toBe(true);
    expect(intercompany.routes.every((r) => r.element != null && r.path?.startsWith("/intercompany"))).toBe(true);
    expect(intercompany.nav.map((n) => n.to)).toEqual([
      "/intercompany",
      "/intercompany/partners",
      "/intercompany/journal",
      "/intercompany/journals",
      "/intercompany/reconciliation",
      "/intercompany/reconciliation-runs",
      "/intercompany/charges",
      "/intercompany/charge-log",
      "/intercompany/centralized-payments",
      "/intercompany/planning",
      "/intercompany/chains",
      "/intercompany/transfer-pricing",
    ]);
    expect(intercompany.nav.map((n) => n.duty)).toEqual([
      "intercompany.view",
      "intercompany.view",
      "intercompany.post",
      "intercompany.view",
      "intercompany.reconcile",
      "intercompany.view",
      "intercompany.post",
      "intercompany.view",
      "intercompany.configure",
      "intercompany.plan",
      "intercompany.view",
      "intercompany.view",
    ]);
  });

  it("links the workspace to each tool", () => {
    const { container } = renderAt("/intercompany");
    expect(screen.getByRole("heading", { level: 2, name: "Intercompany workspace" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Preview a journal" })).toHaveAttribute("href", "/intercompany/journal");
    expect(screen.getByRole("link", { name: "Classify balances" })).toHaveAttribute("href", "/intercompany/reconciliation");
    expect(screen.getByRole("link", { name: "Price a charge" })).toHaveAttribute("href", "/intercompany/charges");
    expect(screen.getByRole("link", { name: "Preview a payment" })).toHaveAttribute("href", "/intercompany/centralized-payments");
    expect(screen.getByRole("link", { name: "Sequence a planning group" })).toHaveAttribute("href", "/intercompany/planning");
    expect(pageViolations(container)).toEqual([]);
  });

  it("pads amounts without float arithmetic", () => {
    expect(amount("4")).toBe("4.00");
    expect(amount("-1.5")).toBe("-1.50");
    expect(amount("923.705")).toBe("923.705");
    expect(amount(undefined)).toBe("—");
  });
});

const ac404Legs = [
  { entity: "A", account: "6100-EXPENSE", side: "debit", amount: "1000.00", currency: "USD" },
  { entity: "A", account: "1500-DUE-FROM-B", side: "credit", amount: "1000.00", currency: "USD" },
  { entity: "B", account: "2500-DUE-TO-A", side: "debit", amount: "1000.00", currency: "USD" },
  { entity: "B", account: "1000-CASH", side: "credit", amount: "1000.00", currency: "USD" },
];

describe("intercompany journal page", () => {
  it("previews AC-40.4 and shows both entities' legs under one pair id", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/interco/journals:preview"
        ? { status: 200, body: { pairId: "01890a5d-ac96-774b-bcce-b302099a8058", balanced: true, legs: ac404Legs } }
        : undefined;
    const { container } = renderAt("/intercompany/journal");
    expect(screen.getByRole("heading", { name: "No journal yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Preview journal" }));
    await screen.findByText("Balanced in each entity");
    const table = screen.getByRole("table", { name: "Journal legs" });
    expect(calls[0].method).toBe("POST");
    expect(calls[0].body).toEqual({
      entity: "A",
      account: "6100-EXPENSE",
      side: "debit",
      amount: "1000.00",
      currency: "USD",
      offsetEntity: "B",
      offsetAccount: "1000-CASH",
      matrix: [{ entityA: "A", entityB: "B", dueFrom: "1500-DUE-FROM-B", dueTo: "2500-DUE-TO-A" }],
    });
    expect(screen.getByText("Balanced in each entity").parentElement).toHaveTextContent("01890a5d-ac96-774b-bcce-b302099a8058");
    const dueFrom = within(table).getByRole("row", { name: /1500-DUE-FROM-B/ });
    expect(within(dueFrom).getAllByRole("cell").map((c) => c.textContent)).toEqual(["A", "1500-DUE-FROM-B", "—", "1000.00", "USD"]);
    const dueTo = within(table).getByRole("row", { name: /2500-DUE-TO-A/ });
    expect(within(dueTo).getAllByRole("cell").map((c) => c.textContent)).toEqual(["B", "2500-DUE-TO-A", "1000.00", "—", "USD"]);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows IC-0805 when the matrix has no row and keeps the entered values", async () => {
    handler = () => ({ status: 422, body: { code: "IC-0805", detail: "no IC accounting matrix entry for A to C" } });
    renderAt("/intercompany/journal");
    fireEvent.change(screen.getByLabelText("Offset entity"), { target: { value: "C" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview journal" }));
    await waitFor(() => expect(screen.getByText("The journal was not derived")).toBeInTheDocument());
    expect(screen.getByText("The journal was not derived").closest("[role=alert]")).toHaveTextContent(
      "no IC accounting matrix entry for A to C",
    );
    expect(screen.getByLabelText("Offset entity")).toHaveValue("C");
    expect(screen.queryByRole("table", { name: "Journal legs" })).not.toBeInTheDocument();
  });

  it("puts the service's field errors next to the matrix fields", async () => {
    handler = () => ({ status: 400, body: { code: "COM-0001", detail: "request failed validation", errors: { "matrix[0].dueTo": "is required" } } });
    renderAt("/intercompany/journal");
    fireEvent.click(screen.getByRole("button", { name: "Preview journal" }));
    const row = await screen.findByRole("group", { name: "Matrix row 1" });
    await waitFor(() => expect(within(row).getByText("is required")).toBeInTheDocument());
  });

  it("checks the line before sending", () => {
    expect(validateJournal({ ...ac404, offsetEntity: "A" }).errors.offsetEntity).toBe("The offset entity must differ from the entity.");
    expect(validateJournal({ ...ac404, amount: "0.00" }).errors.amount).toBeDefined();
    expect(validateJournal({ ...ac404, counterCurrency: "EUR" }).errors.rate).toBeDefined();
    expect(validateJournal({ ...ac404, counterCurrency: "eur", rate: "0.9237" }).request?.counterCurrency).toBe("EUR");
    renderAt("/intercompany/journal");
    fireEvent.change(screen.getByLabelText("Due to A (in B)"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview journal" }));
    expect(screen.getByRole("group", { name: "Matrix row 1" })).toHaveTextContent("Enter due to a (in b).");
    expect(calls).toHaveLength(0);
  });

  it("adds and removes matrix rows", () => {
    renderAt("/intercompany/journal");
    fireEvent.click(screen.getByRole("button", { name: "Add matrix row" }));
    expect(screen.getByRole("group", { name: "Matrix row 2" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Remove matrix row 2" }));
    expect(screen.queryByRole("group", { name: "Matrix row 2" })).not.toBeInTheDocument();
  });
});

describe("intercompany reconciliation page", () => {
  const classified = {
    fxTolerancePct: "0.5",
    items: [
      { id: "IC-1001", entityA: "P", entityB: "S", period: "2026-11", currency: "USD", amountA: "1000.00", amountB: "1000.00", difference: "0.00", status: "Matched", toleranceApplied: false },
      { id: "IC-1002", entityA: "P", entityB: "S", period: "2026-11", currency: "USD", amountA: "1000.00", amountB: "996.00", difference: "4.00", status: "Matched", toleranceApplied: true },
      { id: "IC-1003", entityA: "P", entityB: "S", period: "2026-11", currency: "USD", amountA: "250.00", amountB: "0.00", difference: "250.00", status: "Timing", toleranceApplied: false },
      { id: "IC-1004", entityA: "P", entityB: "S", period: "2026-11", currency: "USD", amountA: "100.00", amountB: "105.00", difference: "-5.00", status: "Mismatch", toleranceApplied: false },
    ],
    summary: { matched: 2, timing: 1, mismatch: 1, blocking: true },
  };

  it("classifies the sample pairs and shows the counts, statuses and the consolidation warning", async () => {
    handler = (url) => (url.pathname === "/api/v1/interco/reconciliations:classify" ? { status: 200, body: classified } : undefined);
    const { container } = renderAt("/intercompany/reconciliation");
    expect(screen.getByRole("heading", { name: "Nothing classified yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Classify pairs" }));
    await screen.findByRole("list", { name: "Reconciliation summary" });
    const table = screen.getByRole("table", { name: "Reconciliation items" });
    const body = calls[0].body as { fxTolerancePct: string; items: Record<string, unknown>[] };
    expect(body.fxTolerancePct).toBe("0.5");
    expect(body.items).toHaveLength(4);
    expect(body.items[1]).toEqual({ id: "IC-1002", entityA: "P", entityB: "S", period: "2026-11", currency: "USD", amountA: "1000.00", amountB: "996.00", fxApplies: true });
    expect(body.items[2].nextPeriodB).toBe("250.00");
    const summary = screen.getByRole("list", { name: "Reconciliation summary" });
    expect(summary).toHaveTextContent("Matched 2");
    expect(summary).toHaveTextContent("Mismatch 1");
    expect(within(table).getByRole("row", { name: /IC-1002/ })).toHaveTextContent("within FX tolerance");
    expect(within(table).getByRole("row", { name: /IC-1003/ })).toHaveTextContent("Timing");
    expect(within(table).getByRole("row", { name: /IC-1004/ })).toHaveTextContent("-5.00");
    expect(screen.getByText("Mismatches block consolidation")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a refused request in an alert", async () => {
    handler = () => ({ status: 403, body: { code: "AUTH-0003", detail: "missing duty intercompany.reconcile" } });
    renderAt("/intercompany/reconciliation");
    fireEvent.click(screen.getByRole("button", { name: "Classify pairs" }));
    await waitFor(() => expect(screen.getByText("The pairs were not classified")).toBeInTheDocument());
    expect(screen.getByText("The pairs were not classified").closest("[role=alert]")).toHaveTextContent(
      "missing duty intercompany.reconcile",
    );
    expect(screen.queryByRole("table", { name: "Reconciliation items" })).not.toBeInTheDocument();
  });

  it("checks pairs before sending", () => {
    const same = { ...sample, items: [{ ...sample.items[0], entityB: "P" }] };
    expect(validateRecon(same).errors["items[0].entityB"]).toBe("Entity B must differ from entity A.");
    expect(validateRecon({ ...sample, fxTolerancePct: "6" }).errors.fxTolerancePct).toBeDefined();
    expect(validateRecon({ ...sample, fxTolerancePct: "5.00" }).request).toBeDefined();
    expect(validateRecon({ ...sample, items: [] }).errors.items).toBe("Add at least one pair.");
    renderAt("/intercompany/reconciliation");
    const pair1 = screen.getByRole("group", { name: "Pair 1" });
    fireEvent.change(within(pair1).getByLabelText("Period"), { target: { value: "Nov" } });
    fireEvent.click(screen.getByRole("button", { name: "Classify pairs" }));
    expect(pair1).toHaveTextContent("Enter a period such as 2026-11.");
    expect(calls).toHaveLength(0);
  });
});
