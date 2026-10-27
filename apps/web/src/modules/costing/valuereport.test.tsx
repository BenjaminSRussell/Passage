import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { CostTransaction, TieOut, ValuationLedger, ValueReport } from "./api/client";
import { CostTransactionsPage, filterFromSearch } from "./CostTransactionsPage";
import { paramsFromSearch, parseLedgerBalances, ValueReportPage } from "./ValueReportPage";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const LE = "11111111-1111-4111-8111-111111111111";
const P1 = "22222222-2222-4222-8222-222222222222";
const S1 = "44444444-4444-4444-8444-444444444444";
const S2 = "55555555-5555-4555-8555-555555555555";

const ifrs: ValuationLedger = { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", legalEntityId: LE, ledgerGroupId: S2, code: "IFRS", method: "WEIGHTED_AVG_PERIODIC", currency: "EUR", rateType: "AVERAGE", primary: true, createdAt: "2026-10-01T08:00:00Z" };
const gaap: ValuationLedger = { ...ifrs, id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", code: "USGAAP", method: "FIFO", currency: "USD", primary: false };

const report: ValueReport = {
  legalEntityId: LE,
  asOf: "2026-03-31",
  valuationLedgerCode: "IFRS",
  currency: "EUR",
  groupBy: ["product", "valuationType"],
  rows: [
    { key: { product: P1, valuationType: "DOMESTIC" }, qty: "100", physicalValue: "0.00", financialValue: "1000.00", value: "1000.00" },
    { key: { product: P1, valuationType: "IMPORT" }, qty: "50", physicalValue: "0.00", financialValue: "700.00", value: "700.00" },
  ],
  total: { qty: "150", physicalValue: "0.00", financialValue: "1700.00", value: "1700.00" },
};

const txn: CostTransaction = {
  id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
  sourceKey: "gr-1",
  legalEntityId: LE,
  ledgerEntryId: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
  productId: P1,
  siteId: S1,
  valuationType: "IMPORT",
  kind: "FINANCIAL",
  qty: "100",
  amount: "1400.00",
  currency: "EUR",
  financialDate: "2026-03-05",
  createdAt: "2026-03-05T10:00:00Z",
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

function urls(fetchMock: ReturnType<typeof vi.fn>, method: string, fragment: string): string[] {
  return (fetchMock.mock.calls as unknown as [string, RequestInit][]).filter(([url, init]) => (init?.method ?? "GET") === method && url.includes(fragment)).map(([url]) => url);
}

function at(path: string) {
  const route = (p: string, el: () => React.JSX.Element) => createElement(Route, { path: p, element: createElement(el) });
  return render(
    createElement(
      MemoryRouter,
      { initialEntries: [path] },
      createElement(Routes, null, route("/costing/value-report", ValueReportPage), route("/costing/cost-transactions", CostTransactionsPage)),
    ),
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("parseLedgerBalances", () => {
  it("reads balances keyed by exactly the grouping dimensions, in any column order", () => {
    const parsed = parseLedgerBalances(`balance,site\n1700.00,${S1}\n-12.5,${S2}\n`, ["site"]);
    expect(parsed).toEqual({ ok: true, value: [{ dimensions: { site: S1 }, balance: "1700.00" }, { dimensions: { site: S2 }, balance: "-12.5" }] });
    expect(parseLedgerBalances("balance\n10", [])).toEqual({ ok: true, value: [{ dimensions: {}, balance: "10" }] });
  });

  it("refuses a wrong header, a missing balance line, a short line and a non-number", () => {
    expect(parseLedgerBalances("product,balance\nx,1", ["site"])).toMatchObject({ ok: false, error: expect.stringContaining("site, balance") });
    expect(parseLedgerBalances("site,balance\n", ["site"])).toMatchObject({ ok: false, error: expect.stringContaining("at least one") });
    expect(parseLedgerBalances("site,balance\ns1", ["site"])).toMatchObject({ ok: false, error: "Line 2: expected 2 values." });
    expect(parseLedgerBalances("site,balance\ns1,1e3", ["site"])).toMatchObject({ ok: false, error: expect.stringContaining("Line 2") });
  });
});

describe("paramsFromSearch and filterFromSearch", () => {
  it("default the grouping to site and drop unknown dimensions or bad ids", () => {
    expect(paramsFromSearch(new URLSearchParams(`legalEntityId=${LE}&asOf=2026-03-31`))).toEqual({ legalEntityId: LE, asOf: "2026-03-31", groupBy: ["site"] });
    expect(paramsFromSearch(new URLSearchParams(`legalEntityId=${LE}&asOf=2026-03-31&groupBy=colour,product`))?.groupBy).toEqual(["product"]);
    expect(paramsFromSearch(new URLSearchParams(`legalEntityId=${LE}&asOf=2026-03-31&groupBy=`))?.groupBy).toEqual([]);
    expect(paramsFromSearch(new URLSearchParams("legalEntityId=x&asOf=2026-03-31"))).toBeNull();
    expect(filterFromSearch(new URLSearchParams(`legalEntityId=${LE}&productId=nope&kind=PPV`))).toEqual({ legalEntityId: LE, kind: "PPV" });
    expect(filterFromSearch(new URLSearchParams("kind=PPV"))).toBeNull();
  });
});

describe("ValueReportPage", () => {
  it("asks for a legal entity first, validates the form, then runs the report for the chosen ledger and grouping", async () => {
    const fetchMock = stubRoutes({
      "GET /api/v1/costing/valuation-ledgers": () => json(200, { items: [ifrs, gaap] }),
      "GET /api/v1/costing/value-report": () => json(200, { ...report, valuationLedgerId: gaap.id, valuationLedgerCode: "USGAAP", currency: "USD" }),
    });
    const { container } = at("/costing/value-report");
    expect(screen.getByRole("heading", { name: "Choose a legal entity and a date" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: "nope" } });
    fireEvent.click(screen.getByRole("button", { name: "Run report" }));
    expect(screen.getByText("Enter the legal entity's id (a UUID).")).toBeInTheDocument();
    expect(urls(fetchMock, "GET", "value-report")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("As of date"), { target: { value: "2026-03-31" } });
    const ledger = screen.getByLabelText("Valuation ledger");
    await within(ledger).findByRole("option", { name: "USGAAP (FIFO, USD)" });
    expect(within(ledger).queryByRole("option", { name: /IFRS/ })).toBeNull(); // the primary is "Primary valuation"
    fireEvent.change(ledger, { target: { value: gaap.id } });
    fireEvent.click(screen.getByRole("checkbox", { name: "Site" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Valuation type" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Product" }));
    fireEvent.click(screen.getByRole("button", { name: "Run report" }));

    const table = await findTable({ name: "Inventory value by group" });
    expect(await within(table).findByText("DOMESTIC")).toBeInTheDocument();
    expect(within(table).getAllByText("700.00")).toHaveLength(2); // financial and total value of IMPORT
    expect(within(table).getByRole("columnheader", { name: "Value (USD)" })).toBeInTheDocument();
    expect(screen.getByText("Ledger USGAAP")).toBeInTheDocument();
    const [url] = urls(fetchMock, "GET", "value-report");
    const q = new URL(url, "http://x").searchParams;
    expect([q.get("legalEntityId"), q.get("asOf"), q.get("valuationLedgerId"), q.get("groupBy")]).toEqual([LE, "2026-03-31", gaap.id, "product,valuationType"]);
    expect(pageViolations(container)).toEqual([]);
  });

  it("opens a row's cost transactions", async () => {
    const fetchMock = stubRoutes({
      "GET /api/v1/costing/valuation-ledgers": () => json(200, { items: [ifrs] }),
      "GET /api/v1/costing/value-report": () => json(200, report),
      "GET /api/v1/costing/cost-transactions": () => json(200, { items: [txn] }),
    });
    at(`/costing/value-report?legalEntityId=${LE}&asOf=2026-03-31&groupBy=product,valuationType&siteId=${S1}`);
    const table = await findTable({ name: "Inventory value by group" });
    fireEvent.click(await within(table).findByText("IMPORT"));
    expect(await screen.findByRole("heading", { name: "Cost transactions" })).toBeInTheDocument();
    await waitFor(() => expect(urls(fetchMock, "GET", "cost-transactions")).toHaveLength(1));
    const q = new URL(urls(fetchMock, "GET", "cost-transactions")[0], "http://x").searchParams;
    expect([q.get("legalEntityId"), q.get("productId"), q.get("siteId"), q.get("asOf")]).toEqual([LE, P1, S1, "2026-03-31"]);
    expect(await screen.findByText("1400.00 EUR")).toBeInTheDocument();
  });

  it("shows the empty report, and an error with a retry", async () => {
    let fail = true;
    stubRoutes({
      "GET /api/v1/costing/valuation-ledgers": () => json(200, { items: [] }),
      "GET /api/v1/costing/value-report": () =>
        fail ? json(422, { title: "Unprocessable", code: "CST-RPT-004", detail: "the valuation ledger values in USD but holds cost transactions in EUR" }) : json(200, { ...report, rows: [], total: { qty: "0", physicalValue: "0.00", financialValue: "0.00", value: "0.00" } }),
    });
    at(`/costing/value-report?legalEntityId=${LE}&asOf=2026-03-31`);
    expect(await findAlert()).toHaveTextContent("CST-RPT-004");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No stock is valued at this date")).toBeInTheDocument();
  });

  it("ties the report out to pasted ledger balances", async () => {
    const tie: TieOut = {
      legalEntityId: LE,
      asOf: "2026-03-31",
      currency: "EUR",
      groupBy: ["site"],
      rows: [
        { key: { site: S1 }, inventory: "1700.00", ledger: "1650.00", diff: "50.00" },
        { key: { site: S2 }, inventory: "50.00", ledger: "50.00", diff: "0.00" },
      ],
      differences: 1,
      totalDiff: "50.00",
      ties: false,
    };
    const fetchMock = stubRoutes({
      "GET /api/v1/costing/valuation-ledgers": () => json(200, { items: [] }),
      "GET /api/v1/costing/value-report": () => json(200, { ...report, groupBy: ["site"], rows: [{ ...report.rows[0], key: { site: S1 } }] }),
      "POST /api/v1/costing/value-report:tie-out": () => json(200, tie),
    });
    const { container } = at(`/costing/value-report?legalEntityId=${LE}&asOf=2026-03-31&groupBy=site`);
    const balances = await screen.findByLabelText("Ledger balances (CSV)");
    expect(balances).toHaveValue("site,balance\n");
    fireEvent.click(screen.getByRole("button", { name: "Tie out" }));
    expect(await screen.findByText("Enter at least one ledger balance below the header.")).toBeInTheDocument();
    expect(urls(fetchMock, "POST", "tie-out")).toHaveLength(0);

    fireEvent.change(balances, { target: { value: `site,balance\n${S1},1650.00\n${S2},50.00` } });
    fireEvent.click(screen.getByRole("button", { name: "Tie out" }));
    expect(await screen.findByText("Does not tie to the ledger")).toBeInTheDocument();
    expect(screen.getByText(/1 group differs; net difference 50.00 EUR/)).toBeInTheDocument();
    const [, init] = (fetchMock.mock.calls as unknown as [string, RequestInit][]).find(([u]) => u.includes("tie-out"))!;
    expect(JSON.parse(String(init.body))).toMatchObject({ legalEntityId: LE, asOf: "2026-03-31", groupBy: ["site"], glBalances: [{ dimensions: { site: S1 }, balance: "1650.00" }, { dimensions: { site: S2 }, balance: "50.00" }] });
    expect(pageViolations(container)).toEqual([]);
  });
});

describe("CostTransactionsPage", () => {
  it("asks for a legal entity, then lists, filters and pages the cost transactions", async () => {
    const fetchMock = stubRoutes({
      "GET /api/v1/costing/cost-transactions": (url) =>
        url.includes("cursor=") ? json(200, { items: [{ ...txn, id: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", kind: "PPV", amount: "50.00" }] }) : json(200, { items: [txn], nextCursor: "c2" }),
    });
    const { container } = at("/costing/cost-transactions");
    expect(screen.getByRole("heading", { name: "Choose a legal entity" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Show cost transactions" }));
    expect(screen.getByText("Enter the legal entity's id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter a UUID, or leave it empty.")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "FINANCIAL" } });
    fireEvent.click(screen.getByRole("button", { name: "Show cost transactions" }));
    const table = await findTable({ name: "Cost transactions" });
    expect(await within(table).findByText("1400.00 EUR")).toBeInTheDocument();
    expect(within(table).getByText(txn.ledgerEntryId!)).toBeInTheDocument();
    const q = new URL(urls(fetchMock, "GET", "cost-transactions")[0], "http://x").searchParams;
    expect([q.get("legalEntityId"), q.get("kind")]).toEqual([LE, "FINANCIAL"]);

    fireEvent.click(screen.getByRole("button", { name: "Load more cost transactions" }));
    expect(await within(table).findByText("PPV (not in stock value)")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more cost transactions" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty list, and an error with a retry", async () => {
    let fail = true;
    stubRoutes({
      "GET /api/v1/costing/cost-transactions": () => (fail ? json(404, { title: "Not Found", code: "CST-VLG-005", detail: "the legal entity has no such valuation ledger" }) : json(200, { items: [] })),
    });
    at(`/costing/cost-transactions?legalEntityId=${LE}`);
    expect(await findAlert()).toHaveTextContent("CST-VLG-005");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No cost transactions match")).toBeInTheDocument();
  });
});
