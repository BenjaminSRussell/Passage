import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import inventory from "./module";
import { validateReconcile } from "./NewReconciliationPage";

const RUN_ID = "0190a0b0-0000-7000-8000-000000000071";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];

const json = (status: number, body: unknown) => ({ status, body });
const sent = (method: string) => calls.filter((c) => c.method === method);

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init });
      const { status, body } = handler(url, init);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
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
        {inventory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

function run(overrides: Record<string, unknown> = {}) {
  return {
    id: RUN_ID,
    legalEntityId: ENTITY_ID,
    account: "1300",
    currency: "USD",
    runDate: "2026-10-27",
    inventoryValue: "350",
    ledgerBalance: "425.5",
    diff: "-75.5",
    status: "DIFFERENCE",
    ledgerSource: "LEDGER_OVERRIDE",
    negativeRows: 0,
    createdAt: "2026-10-27T02:00:00Z",
    ...overrides,
  };
}

describe("reconciliation form rules", () => {
  it("mirrors the service checks", () => {
    expect(validateReconcile({ legalEntityId: ENTITY_ID, currency: "USD", ledgerBalance: "" })).toEqual({});
    expect(validateReconcile({ legalEntityId: ENTITY_ID, currency: "EUR", ledgerBalance: "-12.123456" })).toEqual({});
    const errors = validateReconcile({ legalEntityId: "nope", currency: "usd", ledgerBalance: "1.0000001" });
    expect(Object.keys(errors).sort()).toEqual(["currency", "ledgerBalance", "legalEntityId"]);
    expect(validateReconcile({ legalEntityId: ENTITY_ID, currency: "USD", ledgerBalance: "12,5" }).ledgerBalance).toBeDefined();
  });
});

describe("Reconciliation runs", () => {
  it("lists runs with both sides and the difference, links to the run and filters by status", async () => {
    handler = () => json(200, { items: [run(), run({ id: "r2", runDate: "2026-10-26", status: "MATCHED", diff: "0", ledgerBalance: "350", negativeRows: 2 })] });
    const { container } = renderAt("/inventory/reconciliations");
    const table = await screen.findByRole("table", { name: "Reconciliation runs" });
    expect(table).toHaveTextContent("350 USD");
    expect(table).toHaveTextContent("425.5 USD");
    expect(table).toHaveTextContent("-75.5 USD");
    expect(table).toHaveTextContent("Difference");
    expect(table).toHaveTextContent("Matched");
    expect(screen.getByRole("link", { name: "2026-10-27" })).toHaveAttribute("href", `/inventory/reconciliations/${RUN_ID}`);
    expect(screen.getByRole("link", { name: "Run reconciliation" })).toHaveAttribute("href", "/inventory/reconciliations/new");
    expect(calls[0]?.url.pathname).toBe("/api/v1/inventory/reconciliations");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "DIFFERENCE" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("status")).toBe("DIFFERENCE"));
  });

  it("shows the empty state", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/inventory/reconciliations");
    expect(await screen.findByText("No reconciliation runs yet")).toBeInTheDocument();
  });

  it("reports a load failure with a retry", async () => {
    handler = () => json(500, { code: "COM-0500", detail: "unexpected error" });
    renderAt("/inventory/reconciliations");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not load reconciliation runs");
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(calls).toHaveLength(2));
  });
});

describe("Reconciliation run", () => {
  it("explains a difference and flags negative stock", async () => {
    handler = (url) => {
      expect(url.pathname).toBe(`/api/v1/inventory/reconciliations/${RUN_ID}`);
      return json(200, run({ negativeRows: 3 }));
    };
    const { container } = renderAt(`/inventory/reconciliations/${RUN_ID}`);
    expect(await screen.findByRole("heading", { name: "Reconciliation 2026-10-27" })).toBeInTheDocument();
    expect(screen.getByText("Inventory and ledger disagree")).toBeInTheDocument();
    expect(screen.getByText(/-75.5 USD against the ledger/)).toBeInTheDocument();
    expect(screen.getByText("3 on-hand rows have negative physical stock. Check the on-hand inquiry.")).toBeInTheDocument();
    const result = screen.getByRole("list", { name: "Reconciliation result" });
    expect(result).toHaveTextContent("Balance read from the ledger");
    expect(result).toHaveTextContent("350 USD");
    expect(pageViolations(container)).toEqual([]);
  });

  it("confirms a matched run without a negative-stock warning", async () => {
    handler = () => json(200, run({ status: "MATCHED", diff: "0", ledgerBalance: "350", ledgerSource: "GL_JOURNALS" }));
    renderAt(`/inventory/reconciliations/${RUN_ID}`);
    expect(await screen.findByText("Inventory matches the ledger")).toBeInTheDocument();
    expect(screen.queryByText("Negative stock")).toBeNull();
    expect(screen.getByText("GL of the posted inventory journals", { selector: "dd" })).toBeInTheDocument();
  });

  it("shows not found for an unknown run", async () => {
    handler = () => json(404, { code: "INV-REC-001", detail: "reconciliation run not found" });
    renderAt(`/inventory/reconciliations/${RUN_ID}`);
    expect(await screen.findByText("Reconciliation run not found")).toBeInTheDocument();
  });

  it("reports a load failure", async () => {
    handler = () => json(500, { code: "COM-0500", detail: "unexpected error" });
    renderAt(`/inventory/reconciliations/${RUN_ID}`);
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load the reconciliation run");
  });
});

describe("Run reconciliation", () => {
  it("checks the fields before calling the service", async () => {
    handler = () => json(200, run());
    renderAt("/inventory/reconciliations/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: "nope" } });
    fireEvent.click(screen.getByRole("button", { name: "Run reconciliation" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("runs against the posted journals and opens the run", async () => {
    handler = () => json(200, run({ status: "MATCHED", diff: "0" }));
    const { container } = renderAt("/inventory/reconciliations/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.click(screen.getByRole("button", { name: "Run reconciliation" }));
    expect(await screen.findByRole("heading", { name: "Reconciliation 2026-10-27" })).toBeInTheDocument();
    const post = sent("POST")[0];
    expect(post?.url.pathname).toBe("/api/v1/inventory/reconciliations");
    expect(JSON.parse(post?.init.body as string)).toEqual({ legalEntityId: ENTITY_ID, currency: "USD" });
  });

  it("sends a ledger balance read from the ledger", async () => {
    handler = () => json(200, run());
    renderAt("/inventory/reconciliations/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Ledger balance (optional)"), { target: { value: " 425.50 " } });
    fireEvent.click(screen.getByRole("button", { name: "Run reconciliation" }));
    await waitFor(() => expect(sent("POST")).toHaveLength(1));
    expect(JSON.parse(sent("POST")[0]?.init.body as string)).toEqual({ legalEntityId: ENTITY_ID, currency: "USD", ledgerBalance: "425.50" });
  });

  it("maps field errors and shows other failures", async () => {
    handler = () => json(400, { code: "COM-0001", detail: "one or more fields are invalid", errors: { currency: "must be an ISO 4217 alpha-3 code" } });
    renderAt("/inventory/reconciliations/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.click(screen.getByRole("button", { name: "Run reconciliation" }));
    expect(await screen.findByText("must be an ISO 4217 alpha-3 code")).toBeInTheDocument();

    handler = () => json(403, { code: "AUTH-0002", detail: "missing duty inventory.admin" });
    fireEvent.click(screen.getByRole("button", { name: "Run reconciliation" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not run the reconciliation");
    expect(alert).toHaveTextContent("(AUTH-0002)");
  });
});
