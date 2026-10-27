import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { ac403, validateChain } from "./ChainsPage";
import intercompany from "./module";
import { parseCsv, sampleEvidence, validateEvidence } from "./TransferPricingPage";

type Reply = { status: number; body: unknown; text?: string; headers?: Record<string, string> };
type Handler = (url: URL, body: unknown) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; body: unknown; accept?: string }[];

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      const body = init.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method: init.method ?? "GET", url, body, accept: (init.headers as Record<string, string> | undefined)?.Accept });
      const reply = handler(url, body) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
      if (reply.text !== undefined) return new Response(reply.text, { status: reply.status, headers: reply.headers });
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

describe("module registration", () => {
  it("routes and links the slice-3 pages with their duties", () => {
    expect(intercompany.routes.map((r) => r.path)).toEqual(expect.arrayContaining(["/intercompany/chains", "/intercompany/transfer-pricing"]));
    expect(intercompany.nav).toEqual(
      expect.arrayContaining([
        { label: "Direct delivery chains", to: "/intercompany/chains", duty: "IntercompanyView" },
        { label: "Transfer-pricing evidence", to: "/intercompany/transfer-pricing", duty: "IntercompanyView" },
      ]),
    );
    renderAt("/intercompany");
    expect(screen.getByRole("link", { name: "Check a chain" })).toHaveAttribute("href", "/intercompany/chains");
    expect(screen.getByRole("link", { name: "Produce an evidence file" })).toHaveAttribute("href", "/intercompany/transfer-pricing");
  });
});

describe("direct delivery chains page", () => {
  it("previews AC-40.3: the receipt and the customer packing slip at the same quantity", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/interco/chains:preview"
        ? {
            status: 200,
            body: {
              chainId: "CH-2026-0001",
              postings: [
                { kind: "ProductReceipt", entity: "A", docId: "PO-A-2001", quantity: "20" },
                { kind: "CustomerPackingSlip", entity: "A", docId: "SO-A-1001", quantity: "20" },
              ],
            },
          }
        : undefined;
    const { container } = renderAt("/intercompany/chains");
    expect(screen.getByRole("heading", { name: "No chain checked yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Preview packing slip postings" }));
    await screen.findByText("Chain is consistent");
    expect(calls[0].method).toBe("POST");
    expect(calls[0].body).toEqual({
      chainId: "CH-2026-0001",
      partner: { sellerEntity: "B", buyerEntity: "A", active: true },
      customerOrder: { docId: "SO-A-1001", entity: "A", quantity: "20" },
      icPurchase: { docId: "PO-A-2001", entity: "A", quantity: "20" },
      icSales: { docId: "SO-B-3001", entity: "B", quantity: "20" },
    });
    expect(bodyRows(screen.getByRole("table", { name: "Packing slip postings" })).map(cells)).toEqual([
      ["Product receipt", "A", "PO-A-2001", "20"],
      ["Customer packing slip", "A", "SO-A-1001", "20"],
    ]);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows an inactive pairing (IC-0801) and keeps the form", async () => {
    handler = () => ({ status: 422, body: { code: "IC-0801", detail: "no active IC partner policy between B and A" } });
    renderAt("/intercompany/chains");
    fireEvent.change(screen.getByLabelText("Partner pairing"), { target: { value: "no" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview packing slip postings" }));
    await waitFor(() => expect(screen.getByText("The chain was not accepted")).toBeInTheDocument());
    expect((calls[0].body as { partner: { active: boolean } }).partner.active).toBe(false);
    expect(screen.getByText("The chain was not accepted").closest("[role=alert]")).toHaveTextContent("no active IC partner policy between B and A (IC-0801)");
    expect(screen.queryByRole("table", { name: "Packing slip postings" })).not.toBeInTheDocument();
  });

  it("checks the chain before sending and puts service errors on the leg", async () => {
    expect(validateChain({ ...ac403, shippedQuantity: "5" }).request?.shippedQuantity).toBe("5");
    expect(validateChain({ ...ac403, buyerEntity: "B" }).errors["partner.buyerEntity"]).toBeDefined();
    expect(validateChain({ ...ac403, "icSales.quantity": "0" }).errors["icSales.quantity"]).toBeDefined();
    expect(validateChain({ ...ac403, shippedQuantity: "x" }).errors.shippedQuantity).toBeDefined();
    renderAt("/intercompany/chains");
    fireEvent.change(screen.getByLabelText("IC purchase order (buyer) number"), { target: { value: " " } });
    fireEvent.click(screen.getByRole("button", { name: "Preview packing slip postings" }));
    expect(screen.getByText("Enter the document number.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    handler = () => ({ status: 400, body: { code: "COM-0001", detail: "request failed validation", errors: { "icSales.entity": "is required" } } });
    fireEvent.change(screen.getByLabelText("IC purchase order (buyer) number"), { target: { value: "PO-A-2001" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview packing slip postings" }));
    expect(await screen.findByText("is required")).toBeInTheDocument();
  });
});

const csv =
  "seller_entity,buyer_entity,period,item,quantity,currency,price,cost,margin,margin_pct,basis\n" +
  "P,S,2026-11,Hour/Consultant,8,EUR,528.00,480.00,48.00,9.09,CostPlus\n" +
  'P,S,2026-11,"PUMP-100, blue",10,EUR,1500.00,1500.00,0.00,0.00,Cost\n';

describe("transfer-pricing evidence page", () => {
  it("produces the evidence file, lists its lines and offers the download", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/interco/transfer-pricing/evidence:export"
        ? { status: 200, body: null, text: csv, headers: { "Content-Type": "text/csv", "Content-Disposition": 'attachment; filename="tp-evidence-P-S-2026-11.csv"' } }
        : undefined;
    const { container } = renderAt("/intercompany/transfer-pricing");
    expect(screen.getByRole("heading", { name: "No evidence file yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Produce evidence file" }));
    await screen.findByText("Evidence file ready");
    expect(calls[0].accept).toBe("text/csv");
    expect(calls[0].body).toEqual({
      sellerEntity: "P",
      buyerEntity: "S",
      period: "2026-11",
      currency: "EUR",
      rows: [
        { item: "Hour/Consultant", quantity: "8", price: "528.00", cost: "480.00", basis: "CostPlus" },
        { item: "PUMP-100", quantity: "10", price: "1500.00", cost: "1500.00", basis: "Cost" },
      ],
    });
    const link = screen.getByRole("link", { name: "Download tp-evidence-P-S-2026-11.csv" });
    expect(link).toHaveAttribute("download", "tp-evidence-P-S-2026-11.csv");
    expect(decodeURIComponent(link.getAttribute("href") ?? "")).toBe(`data:text/csv;charset=utf-8,${csv}`);
    expect(bodyRows(screen.getByRole("table", { name: "Evidence lines" })).map(cells)).toEqual([
      ["Hour/Consultant", "8", "528.00", "480.00", "48.00", "9.09", "CostPlus"],
      ["PUMP-100, blue", "10", "1500.00", "1500.00", "0.00", "0.00", "Cost"],
    ]);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a service failure and puts row errors next to the row", async () => {
    handler = () => ({ status: 400, body: { code: "COM-0001", detail: "request failed validation", errors: { "rows[1].basis": "is required" } } });
    renderAt("/intercompany/transfer-pricing");
    fireEvent.click(screen.getByRole("button", { name: "Produce evidence file" }));
    expect(await screen.findByText("is required")).toBeInTheDocument();
    expect(screen.getByText("The evidence file was not produced").closest("[role=alert]")).toHaveTextContent("(COM-0001)");
    expect(screen.queryByRole("table", { name: "Evidence lines" })).not.toBeInTheDocument();
  });

  it("checks the lines before sending", () => {
    expect(validateEvidence({ ...sampleEvidence, head: { ...sampleEvidence.head, currency: "eur" } }).request?.currency).toBe("EUR");
    expect(validateEvidence({ ...sampleEvidence, head: { ...sampleEvidence.head, period: "2026-13" } }).errors.period).toBeDefined();
    expect(validateEvidence({ ...sampleEvidence, head: { ...sampleEvidence.head, buyerEntity: "P" } }).errors.buyerEntity).toBeDefined();
    expect(validateEvidence({ ...sampleEvidence, rows: [] }).errors.rows).toBe("Add at least one line.");
    expect(validateEvidence({ ...sampleEvidence, rows: [{ ...sampleEvidence.rows[0], cost: "-1" }] }).errors["rows[0].cost"]).toBeDefined();
    expect(validateEvidence({ ...sampleEvidence, rows: [{ ...sampleEvidence.rows[0], quantity: "0" }] }).errors["rows[0].quantity"]).toBeDefined();
  });

  it("parses quoted CSV cells", () => {
    expect(parseCsv('a,"b ""x"", c"\r\n1,2\n')).toEqual([
      ["a", 'b "x", c'],
      ["1", "2"],
    ]);
  });
});

describe("centralized payment reversal", () => {
  const pairId = "01890a5d-ac96-774b-bcce-b302099a8057";
  const legs = [
    { entity: "P", account: "1510-DUE-FROM-S", side: "debit", amount: "10000.00", currency: "EUR" },
    { entity: "P", account: "1100-BANK", side: "credit", amount: "10000.00", currency: "EUR" },
    { entity: "S", account: "2000-VENDOR", side: "debit", amount: "10000.00", currency: "EUR" },
    { entity: "S", account: "2510-DUE-TO-P", side: "credit", amount: "10000.00", currency: "EUR" },
  ];
  const flip = (side: string) => (side === "debit" ? "credit" : "debit");

  it("reverses the previewed payment under its pair id (BR-40.11)", async () => {
    handler = (url, body) => {
      if (url.pathname === "/api/v1/interco/centralized-payments:preview") return { status: 200, body: { pairId, balanced: true, legs } };
      if (url.pathname === "/api/v1/interco/centralized-payments/reversals:preview")
        return { status: 200, body: { pairId: (body as { pairId: string }).pairId, balanced: true, legs: legs.map((l) => ({ ...l, side: flip(l.side) })) } };
      return undefined;
    };
    renderAt("/intercompany/centralized-payments");
    expect(screen.queryByRole("button", { name: "Preview reversal" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Preview postings" }));
    await screen.findByText("Balanced in each entity");
    fireEvent.click(screen.getByRole("button", { name: "Preview reversal" }));
    const table = await findTable({ name: "Reversal legs" });
    expect(calls[1].url.pathname).toBe("/api/v1/interco/centralized-payments/reversals:preview");
    expect(calls[1].body).toEqual({ ...(calls[0].body as object), pairId });
    expect(bodyRows(table).map(cells)).toEqual([
      ["P", "1510-DUE-FROM-S", "—", "10000.00", "EUR"],
      ["P", "1100-BANK", "10000.00", "—", "EUR"],
      ["S", "2000-VENDOR", "—", "10000.00", "EUR"],
      ["S", "2510-DUE-TO-P", "10000.00", "—", "EUR"],
    ]);
  });

  it("explains a failed reversal", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/interco/centralized-payments:preview"
        ? { status: 200, body: { pairId, balanced: true, legs } }
        : { status: 422, body: { code: "IC-0805", detail: "no IC accounting matrix entry for P to S" } };
    renderAt("/intercompany/centralized-payments");
    fireEvent.click(screen.getByRole("button", { name: "Preview postings" }));
    await screen.findByText("Balanced in each entity");
    fireEvent.click(screen.getByRole("button", { name: "Preview reversal" }));
    await waitFor(() => expect(screen.getByText("The reversal was not derived")).toBeInTheDocument());
    expect(screen.getByText("The reversal was not derived").closest("[role=alert]")).toHaveTextContent("(IC-0805)");
    expect(screen.queryByRole("table", { name: "Reversal legs" })).not.toBeInTheDocument();
  });
});
