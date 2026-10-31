import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { validateQuota } from "./AllocationQuotasPage";
import sales from "./module";

const LE = "11111111-0000-4000-8000-000000000001";
const CUSTOMER = "22222222-0000-4000-8000-000000000002";
const PRODUCT = "33333333-0000-4000-8000-000000000003";
const ORDER_ID = "66666666-0000-4000-8000-000000000006";
const QUOTA_ID = "99999999-0000-4000-8000-000000000009";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
type Handler = (url: URL, init: RequestInit) => Reply | Promise<Reply>;

let handler: Handler;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = await handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(status === 204 ? null : JSON.stringify(body), { status, headers });
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
        {sales.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
        <Route path="*" element={<p>Elsewhere</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

function fill(label: string | RegExp, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

const quota = {
  id: QUOTA_ID,
  legalEntityId: LE,
  productId: PRODUCT,
  periodStart: "2027-03-01",
  periodEnd: "2027-03-31",
  quotaQty: "100",
  consumedQty: "60",
  remainingQty: "40",
  version: 3,
  createdBy: "demand-planner",
  createdAt: "2027-02-20T09:00:00Z",
  updatedAt: "2027-03-02T09:00:00Z",
  consumptions: [
    { id: "c1", orderId: ORDER_ID, lineId: "l1", lineNo: 1, quantity: "60", consumedAt: "2027-03-02T09:00:00Z" },
    { id: "c0", orderId: ORDER_ID, lineId: "l2", lineNo: 2, quantity: "5", consumedAt: "2027-03-01T09:00:00Z", returnedAt: "2027-03-01T10:00:00Z" },
  ],
};

describe("Allocation quotas", () => {
  it("lists quota, consumed, remaining and share used, and passes the filter", async () => {
    handler = () => ({ status: 200, body: { items: [{ ...quota, customerId: CUSTOMER }, { ...quota, id: "x2", periodStart: "2027-04-01", periodEnd: "2027-04-30", consumedQty: "0", remainingQty: "100" }] } });
    const { container } = renderAt("/sales/allocation-quotas");
    const table = await findTable({ name: "Allocation quotas" });
    await waitFor(() => expect(within(table).getByText("2027-03-01 to 2027-03-31")).toBeInTheDocument());
    expect(within(table).getByText("60.0%")).toBeInTheDocument();
    expect(within(table).getByText("Every customer")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fill("Product", PRODUCT);
    fill("Period containing", "2027-03-15");
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("productId")).toBe(PRODUCT));
    expect(calls.at(-1)?.url.searchParams.get("onDate")).toBe("2027-03-15");
  });

  it("explains an empty list, shows loading and the problem code with retry", async () => {
    let resolve: (r: Reply) => void = () => undefined;
    handler = () => new Promise<Reply>((r) => (resolve = r));
    renderAt("/sales/allocation-quotas");
    expect(await screen.findByRole("table", { name: "Allocation quotas" })).toHaveAttribute("aria-busy", "true");
    resolve({ status: 200, body: { items: [] } });
    expect(await screen.findByRole("heading", { name: "No allocation quotas" })).toBeInTheDocument();
  });

  it("shows the problem code when the list fails and recovers on retry", async () => {
    let fail = true;
    handler = () => (fail ? { status: 503, body: { code: "COM-0503", detail: "store unavailable" } } : { status: 200, body: { items: [quota] } });
    renderAt("/sales/allocation-quotas");
    expect(await screen.findByText(/COM-0503/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("2027-03-01 to 2027-03-31")).toBeInTheDocument();
  });

  it("refuses a malformed filter without calling the service", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/sales/allocation-quotas");
    await screen.findByRole("heading", { name: "No allocation quotas" });
    const before = calls.length;
    fill("Product", "FG-1");
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    expect(screen.getByText("Enter a product UUID.")).toBeInTheDocument();
    expect(calls.length).toBe(before);
  });

  it("the new quota form validates, posts with an Idempotency-Key and opens the quota", async () => {
    handler = (_url, init) => {
      if (init.method === "POST") return { status: 201, body: quota, etag: '"3"' };
      return { status: 200, body: quota, etag: '"3"' };
    };
    const { container } = renderAt("/sales/allocation-quotas/new");
    fireEvent.click(await screen.findByRole("button", { name: "Save quota" }));
    expect(screen.getByText("Enter the legal entity's UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);

    fill("Legal entity", LE);
    fill("Product", PRODUCT);
    fill("Period start", "2027-03-01");
    fill("Period end", "2027-03-31");
    fill("Quota quantity", "100");
    fireEvent.click(screen.getByRole("button", { name: "Save quota" }));
    expect(await screen.findByRole("heading", { name: /Allocation quota 2027-03-01 to 2027-03-31/ })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe("/api/v1/sales/allocation-quotas");
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post?.body).toEqual({ legalEntityId: LE, productId: PRODUCT, periodStart: "2027-03-01", periodEnd: "2027-03-31", quotaQty: "100" });
  });

  it("explains an overlapping period from the service", async () => {
    handler = () => ({ status: 409, body: { code: "SLS-0035", detail: "an allocation quota for this product already covers part of the period" } });
    renderAt("/sales/allocation-quotas/new");
    fill("Legal entity", LE);
    fill("Product", PRODUCT);
    fill("Customer", CUSTOMER);
    fill("Period start", "2027-03-01");
    fill("Period end", "2027-03-31");
    fill("Quota quantity", "100");
    fireEvent.click(await screen.findByRole("button", { name: "Save quota" }));
    expect(await screen.findByText("The period overlaps another quota")).toBeInTheDocument();
    expect(screen.getByText(/SLS-0035/)).toBeInTheDocument();
  });

  it("validateQuota mirrors the service rules", () => {
    const base = { legalEntityId: LE, productId: PRODUCT, customerId: "", periodStart: "2027-03-01", periodEnd: "2027-03-31", quotaQty: "100" };
    expect(validateQuota(base).input).toEqual({ legalEntityId: LE, productId: PRODUCT, periodStart: "2027-03-01", periodEnd: "2027-03-31", quotaQty: "100" });
    expect(validateQuota({ ...base, customerId: CUSTOMER }).input?.customerId).toBe(CUSTOMER);
    expect(validateQuota({ ...base, customerId: "C1" }).errors.customerId).toMatch(/UUID/);
    expect(validateQuota({ ...base, periodEnd: "2027-02-28" }).errors.periodEnd).toBe("The end must not be before the start.");
    expect(validateQuota({ ...base, quotaQty: "0" }).errors.quotaQty).toMatch(/above 0/);
    expect(validateQuota({ ...base, quotaQty: "1.1234567" }).errors.quotaQty).toMatch(/6 decimals/);
  });
});

describe("Allocation quota detail", () => {
  it("shows the quota, its draws including returned ones, and changes the quantity at the ETag", async () => {
    handler = (_url, init) =>
      init.method === "PATCH"
        ? { status: 200, body: { ...quota, quotaQty: "120", remainingQty: "60", version: 4 }, etag: '"4"' }
        : { status: 200, body: quota, etag: '"3"' };
    const { container } = renderAt(`/sales/allocation-quotas/${QUOTA_ID}`);
    const draws = await findTable({ name: "Draws" });
    expect(within(draws).getByText("66666666 line 1")).toBeInTheDocument();
    expect(within(draws).getByText("2027-03-01 10:00 UTC")).toBeInTheDocument();
    expect(screen.getByText("Remaining")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fill("New quota quantity", "abc");
    fireEvent.click(screen.getByRole("button", { name: "Change quota" }));
    expect(screen.getByText("Enter a quantity above 0 with up to 6 decimals.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PATCH")).toBe(false);

    fill("New quota quantity", "120");
    fireEvent.click(screen.getByRole("button", { name: "Change quota" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH")).toBe(true));
    const patch = calls.find((c) => c.method === "PATCH");
    expect(patch?.headers.get("If-Match")).toBe('"3"');
    expect(patch?.body).toEqual({ quotaQty: "120" });
    expect(await screen.findByText("The quota is now 120.")).toBeInTheDocument();
  });

  it("explains a change below the consumed quantity", async () => {
    handler = (_url, init) =>
      init.method === "PATCH"
        ? { status: 409, body: { code: "SLS-0036", detail: "quota cannot be set below the consumed quantity (60 consumed)" } }
        : { status: 200, body: quota, etag: '"3"' };
    renderAt(`/sales/allocation-quotas/${QUOTA_ID}`);
    await findTable({ name: "Draws" });
    fill("New quota quantity", "50");
    fireEvent.click(screen.getByRole("button", { name: "Change quota" }));
    expect(await screen.findByText("The quota cannot go below what is consumed")).toBeInTheDocument();
    expect(screen.getByText(/SLS-0036/)).toBeInTheDocument();
  });

  it("says when nothing is drawn, and shows the problem when the quota is missing", async () => {
    handler = () => ({ status: 200, body: { ...quota, consumptions: [], consumedQty: "0", remainingQty: "100" }, etag: '"1"' });
    const first = renderAt(`/sales/allocation-quotas/${QUOTA_ID}`);
    expect(await screen.findByRole("heading", { name: "Nothing drawn yet" })).toBeInTheDocument();
    first.unmount();
    handler = () => ({ status: 404, body: { code: "SLS-0108", detail: "allocation quota not found" } });
    renderAt(`/sales/allocation-quotas/${QUOTA_ID}`);
    expect(await screen.findByText(/SLS-0108/)).toBeInTheDocument();
  });
});

describe("Order confirmation with allocation", () => {
  it("is reported as a warning when a line moved to a later period", async () => {
    const order = {
      id: ORDER_ID, legalEntityId: LE, orderNumber: "SO-0000042", customerId: CUSTOMER, invoiceAccountId: CUSTOMER, orderType: "ORDER", status: "OPEN", currency: "USD",
      requestedShipDate: "2027-03-25", channel: "UI", netTotal: "500", version: 1, holds: [], listingOverrides: [],
      lines: [{ id: "l1", lineNo: 1, productId: PRODUCT, quantity: "50", unit: "EA", siteId: LE, warehouseId: LE, unitPrice: "10", discPct: "0", lineNet: "500", requestedShipDate: "2027-03-25", lineStatus: "OPEN" }],
      createdAt: "2027-03-01T09:00:00Z", updatedAt: "2027-03-01T09:00:00Z",
    };
    handler = (_url, init) =>
      init.method === "POST"
        ? {
            status: 200,
            body: {
              ...order, status: "CONFIRMED", confirmed: true, version: 2,
              lines: [{ ...order.lines[0], confirmedShipDate: "2027-04-01" }],
              warnings: [{ code: "SLS-0013", lineNo: 1, message: "Allocation for the requested period is exhausted; line 1 is confirmed for 2027-04-01 (source ALLOCATION)" }],
            },
            etag: '"2"',
          }
        : { status: 200, body: order, etag: '"1"' };
    renderAt(`/sales/orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Confirm order" }));
    expect(await screen.findByText(/confirmed for 2027-04-01 \(source ALLOCATION\)/)).toBeInTheDocument();
  });

  it("says a CREDIT release left the order unconfirmed when allocation could not cover it", async () => {
    const hold = { id: "h1", holdType: "CREDIT", reasonCode: "CR-LIMIT", status: "ACTIVE", orderAmount: "500", appliedBy: "sales-rep", appliedAt: "2027-03-01T09:00:00Z" };
    const order = {
      id: ORDER_ID, legalEntityId: LE, orderNumber: "SO-0000043", customerId: CUSTOMER, invoiceAccountId: CUSTOMER, orderType: "ORDER", status: "OPEN", currency: "USD",
      requestedShipDate: "2027-03-25", channel: "UI", netTotal: "500", version: 2, holds: [hold], listingOverrides: [],
      lines: [{ id: "l1", lineNo: 1, productId: PRODUCT, quantity: "50", unit: "EA", siteId: LE, warehouseId: LE, unitPrice: "10", discPct: "0", lineNet: "500", requestedShipDate: "2027-03-25", lineStatus: "OPEN" }],
      createdAt: "2027-03-01T09:00:00Z", updatedAt: "2027-03-01T09:00:00Z",
    };
    handler = (_url, init) =>
      init.method === "POST"
        ? {
            status: 200,
            body: {
              ...order, confirmed: false, version: 3,
              holds: [{ ...hold, status: "RELEASED", releasedBy: "credit-manager" }],
              warnings: [{ code: "SLS-0013", message: "Allocation for line 1 (product x) from 2027-03-25 is exhausted and no later period has 50 left; request a quota change" }],
            },
            etag: '"3"',
          }
        : { status: 200, body: order, etag: '"2"' };
    renderAt(`/sales/orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Release Credit hold CR-LIMIT" }));
    expect(await screen.findByText(/hold released, but the order is not confirmed\. Allocation for line 1/)).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.url.pathname).toMatch(/holds\/h1:release$/);
  });
});
