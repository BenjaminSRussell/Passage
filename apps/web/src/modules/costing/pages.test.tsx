import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { AgingPage, parseBounds } from "./AgingPage";
import { describeError, CostingApiError } from "./api/client";
import { SettlementPreview } from "./pages";
import { validateLedgers, ValuationLedgersPage } from "./ValuationLedgersPage";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

function stubFetch(status: number, body: unknown) {
  const fetchMock = vi.fn(async () => new Response(JSON.stringify(body), { status }));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function sentBody(fetchMock: ReturnType<typeof vi.fn>): Record<string, unknown> {
  const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
  return JSON.parse(init.body as string) as Record<string, unknown>;
}

function sentUrl(fetchMock: ReturnType<typeof vi.fn>): string {
  return (fetchMock.mock.calls[0] as unknown as [string])[0];
}

const page = (el: () => React.JSX.Element) => render(createElement(MemoryRouter, null, createElement(el)));

afterEach(() => vi.unstubAllGlobals());

describe("SettlementPreview", () => {
  it("shows the empty state before a run and passes the page accessibility checks", () => {
    const { container } = page(SettlementPreview);
    expect(screen.getByRole("heading", { name: "No preview run yet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("sends the default valuation type and lists remaining value by type", async () => {
    const fetchMock = stubFetch(200, {
      method: "WEIGHTED_AVG_RUNNING",
      currency: "EUR",
      issueCosts: { I1: "700.00" },
      settlements: [{ seq: 1, issueId: "I1", pool: "AVG:RUNNING", qty: "50", value: "700.00" }],
      exceptions: [],
      remainingQty: "150",
      remainingValue: "1700.00",
      remainingByValuationType: { IMPORT: "700.00", DOMESTIC: "1000.00" },
    });
    page(SettlementPreview);
    fireEvent.change(screen.getByLabelText("Default valuation type"), { target: { value: " DOMESTIC " } });
    fireEvent.click(screen.getByRole("button", { name: "Preview settlement" }));
    const table = await findTable({ name: "Remaining value by valuation type" });
    const rows = within(table).getAllByRole("row");
    expect(rows[1]).toHaveTextContent("DOMESTIC");
    expect(rows[1]).toHaveTextContent("1000.00");
    expect(rows[2]).toHaveTextContent("IMPORT");
    expect(sentBody(fetchMock)).toMatchObject({ defaultValuationType: "DOMESTIC" });
    expect(screen.getByRole("table", { name: "Settlements" })).toHaveTextContent("AVG:RUNNING");
    expect(screen.queryByRole("heading", { name: "Exceptions" })).toBeNull();
  });

  it("omits the valuation-type table for an unsplit product", async () => {
    stubFetch(200, { method: "FIFO", currency: "EUR", issueCosts: {}, settlements: [], exceptions: [], remainingQty: "0", remainingValue: "0.00" });
    page(SettlementPreview);
    fireEvent.click(screen.getByRole("button", { name: "Preview settlement" }));
    expect(await findTable({ name: "Issue costs" })).toHaveTextContent("No issues to cost");
    expect(screen.queryByRole("table", { name: "Remaining value by valuation type" })).toBeNull();
  });
});

describe("validateLedgers", () => {
  const ledger = { id: "P", method: "FIFO" as const, currency: "EUR", rate: "1", primary: true, standardUnitCost: "" };

  it("accepts the AC-20.11 pair", () => {
    expect(validateLedgers([ledger, { ...ledger, id: "G", currency: "USD", rate: "1.10", primary: false }])).toEqual({});
  });

  it("requires exactly one primary, distinct ids, a positive rate and rate 1 on the primary", () => {
    expect(validateLedgers([{ ...ledger, primary: false }]).primary).toMatch(/exactly one/);
    expect(validateLedgers([ledger, { ...ledger }]).primary).toMatch(/exactly one/);
    expect(validateLedgers([ledger, { ...ledger, primary: false }])["ledgers[1].id"]).toMatch(/own id/);
    expect(validateLedgers([ledger, { ...ledger, id: "G", rate: "0", primary: false }])["ledgers[1].rate"]).toMatch(/above 0/);
    expect(validateLedgers([{ ...ledger, rate: "1.2" }])["ledgers[0].rate"]).toMatch(/rate is 1/);
    expect(validateLedgers([{ ...ledger, currency: "eu" }])["ledgers[0].currency"]).toBeDefined();
    expect(validateLedgers([{ ...ledger, method: "STANDARD" }])["ledgers[0].standardUnitCost"]).toBeDefined();
  });
});

describe("ValuationLedgersPage", () => {
  const response = {
    ledgers: [
      { id: "IFRS", currency: "EUR", primary: true, issueCosts: { "iss-1": "1650.00" }, remainingQty: "50", remainingValue: "550.00", settlements: 1, exceptions: 0 },
      { id: "GROUP", currency: "USD", primary: false, issueCosts: { "iss-1": "1760.00" }, remainingQty: "50", remainingValue: "660.00", settlements: 2, exceptions: 0 },
    ],
  };

  it("previews the AC-20.11 ledgers and compares issue costs side by side", async () => {
    const fetchMock = stubFetch(200, response);
    const { container } = page(ValuationLedgersPage);
    expect(screen.getByRole("heading", { name: "No ledger preview yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Preview ledgers" }));
    const costs = await findTable({ name: "Issue cost by ledger" });
    expect(within(costs).getByRole("columnheader", { name: "GROUP (USD)" })).toBeInTheDocument();
    expect(within(costs).getAllByRole("row")[1]).toHaveTextContent(/iss-1.*1650\.00.*1760\.00/);
    expect(screen.getByRole("table", { name: "Ledger results" })).toHaveTextContent("Primary");
    expect(sentUrl(fetchMock)).toBe("/api/v1/costing/valuation-ledgers:preview");
    expect(sentBody(fetchMock)).toMatchObject({
      ledgers: [
        { id: "IFRS", method: "WEIGHTED_AVG_PERIODIC", currency: "EUR", rate: "1", primary: true },
        { id: "GROUP", method: "FIFO", currency: "USD", rate: "1.10", primary: false },
      ],
    });
    expect(pageViolations(container)).toEqual([]);
  });

  it("refuses two primaries on the client without calling the API", async () => {
    const fetchMock = stubFetch(200, response);
    page(ValuationLedgersPage);
    fireEvent.click(screen.getByLabelText("Primary ledger 2"));
    fireEvent.click(screen.getByRole("button", { name: "Preview ledgers" }));
    expect(await screen.findByText("Mark exactly one ledger as primary; 2 are marked.")).toBeInTheDocument();
    // The second ledger now claims to be primary at 1.10, which the rate rule also flags.
    expect(screen.getByText("The primary ledger's rate is 1.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("adds and removes ledgers", () => {
    page(ValuationLedgersPage);
    fireEvent.click(screen.getByRole("button", { name: "Add ledger" }));
    expect(screen.getByRole("group", { name: "Ledger 3" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Remove ledger 3" }));
    expect(screen.queryByRole("group", { name: "Ledger 3" })).toBeNull();
  });

  it("shows the service's stable code when it refuses the ledger set", async () => {
    stubFetch(422, { title: "Unprocessable Entity", code: "CST-VLG-001", detail: "exactly one primary valuation ledger is required, got 0" });
    page(ValuationLedgersPage);
    fireEvent.click(screen.getByRole("button", { name: "Preview ledgers" }));
    expect(await findAlert()).toHaveTextContent("exactly one primary valuation ledger is required");
    expect(screen.queryByRole("table", { name: "Ledger results" })).toBeNull();
  });
});

describe("parseBounds", () => {
  it("reads ascending whole days and treats empty as the defaults", () => {
    expect(parseBounds("30, 90,180")).toEqual({ ok: true, value: [30, 90, 180] });
    expect(parseBounds("  ")).toEqual({ ok: true });
    expect(parseBounds("90, 30").ok).toBe(false);
    expect(parseBounds("30, 30").ok).toBe(false);
    expect(parseBounds("7.5").ok).toBe(false);
    expect(parseBounds(Array.from({ length: 13 }, (_, i) => String(i + 1)).join(",")).ok).toBe(false);
  });
});

describe("AgingPage", () => {
  const response = {
    method: "WEIGHTED_AVG_RUNNING",
    currency: "EUR",
    asOf: "2026-01-31",
    buckets: [
      { label: "0-30", qty: "150", value: "1700.00" },
      { label: "> 30", qty: "0", value: "0.00" },
    ],
    totalQty: "150",
    totalValue: "1700.00",
    remainingValue: "1700.00",
    tiesToRemaining: true,
    valuationTypes: [
      { valuationType: "DOMESTIC", buckets: [{ label: "0-30", qty: "100", value: "1000.00" }], totalQty: "100", totalValue: "1000.00", remainingValue: "1000.00" },
      { valuationType: "IMPORT", buckets: [{ label: "0-30", qty: "50", value: "700.00" }], totalQty: "50", totalValue: "700.00", remainingValue: "700.00" },
    ],
  };

  it("runs the aging with custom bounds and shows the buckets, the tie-out and each valuation type", async () => {
    const fetchMock = stubFetch(200, response);
    const { container } = page(AgingPage);
    expect(screen.getByRole("heading", { name: "No aging run yet" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Bucket bounds"), { target: { value: "30" } });
    fireEvent.click(screen.getByRole("button", { name: "Run aging" }));
    const buckets = await findTable({ name: "Aging buckets" });
    expect(within(buckets).getAllByRole("row")[1]).toHaveTextContent(/0-30.*150.*1700\.00/);
    expect(screen.getByText("The buckets tie to the remaining value of 1700.00 EUR.")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Aging buckets for IMPORT" })).toHaveTextContent("700.00");
    expect(screen.getByRole("list", { name: "Aging totals" })).toHaveTextContent("Total value 1700.00 EUR");
    expect(sentUrl(fetchMock)).toBe("/api/v1/costing/aging:preview");
    expect(sentBody(fetchMock)).toMatchObject({ asOf: "2026-01-31", bucketBounds: [30], method: "FIFO" });
    expect(pageViolations(container)).toEqual([]);
  });

  it("sends no bucketBounds when the field is empty", async () => {
    const fetchMock = stubFetch(200, { ...response, valuationTypes: undefined });
    page(AgingPage);
    fireEvent.change(screen.getByLabelText("Bucket bounds"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Run aging" }));
    await findTable({ name: "Aging buckets" });
    expect(sentBody(fetchMock)).not.toHaveProperty("bucketBounds");
    expect(screen.queryByRole("table", { name: "Aging buckets for IMPORT" })).toBeNull();
  });

  it("flags buckets that do not tie to the remaining value", async () => {
    stubFetch(200, { ...response, totalValue: "1699.99", tiesToRemaining: false, valuationTypes: undefined });
    page(AgingPage);
    fireEvent.click(screen.getByRole("button", { name: "Run aging" }));
    expect(await screen.findByText("The buckets total 1699.99 but the remaining value is 1700.00 EUR.")).toBeInTheDocument();
  });

  it("validates bounds on the client and shows field errors from the service", async () => {
    const fetchMock = stubFetch(400, {
      title: "Bad Request",
      code: "COM-0001",
      detail: "request failed validation",
      errors: { "transactions[2].financialDate": "is after asOf" },
    });
    page(AgingPage);
    fireEvent.change(screen.getByLabelText("Bucket bounds"), { target: { value: "90, 30" } });
    fireEvent.click(screen.getByRole("button", { name: "Run aging" }));
    expect(await findAlert()).toHaveTextContent("Each bound must be larger than the one before it.");
    expect(fetchMock).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("Bucket bounds"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Run aging" }));
    expect(await findAlert()).toHaveTextContent("transactions[2].financialDate is after asOf");
    expect(screen.queryByRole("table", { name: "Aging buckets" })).toBeNull();
  });
});

describe("describeError", () => {
  it("falls back for non-API errors", () => {
    expect(describeError(new Error("boom"), "The preview failed.")).toBe("The preview failed.");
    expect(describeError(new CostingApiError({ title: "x", status: 422, code: "CST-VAL-001", detail: "name a type" }), "f")).toBe("name a type");
  });
});
