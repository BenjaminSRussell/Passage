import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findAlert, findTable } from "../../test-utils";
import type { Agreement, CreditExposureRow } from "./api/client";
import sales from "./module";

const LE = "11111111-0000-4000-8000-000000000001";
const OVER = "22222222-0000-4000-8000-000000000002";
const WARN = "33333333-0000-4000-8000-000000000003";
const OPEN = "44444444-0000-4000-8000-000000000004";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown };
let handler: (url: URL) => Reply;
let calls: URL[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      const url = new URL(input, "http://localhost");
      calls.push(url);
      const { status, body } = handler(url);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string) {
  return render(
    <PrincipalProvider duties={["sales.order.read"]}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {sales.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const row = (customerId: string, patch: Partial<CreditExposureRow>): CreditExposureRow => ({
  legalEntityId: LE,
  customerId,
  currency: "USD",
  limitType: "BALANCE_ALL",
  limit: "10000",
  threshold: "11000",
  exposure: { openAr: "8000", unapplied: "500", deliveredNotInvoiced: "700", openOrders: "1000" },
  balance: "9200",
  utilizationPct: "92",
  status: "WITHIN",
  understated: false,
  ...patch,
});

const report = {
  items: [
    row(OVER, { balance: "12000", utilizationPct: "120", status: "OVER_TOLERANCE" }),
    row(WARN, { balance: "10500", utilizationPct: "105", status: "OVER_LIMIT", understated: true }),
    row(OPEN, { limitType: "NONE", limit: "0", threshold: "0", utilizationPct: undefined, status: "NO_LIMIT" }),
  ],
  total: 3,
  truncated: false,
};

describe("credit exposure report", () => {
  it("is in the sales nav on the read duty", () => {
    expect(sales.nav?.find((n) => n.to === "/sales/credit-exposure")?.duty).toBe("sales.order.read");
    expect(sales.routes.find((r) => r.path === "/sales/credit-exposure")).toBeDefined();
  });

  it("ranks customers by use of the limit and marks the status", async () => {
    handler = () => ({ status: 200, body: report });
    const { container } = renderAt("/sales/credit-exposure");
    const table = await findTable({ name: "Credit exposure" });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(3);
    expect(within(rows[0]!).getByText(OVER)).toBeInTheDocument();
    expect(within(rows[0]!).getByText("120%")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("Over tolerance")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("Over limit")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("10500 USD or more")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("No limit")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("—")).toBeInTheDocument();
    expect(calls[0]?.pathname).toBe("/api/v1/sales/credit-exposure");
    expect(pageViolations(container)).toEqual([]);
  });

  it("sends the filters", async () => {
    handler = () => ({ status: 200, body: report });
    renderAt("/sales/credit-exposure");
    await findTable({ name: "Credit exposure" });
    fireEvent.change(screen.getByLabelText(/Status/), { target: { value: "OVER_LIMIT" } });
    fireEvent.change(screen.getByLabelText(/Used at least/), { target: { value: "90.5" } });
    fireEvent.change(screen.getByLabelText(/^Customer/), { target: { value: WARN } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(calls).toHaveLength(2));
    const q = calls[1]!.searchParams;
    expect(q.get("status")).toBe("OVER_LIMIT");
    expect(q.get("minUtilizationPct")).toBe("90.5");
    expect(q.get("customerId")).toBe(WARN);
  });

  it("refuses a bad filter without calling the service", async () => {
    handler = () => ({ status: 200, body: report });
    renderAt("/sales/credit-exposure");
    await findTable({ name: "Credit exposure" });
    fireEvent.change(screen.getByLabelText(/Used at least/), { target: { value: "abc" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(await screen.findByText("Enter a percent such as 80 or 92.5.")).toBeInTheDocument();
    expect(calls).toHaveLength(1);
  });

  it("warns when the ranking is partial and says how many are shown", async () => {
    handler = () => ({ status: 200, body: { ...report, total: 1500, truncated: true } });
    renderAt("/sales/credit-exposure");
    expect(await screen.findByText("Partial ranking")).toBeInTheDocument();
    expect(await screen.findByText("Showing 3 of 1500 customers.")).toBeInTheDocument();
  });

  it("shows the empty state", async () => {
    handler = () => ({ status: 200, body: { items: [], total: 0, truncated: false } });
    renderAt("/sales/credit-exposure");
    expect(await screen.findByText("No customers match")).toBeInTheDocument();
  });

  it("shows the service's error and retries", async () => {
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "boom" } });
    renderAt("/sales/credit-exposure");
    const alert = await findAlert();
    expect(within(alert).getByText(/COM-0500/)).toBeInTheDocument();
    handler = () => ({ status: 200, body: report });
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    await findTable({ name: "Credit exposure" });
  });
});

describe("agreement usage and expiry filters", () => {
  const agreement = (id: string, number: string, usedPct: string | undefined): Agreement =>
    ({
      id,
      legalEntityId: LE,
      agreementNumber: number,
      customerId: OVER,
      invoiceAccountId: OVER,
      productId: WARN,
      unit: "EA",
      unitPrice: "12",
      currency: "USD",
      commitmentQty: "1000",
      releasedQty: "750",
      remainingQty: "250",
      usedPct,
      status: "ACTIVE",
      validTo: "2027-06-30",
      version: 2,
      createdBy: "u",
      createdAt: "2027-01-01T00:00:00Z",
      updatedAt: "2027-01-01T00:00:00Z",
    }) as Agreement;

  it("shows the used percent and sends the expiry and usage filters", async () => {
    handler = () => ({ status: 200, body: { items: [agreement(OPEN, "SA-0000001", "75"), agreement(WARN, "SA-0000002", undefined)] } });
    renderAt("/sales/agreements");
    const table = await findTable({ name: "Sales agreements" });
    expect(within(table).getByText("75%")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Ending by/), { target: { value: "2027-06-30" } });
    fireEvent.change(screen.getByLabelText(/Used at least/), { target: { value: "70" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(calls[1]!.searchParams.get("expiringBy")).toBe("2027-06-30");
    expect(calls[1]!.searchParams.get("minUsedPct")).toBe("70");
  });

  it("refuses a bad usage percent", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/sales/agreements");
    await screen.findByText("No sales agreements yet");
    fireEvent.change(screen.getByLabelText(/Used at least/), { target: { value: "-3" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(await screen.findByText("Enter a percent such as 80 or 92.5.")).toBeInTheDocument();
    expect(calls).toHaveLength(1);
  });
});
