import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { setLookupFetcher } from "../../shell/lookup/entities";
import intercompany from "./module";
import { validateEntity, validateMatrix, validatePrice, validateSettings } from "./SetupPages";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, method: string, body: unknown) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const LE_US = "00000000-0000-0000-0000-0000000000e1";
const LE_DE = "00000000-0000-0000-0000-0000000000e2";
const entities = [
  { entity: "CRP-US", legalEntityId: LE_US, ledgerId: "CRP-US-GL", currency: "USD", country: "US", fxDifferenceAccount: "7900-FX-DIFF" },
  { entity: "CRP-DE", legalEntityId: LE_DE, ledgerId: "CRP-DE-GL", currency: "EUR", country: "DE" },
];

let handler: Handler;
let calls: { method: string; url: URL; body: unknown; headers: Record<string, string> }[];

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  const respond = async (input: string, init: RequestInit = {}) => {
    const url = new URL(input, "http://localhost");
    const method = init.method ?? "GET";
    const body = init.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method, url, body, headers: (init.headers as Record<string, string>) ?? {} });
    if (url.pathname.startsWith("/api/v1/org/legal-entities")) {
      const items = [
        { id: LE_US, code: "CRP-US", name: "Cedar Ridge Pumps, Inc." },
        { id: LE_DE, code: "CRP-DE", name: "Cedar Ridge Pumps Europe GmbH" },
      ];
      const one = items.find((i) => url.pathname.endsWith(i.id));
      return new Response(JSON.stringify(one ?? { items }), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    const reply = handler(url, method, body) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${method} ${url.pathname}` } };
    return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
  };
  vi.stubGlobal("fetch", vi.fn(respond));
  setLookupFetcher((url, init) => respond(url, init));
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

describe("setup checks", () => {
  it("checks entities, matrix rows, prices and settings like the service", () => {
    const entity = { entity: "CRP-US", legalEntityId: LE_US, ledgerId: "GL", currency: "usd", country: "", fxDifferenceAccount: "", outputTaxAccount: "", inputTaxAccount: "", taxGroup: "" };
    expect(validateEntity(entity)).toEqual({});
    expect(Object.keys(validateEntity({ ...entity, entity: "bad code!", legalEntityId: "", currency: "US", country: "USA", fxDifferenceAccount: "has space" })).sort()).toEqual(["country", "currency", "entity", "fxDifferenceAccount", "legalEntityId"]);
    const row = { entityA: "CRP-US", entityB: "CRP-DE", dueFrom: "1500", dueTo: "2500" };
    expect(validateMatrix([row])).toEqual({});
    expect(validateMatrix([row, { ...row }])).toEqual({ "rows[1].entityB": "This pair appears twice." });
    expect(validateMatrix([{ ...row, entityB: "CRP-US" }])).toEqual({ "rows[0].entityB": "A pair needs two different entities." });
    const price = { lendingEntity: "CRP-DE", borrowingEntity: "CRP-US", category: "Hour" as const, resourceRole: "", method: "CostPlus" as const, markupPct: "10", unitPrice: "", currency: "USD", effectiveFrom: "2026-11-01" };
    expect(validatePrice(price)).toEqual({});
    expect(Object.keys(validatePrice({ ...price, method: "Table", unitPrice: "0", effectiveFrom: "" })).sort()).toEqual(["effectiveFrom", "unitPrice"]);
    expect(validateSettings({ chargeSettlementMode: "Journal", crossBorderTax: false, reconCurrency: "", fxTolerancePct: "0.5", fxAdjustMaxPct: "2" })).toEqual({});
    expect(Object.keys(validateSettings({ chargeSettlementMode: "Journal", crossBorderTax: false, reconCurrency: "EURO", fxTolerancePct: "x", fxAdjustMaxPct: "11" })).sort()).toEqual(["fxAdjustMaxPct", "fxTolerancePct", "reconCurrency"]);
  });
});

describe("entity registry", () => {
  it("lists the entities by legal entity name and saves a new one", async () => {
    handler = (url, method) => {
      if (url.pathname === "/api/v1/interco/entities") return { status: 200, body: { items: entities } };
      if (url.pathname === "/api/v1/interco/entities/CRP-ES" && method === "PUT") return { status: 200, body: { entity: "CRP-ES" } };
      return undefined;
    };
    const { container, unmount } = renderAt("/intercompany/entities");
    expect(await screen.findByText("CRP-DE-GL")).toBeInTheDocument();
    expect(await screen.findByText(/Cedar Ridge Pumps Europe GmbH/)).toBeInTheDocument();
    expect(screen.queryByText(LE_DE)).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    unmount();

    renderAt("/intercompany/entities/new");
    fireEvent.click(screen.getByRole("button", { name: "Save entity" }));
    expect(await screen.findByText(/Enter a code of 1 to 32/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Entity code"), { target: { value: "CRP-ES" } });
    fireEvent.change(screen.getByLabelText("Ledger"), { target: { value: "CRP-ES-GL" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "eur" } });
    fireEvent.change(screen.getByRole("combobox", { name: "Legal entity" }), { target: { value: "Cedar Ridge Pumps, Inc." } });
    fireEvent.click(await screen.findByRole("option", { name: /Cedar Ridge Pumps, Inc\./ }));
    fireEvent.click(screen.getByRole("button", { name: "Save entity" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.url.pathname).toBe("/api/v1/interco/entities/CRP-ES");
    expect(put.body).toMatchObject({ legalEntityId: LE_US, ledgerId: "CRP-ES-GL", currency: "EUR" });
    expect(await screen.findByRole("heading", { name: "Intercompany entities" })).toBeInTheDocument();
  });
});

describe("accounting matrix", () => {
  it("edits the pairs with the registry's entities and replaces the matrix", async () => {
    handler = (url, method) => {
      if (url.pathname === "/api/v1/interco/entities") return { status: 200, body: { items: entities } };
      if (url.pathname === "/api/v1/interco/matrix" && method === "GET") return { status: 200, body: { rows: [{ entityA: "CRP-US", entityB: "CRP-DE", dueFrom: "1500", dueTo: "2500" }] } };
      if (url.pathname === "/api/v1/interco/matrix" && method === "PUT") return { status: 200, body: { rows: [{ entityA: "CRP-US", entityB: "CRP-DE", dueFrom: "1500", dueTo: "2500" }, { entityA: "CRP-DE", entityB: "CRP-US", dueFrom: "1510", dueTo: "2510" }] } };
      return undefined;
    };
    const { container } = renderAt("/intercompany/matrix");
    expect(await screen.findByDisplayValue("1500")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add a pair" }));
    const pair = within(screen.getByRole("group", { name: "Pair 2" }));
    fireEvent.click(screen.getByRole("button", { name: "Save the matrix" }));
    expect(await pair.findByText("Choose the entity that holds the receivable.")).toBeInTheDocument();
    fireEvent.change(pair.getByLabelText("Entity A (receivable)"), { target: { value: "CRP-DE" } });
    fireEvent.change(pair.getByLabelText("Entity B (payable)"), { target: { value: "CRP-US" } });
    fireEvent.change(pair.getByLabelText("A's receivable account"), { target: { value: " 1510 " } });
    fireEvent.change(pair.getByLabelText("B's payable account"), { target: { value: "2510" } });
    fireEvent.click(screen.getByRole("button", { name: "Save the matrix" }));
    expect(await screen.findByText("The matrix was saved with 2 pairs.")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "PUT")!.body).toEqual({ rows: [{ entityA: "CRP-US", entityB: "CRP-DE", dueFrom: "1500", dueTo: "2500" }, { entityA: "CRP-DE", entityB: "CRP-US", dueFrom: "1510", dueTo: "2510" }] });
    expect(pageViolations(container)).toEqual([]);
  });
});

describe("transfer prices", () => {
  it("lists the stored prices and adds one", async () => {
    handler = (url, method) => {
      if (url.pathname === "/api/v1/interco/entities") return { status: 200, body: { items: entities } };
      if (url.pathname === "/api/v1/interco/transfer-prices" && method === "GET")
        return { status: 200, body: { items: [{ id: "tp1", lendingEntity: "CRP-DE", borrowingEntity: "CRP-US", category: "Hour", resourceRole: "Engineer", method: "CostPlus", markupPct: "10", currency: "USD", effectiveFrom: "2026-01-01", createdBy: "ana.ortiz" }] } };
      if (url.pathname === "/api/v1/interco/transfer-prices" && method === "POST") return { status: 201, body: { id: "tp2" } };
      return undefined;
    };
    const { unmount } = renderAt("/intercompany/transfer-prices");
    expect(await screen.findByText("Cost plus 10 %")).toBeInTheDocument();
    expect(screen.getByText("Hour, Engineer")).toBeInTheDocument();
    unmount();

    renderAt("/intercompany/transfer-prices/new");
    await screen.findAllByRole("option", { name: "CRP-DE (EUR)" });
    fireEvent.change(screen.getByLabelText("Lending entity"), { target: { value: "CRP-DE" } });
    fireEvent.change(screen.getByLabelText("Borrowing entity"), { target: { value: "CRP-US" } });
    fireEvent.change(screen.getByLabelText("Pricing"), { target: { value: "Table" } });
    fireEvent.change(screen.getByLabelText("Price of one unit"), { target: { value: "85" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "usd" } });
    fireEvent.change(screen.getByLabelText("Effective from"), { target: { value: "2026-12-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Add the price" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({ lendingEntity: "CRP-DE", borrowingEntity: "CRP-US", category: "Hour", resourceRole: "", method: "Table", unitPrice: "85", currency: "USD", effectiveFrom: "2026-12-01" });
  });
});

describe("settings", () => {
  it("loads and saves the tenant's settings", async () => {
    handler = (url, method) => {
      if (url.pathname !== "/api/v1/interco/settings") return undefined;
      return method === "PUT" ? { status: 200, body: calls.at(-1)!.body } : { status: 200, body: { chargeSettlementMode: "Journal", crossBorderTax: false, reconCurrency: "", fxTolerancePct: "0.5", fxAdjustMaxPct: "2" } };
    };
    const { container } = renderAt("/intercompany/settings");
    fireEvent.change(await screen.findByLabelText("Charges settle by"), { target: { value: "Invoice" } });
    fireEvent.change(screen.getByLabelText(/Scheduled reconciliation currency/), { target: { value: "usd" } });
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    expect(await screen.findByText("The settings were saved.")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "PUT")!.body).toEqual({ chargeSettlementMode: "Invoice", crossBorderTax: false, reconCurrency: "USD", fxTolerancePct: "0.5", fxAdjustMaxPct: "2" });
    expect(pageViolations(container)).toEqual([]);
  });
});

describe("ledger reconciliation and the FX adjustment", () => {
  const run = { id: "run-1", period: "2026-10", currency: "USD", fxTolerancePct: "0.5", matched: 0, timing: 0, mismatch: 1, open: 1, blocking: true, source: "ledger", createdBy: "dev", createdAt: "2026-11-01T10:00:00Z" };
  const item = { id: "it-1", runId: "run-1", ref: "CRP-US>CRP-DE", entityA: "CRP-US", entityB: "CRP-DE", amountA: "18450.00", amountB: "18210.00", nextPeriodA: "0.00", nextPeriodB: "0.00", fxApplies: true, difference: "240.00", status: "Mismatch", toleranceApplied: false, resolution: "open" };

  it("reads the ledgers with an idempotency key and opens the stored run", async () => {
    handler = (url, method) => {
      if (url.pathname === "/api/v1/interco/reconciliations/run" && method === "POST") return { status: 201, body: run };
      if (url.pathname === "/api/v1/interco/reconciliations/run-1") return { status: 200, body: { ...run, items: [item] } };
      return undefined;
    };
    renderAt("/intercompany/ledger-reconciliation");
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "2026-10" } });
    fireEvent.change(screen.getByLabelText("Compare in currency"), { target: { value: "usd" } });
    fireEvent.click(screen.getByRole("button", { name: "Read the ledgers and reconcile" }));
    expect(await screen.findByRole("heading", { name: "Reconciliation 2026-10" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ period: "2026-10", currency: "USD", source: "ledger" });
    expect(post.headers["Idempotency-Key"]).toBeTruthy();
  });

  it("books the exchange difference of an open pair in two currencies", async () => {
    let booked = false;
    handler = (url, method) => {
      if (url.pathname === "/api/v1/interco/reconciliations/run-1") return { status: 200, body: { ...run, open: booked ? 0 : 1, items: [{ ...item, resolution: booked ? "resolved" : "open", note: booked ? "Exchange difference booked" : undefined, resolvedBy: "dev" }] } };
      if (url.pathname === "/api/v1/interco/reconciliations/items/it-1/fx-adjust" && method === "POST") {
        booked = true;
        return { status: 200, body: { id: "adj-1", itemId: "it-1", entity: "CRP-US", currency: "USD", amount: "240.00", direction: "decrease", account: "1500-DUE-FROM-DE", fxAccount: "7900-FX-DIFF", ledgerEntryId: "le-1", postedOn: "2026-10-31", periodEnd: "2026-10-31" } };
      }
      return undefined;
    };
    const { container } = renderAt("/intercompany/reconciliation-runs/run-1");
    fireEvent.click(await screen.findByRole("button", { name: "Book the exchange difference of CRP-US>CRP-DE" }));
    expect(await screen.findByText(/CRP-US booked 240.00 USD on 2026-10-31/)).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("button", { name: /Book the exchange difference/ })).not.toBeInTheDocument());
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a difference too large to book (IC-0861)", async () => {
    handler = (url, method) => {
      if (url.pathname === "/api/v1/interco/reconciliations/run-1") return { status: 200, body: { ...run, items: [item] } };
      if (method === "POST") return { status: 422, body: { code: "IC-0861", detail: "the difference is above 2 % of the larger side" } };
      return undefined;
    };
    renderAt("/intercompany/reconciliation-runs/run-1");
    fireEvent.click(await screen.findByRole("button", { name: /Book the exchange difference/ }));
    expect(await screen.findByText("The exchange difference was not booked")).toBeInTheDocument();
  });
});
