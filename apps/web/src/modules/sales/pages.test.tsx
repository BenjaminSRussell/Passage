import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { setLookupFetcher } from "../../shell/lookup/entities";
import { promiseSummary, validateAtp } from "./AtpCheckPage";
import { blankProfile, validateProfile } from "./CreditProfileFormPage";
import sales from "./module";
import { emptyLine, validateOrder } from "./NewOrderPage";

const LE = "11111111-0000-4000-8000-000000000001";
const CUSTOMER = "22222222-0000-4000-8000-000000000002";
const PRODUCT = "33333333-0000-4000-8000-000000000003";
const SITE = "44444444-0000-4000-8000-000000000004";
const GROUP = "88888888-0000-4000-8000-000000000008";
const WAREHOUSE = "55555555-0000-4000-8000-000000000005";
const ORDER_ID = "66666666-0000-4000-8000-000000000006";
const HOLD_ID = "77777777-0000-4000-8000-000000000007";

// The shell supplies the h1, landmarks and skip link; a page rendered alone is
// checked for everything else.
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
      return new Response(JSON.stringify(body), { status, headers });
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

const summary = {
  id: ORDER_ID,
  legalEntityId: LE,
  orderNumber: "SO-0000042",
  customerId: CUSTOMER,
  orderType: "ORDER",
  status: "OPEN",
  currency: "USD",
  requestedShipDate: "2027-03-04",
  channel: "UI",
  netTotal: "3000",
  lineCount: 1,
  activeHolds: ["CREDIT"],
  version: 2,
  createdAt: "2027-03-01T09:00:00Z",
};

function order(overrides: Record<string, unknown> = {}) {
  return {
    id: ORDER_ID,
    legalEntityId: LE,
    orderNumber: "SO-0000042",
    customerId: CUSTOMER,
    invoiceAccountId: CUSTOMER,
    orderType: "ORDER",
    status: "OPEN",
    currency: "USD",
    requestedShipDate: "2027-03-04",
    channel: "UI",
    netTotal: "3000",
    version: 1,
    lines: [
      { id: "l1", lineNo: 1, productId: PRODUCT, quantity: "3", unit: "EA", siteId: SITE, warehouseId: WAREHOUSE, unitPrice: "1000", discPct: "0", lineNet: "3000", requestedShipDate: "2027-03-04", lineStatus: "OPEN" },
    ],
    holds: [],
    createdAt: "2027-03-01T09:00:00Z",
    updatedAt: "2027-03-01T09:00:00Z",
    ...overrides,
  };
}

const creditHold = { id: HOLD_ID, holdType: "CREDIT", reasonCode: "CR-1", status: "ACTIVE", orderAmount: "3000", appliedBy: "rep", appliedAt: "2027-03-01T09:05:00Z" };

const heldOrder = order({
  version: 2,
  holds: [creditHold],
  creditCheck: {
    outcome: "FAIL",
    rules: [
      { id: "CR-4", result: "PASS", value: "0", threshold: "0" },
      { id: "CR-1", result: "FAIL", value: "11500", threshold: "11000" },
    ],
  },
});

const profile = {
  legalEntityId: LE,
  customerId: CUSTOMER,
  limit: "10000",
  currency: "USD",
  limitType: "BALANCE",
  tolerancePct: "5",
  toleranceAmount: "1000",
  oldestItemDaysMax: 30,
  maxDunningLevel: 3,
  reviewDate: "2027-12-31",
  mandatoryCheck: true,
  exposure: { openAr: "8500", unapplied: "0", deliveredNotInvoiced: "0", openOrders: "0", overdueAr: "0", oldestItemDays: 0, dunningLevel: 0 },
  version: 4,
  updatedAt: "2027-03-01T08:00:00Z",
};

describe("Sales orders list", () => {
  it("lists orders with status, holds and net, and passes the chosen filter", async () => {
    handler = () => ({ status: 200, body: { items: [summary] } });
    const { container } = renderAt("/sales/orders");
    const table = await findTable({ name: "Sales orders" });
    await waitFor(() => expect(within(table).getByText("SO-0000042")).toBeInTheDocument());
    expect(within(table).getByText("Credit")).toBeInTheDocument();
    expect(within(table).getByText("3000 USD")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "CONFIRMED" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("status")).toBe("CONFIRMED"));
  });

  it("shows a loading table while the request is pending", async () => {
    handler = () => new Promise<Reply>(() => undefined);
    renderAt("/sales/orders");
    expect(await screen.findByRole("table", { name: "Sales orders" })).toHaveAttribute("aria-busy", "true");
  });

  it("explains an empty list", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/sales/orders");
    expect(await screen.findByRole("heading", { name: "No sales orders yet" })).toBeInTheDocument();
  });

  it("shows the problem code and retries on error", async () => {
    handler = () => ({ status: 500, body: { status: 500, code: "COM-0500", detail: "store unavailable" } });
    renderAt("/sales/orders");
    expect(await screen.findByText(/COM-0500/)).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: "Sales orders" })).not.toBeInTheDocument();
    handler = () => ({ status: 200, body: { items: [summary] } });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("SO-0000042")).toBeInTheDocument();
  });

  it("loads the next page from the cursor", async () => {
    handler = (url) =>
      url.searchParams.get("cursor") === "c1"
        ? { status: 200, body: { items: [{ ...summary, id: "o2", orderNumber: "SO-0000041" }] } }
        : { status: 200, body: { items: [summary], nextCursor: "c1" } };
    renderAt("/sales/orders");
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await screen.findByText("SO-0000041")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });
});

describe("Sales order detail", () => {
  it("confirms at the current ETag and explains a credit hold with the failing rule", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 200, body: { ...heldOrder, confirmed: false }, etag: '"2"' } : { status: 200, body: order(), etag: '"1"' });
    const { container } = renderAt(`/sales/orders/${ORDER_ID}`);
    expect(await screen.findByRole("heading", { name: "Sales order SO-0000042" })).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Order lines" })).getByText(PRODUCT)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Confirm order" }));
    expect(await screen.findByText(/a credit hold was placed/)).toBeInTheDocument();
    const confirm = calls.find((c) => c.method === "POST");
    expect(confirm?.url.pathname).toBe(`/api/v1/sales/orders/${ORDER_ID}:confirm`);
    expect(confirm?.headers.get("If-Match")).toBe('"1"');
    const rules = screen.getByRole("table", { name: "Credit rules" });
    expect(within(rules).getByText("11500")).toBeInTheDocument();
    expect(within(rules).getByText("Fail")).toBeInTheDocument();
    // An active CREDIT hold blocks confirmation (BR-27.4).
    expect(screen.getByRole("button", { name: "Confirm order" })).toBeDisabled();
  });

  it("releases the CREDIT hold, which confirms the order", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 200, body: { ...order({ status: "CONFIRMED", version: 4, holds: [{ ...creditHold, status: "RELEASED", releasedBy: "credit-manager" }] }), confirmed: true }, etag: '"4"' }
        : { status: 200, body: heldOrder, etag: '"2"' };
    renderAt(`/sales/orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Release Credit hold CR-1" }));
    expect(await screen.findByText("Credit hold released and order SO-0000042 confirmed.")).toBeInTheDocument();
    const release = calls.find((c) => c.method === "POST");
    expect(release?.url.pathname).toBe(`/api/v1/sales/orders/${ORDER_ID}/holds/${HOLD_ID}:release`);
    expect(release?.headers.get("If-Match")).toBe('"2"');
    expect(screen.queryByRole("button", { name: "Confirm order" })).not.toBeInTheDocument();
  });

  it("offers a reload when someone else changed the order", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? { status: 412, body: { status: 412, code: "SLS-0006", detail: "the order changed; reload it" } } : { status: 200, body: order(), etag: '"1"' };
    renderAt(`/sales/orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Confirm order" }));
    expect(await screen.findByText("Someone else changed this order")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reload order" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "GET" && c.url.pathname === `/api/v1/sales/orders/${ORDER_ID}`)).toHaveLength(2));
  });

  it("shows the error when the order cannot be loaded", async () => {
    handler = () => ({ status: 404, body: { status: 404, code: "SLS-0100", detail: "order not found" } });
    renderAt(`/sales/orders/${ORDER_ID}`);
    expect(await screen.findByText("Could not load the order")).toBeInTheDocument();
    expect(screen.getByText(/SLS-0100/)).toBeInTheDocument();
  });
});

describe("New sales order", () => {
  it("validates before sending anything", async () => {
    handler = () => ({ status: 500, body: {} });
    renderAt("/sales/orders/new");
    fireEvent.click(screen.getByRole("button", { name: "Save draft order" }));
    expect(await screen.findByText("Enter the customer's UUID.")).toBeInTheDocument();
    expect(screen.getByText("Choose a product.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("picks the product through the shared lookup and takes its base unit", async () => {
    handler = () => ({ status: 500, body: {} });
    setLookupFetcher(async () => new Response(JSON.stringify({ items: [{ ID: PRODUCT, ProductNumber: "BOLT", Name: "Hex bolt", BaseUOM: "KG" }] }), { status: 200 }));
    renderAt("/sales/orders/new");
    expect(screen.getByLabelText("Unit")).toHaveValue("EA");
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: "bolt" } });
    fireEvent.click(await screen.findByText("Hex bolt"));
    expect(screen.getByLabelText("Product")).toHaveValue("Hex bolt (BOLT)");
    expect(screen.getByLabelText("Product")).toHaveAttribute("data-value", PRODUCT);
    expect(screen.getByLabelText("Unit")).toHaveValue("KG");
    fireEvent.change(screen.getByLabelText("Unit"), { target: { value: "BOX" } });
    expect(screen.getByLabelText("Unit")).toHaveValue("BOX");
  });

  it("explains tracking and the item group under the product, tied to the field", async () => {
    handler = () => ({ status: 500, body: {} });
    setLookupFetcher(async (url) =>
      new Response(
        JSON.stringify(
          url.includes("item-groups")
            ? { items: [{ ID: GROUP, Name: "Raw material" }] }
            : { items: [{ ID: PRODUCT, ProductNumber: "BOLT", Name: "Hex bolt", BaseUOM: "KG", ItemGroupID: GROUP, BatchTracked: true, CatchWeight: true }] },
        ),
        { status: 200 },
      ),
    );
    renderAt("/sales/orders/new");
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: "bolt" } });
    fireEvent.click(await screen.findByText("Hex bolt"));
    const hint = "Item group: Raw material. Batch tracked: the batch is recorded at receipt. Catch weight: the actual weight is recorded at receipt.";
    const note = await screen.findByText(hint);
    expect(screen.getByLabelText("Product").getAttribute("aria-describedby")).toContain(note.id);
  });

  it("keeps a unit the person typed when a product is picked", async () => {
    handler = () => ({ status: 500, body: {} });
    setLookupFetcher(async () => new Response(JSON.stringify({ items: [{ ID: PRODUCT, ProductNumber: "BOLT", Name: "Hex bolt", BaseUOM: "KG" }] }), { status: 200 }));
    renderAt("/sales/orders/new");
    fireEvent.change(screen.getByLabelText("Unit"), { target: { value: "BOX" } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: "bolt" } });
    fireEvent.click(await screen.findByText("Hex bolt"));
    expect(screen.getByLabelText("Unit")).toHaveValue("BOX");
  });

  it("marks the product invalid when none is chosen", async () => {
    handler = () => ({ status: 500, body: {} });
    renderAt("/sales/orders/new");
    fireEvent.click(screen.getByRole("button", { name: "Save draft order" }));
    await screen.findByText("Choose a product.");
    expect(screen.getByLabelText("Product")).toHaveAttribute("aria-invalid", "true");
  });

  it("asks for a product in plain words when none is chosen", async () => {
    handler = () => ({ status: 500, body: {} });
    renderAt("/sales/orders/new");
    fireEvent.click(screen.getByRole("button", { name: "Save draft order" }));
    expect(await screen.findByText("Choose a product.")).toBeInTheDocument();
  });

  it("creates the order with an Idempotency-Key and opens it", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 201, body: order(), etag: '"1"' } : { status: 200, body: order(), etag: '"1"' });
    const { container } = renderAt("/sales/orders/new");
    expect(pageViolations(container)).toEqual([]);
    const set = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
    set("Legal entity", LE);
    set("Customer", CUSTOMER);
    set("Product", PRODUCT);
    set("Quantity", "3");
    set("Unit price", "1000");
    set("Site", SITE);
    set("Warehouse", WAREHOUSE);
    fireEvent.click(screen.getByRole("button", { name: "Save draft order" }));
    expect(await screen.findByRole("heading", { name: "Sales order SO-0000042" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.headers.get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(post?.body).toMatchObject({ legalEntityId: LE, customerId: CUSTOMER, invoiceAccountId: CUSTOMER, lines: [{ productId: PRODUCT, quantity: "3", unitPrice: "1000" }] });
  });

  it("shows the service's field errors next to the fields", async () => {
    handler = () => ({ status: 400, body: { status: 400, code: "COM-0001", detail: "request failed validation", errors: { currency: "must be an ISO 4217 alpha-3 code" } } });
    renderAt("/sales/orders/new");
    const set = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
    set("Legal entity", LE);
    set("Customer", CUSTOMER);
    set("Product", PRODUCT);
    set("Unit price", "1");
    set("Site", SITE);
    set("Warehouse", WAREHOUSE);
    fireEvent.click(screen.getByRole("button", { name: "Save draft order" }));
    expect(await screen.findByText("must be an ISO 4217 alpha-3 code")).toBeInTheDocument();
    expect(screen.getByText("Could not save the order")).toBeInTheDocument();
  });
});

describe("Credit profiles", () => {
  it("lists profiles and opens one", async () => {
    handler = (url) => (url.pathname.endsWith("/credit-profiles") ? { status: 200, body: { items: [profile] } } : { status: 200, body: profile, etag: '"4"' });
    const { container } = renderAt("/sales/credit-profiles");
    const table = await findTable({ name: "Credit profiles" });
    await waitFor(() => expect(within(table).getByText("10000 USD")).toBeInTheDocument());
    expect(within(table).getByText("8500 USD")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: CUSTOMER }));
    expect(await screen.findByRole("heading", { name: "Credit profile" })).toBeInTheDocument();
  });

  it("lists the reviews due by a day and explains when none are", async () => {
    handler = (url) => ({ status: 200, body: { items: url.searchParams.get("reviewDueBy") === "2027-04-30" ? [profile] : [] } });
    renderAt("/sales/credit-profiles");
    await findTable({ name: "Credit profiles" });
    expect(calls[0].url.searchParams.has("reviewDueBy")).toBe(false);
    fireEvent.change(screen.getByLabelText(/Review due by/), { target: { value: "2027-04-30" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("reviewDueBy") === "2027-04-30")).toBe(true));
    const table = await findTable({ name: "Credit profiles" });
    expect(within(table).getByText("10000 USD")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Review due by/), { target: { value: "2027-01-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    expect(await screen.findByRole("heading", { name: "No credit reviews due" })).toBeInTheDocument();
  });

  it("explains an empty list and shows errors", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    const { unmount } = renderAt("/sales/credit-profiles");
    expect(await screen.findByRole("heading", { name: "No credit profiles yet" })).toBeInTheDocument();
    unmount();
    handler = () => ({ status: 403, body: { status: 403, code: "AUTH-0003", detail: "the legal entity is outside the caller's scope" } });
    renderAt("/sales/credit-profiles");
    expect(await screen.findByText(/AUTH-0003/)).toBeInTheDocument();
  });

  it("edits a profile at its ETag", async () => {
    handler = (_url, init) =>
      init.method === "PUT" ? { status: 200, body: { ...profile, limit: "12000", version: 5 }, etag: '"5"' } : { status: 200, body: profile, etag: '"4"' };
    renderAt(`/sales/credit-profiles/${LE}/${CUSTOMER}`);
    const limit = await screen.findByLabelText("Credit limit");
    expect(limit).toHaveValue("10000");
    expect(screen.getByLabelText("Customer")).toHaveAttribute("readonly");
    fireEvent.change(limit, { target: { value: "12000" } });
    fireEvent.click(screen.getByRole("button", { name: "Save credit profile" }));
    expect(await screen.findByText("Credit profile saved.")).toBeInTheDocument();
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.url.pathname).toBe(`/api/v1/sales/credit-profiles/${CUSTOMER}`);
    expect(put?.url.searchParams.get("legalEntityId")).toBe(LE);
    expect(put?.headers.get("If-Match")).toBe('"4"');
    expect(put?.body).toMatchObject({ limit: "12000", mandatoryCheck: true, exposure: { openAr: "8500" } });
  });

  it("shows open orders computed by the service read-only and names those it cannot count", async () => {
    handler = () => ({
      status: 200,
      body: { ...profile, limitType: "BALANCE_ALL", exposure: { ...profile.exposure, openOrders: "6000", openOrdersOtherCurrencies: [{ currency: "EUR", amount: "4000" }] } },
      etag: '"4"',
    });
    renderAt(`/sales/credit-profiles/${LE}/${CUSTOMER}`);
    const open = await screen.findByLabelText("Confirmed open orders");
    expect(open).toHaveValue("6000");
    expect(open).toHaveAttribute("readonly");
    expect(screen.getByText("Open orders not counted")).toBeInTheDocument();
    expect(screen.getByText(/4000 EUR/)).toBeInTheDocument();
  });

  it("shows delivered, not invoiced goods computed by the service read-only and names those it cannot count", async () => {
    handler = () => ({
      status: 200,
      body: { ...profile, limitType: "BALANCE_PACKING_SLIP", exposure: { ...profile.exposure, deliveredNotInvoiced: "700", deliveredNotInvoicedOtherCurrencies: [{ currency: "EUR", amount: "250" }] } },
      etag: '"4"',
    });
    renderAt(`/sales/credit-profiles/${LE}/${CUSTOMER}`);
    const field = await screen.findByLabelText("Delivered, not invoiced");
    expect(field).toHaveValue("700");
    expect(field).toHaveAttribute("readonly");
    expect(screen.getByText("Delivered goods not counted")).toBeInTheDocument();
    expect(screen.getByText(/250 EUR/)).toBeInTheDocument();
  });

  it("creates a profile with If-Match 0", async () => {
    handler = (_url, init) => (init.method === "PUT" ? { status: 200, body: { ...profile, version: 1 }, etag: '"1"' } : { status: 200, body: { ...profile, version: 1 }, etag: '"1"' });
    renderAt("/sales/credit-profiles/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: CUSTOMER } });
    fireEvent.change(screen.getByLabelText("Credit limit"), { target: { value: "10000" } });
    fireEvent.click(screen.getByRole("button", { name: "Create credit profile" }));
    expect(await screen.findByRole("button", { name: "Save credit profile" })).toBeInTheDocument();
    expect(calls.find((c) => c.method === "PUT")?.headers.get("If-Match")).toBe('"0"');
  });
});

describe("ATP check", () => {
  it("posts the buckets and shows the promise and cumulative ATP (BR-27.1)", async () => {
    handler = () => ({
      status: 200,
      body: {
        promise: { promisedDate: "2027-03-04", complete: true, backorder: "0", source: "ATP", schedule: [{ date: "2027-03-04", quantity: "45", period: 1 }] },
        periods: [
          { date: "2027-03-01", supply: "100", demand: "60" },
          { date: "2027-03-04", supply: "50", demand: "40" },
        ],
        cumulative: ["40", "50"],
      },
    });
    const { container } = renderAt("/sales/atp");
    fireEvent.change(screen.getByLabelText("Requested date"), { target: { value: "2027-03-01" } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "45" } });
    const bucket1 = screen.getByRole("group", { name: "Bucket 1" });
    fireEvent.change(within(bucket1).getByLabelText("Date"), { target: { value: "2027-03-01" } });
    fireEvent.change(within(bucket1).getByLabelText("Supply"), { target: { value: "100" } });
    fireEvent.change(within(bucket1).getByLabelText("Demand"), { target: { value: "60" } });
    fireEvent.click(screen.getByRole("button", { name: "Add bucket" }));
    const bucket2 = screen.getByRole("group", { name: "Bucket 2" });
    fireEvent.change(within(bucket2).getByLabelText("Date"), { target: { value: "2027-03-04" } });
    fireEvent.change(within(bucket2).getByLabelText("Supply"), { target: { value: "50" } });
    fireEvent.change(within(bucket2).getByLabelText("Demand"), { target: { value: "40" } });
    fireEvent.click(screen.getByRole("button", { name: "Check ATP" }));
    expect(await screen.findByText("45 can ship on 2027-03-04.")).toBeInTheDocument();
    const cumulative = within(screen.getByRole("table", { name: "Cumulative ATP" })).getAllByRole("row").map((r) => r.lastElementChild?.textContent);
    expect(cumulative).toEqual(["Cumulative ATP", "40", "50"]);
    expect(calls[0].body).toMatchObject({ requested: "2027-03-01", quantity: "45", completeOnly: true, periods: [{ supply: "100" }, { date: "2027-03-04" }] });
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the service problem when the check fails", async () => {
    handler = () => ({ status: 400, body: { status: 400, code: "COM-0001", detail: "request failed validation", errors: { "periods[0].supply": "must be a non-negative decimal" } } });
    renderAt("/sales/atp");
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Check ATP" }));
    expect(await screen.findByText("must be a non-negative decimal")).toBeInTheDocument();
    expect(screen.getByText("Could not check ATP")).toBeInTheDocument();
  });
});

describe("form validation", () => {
  it("validateOrder invoices the customer by default and rejects bad lines", () => {
    const base = { legalEntityId: LE, customerId: CUSTOMER, invoiceAccountId: "", orderType: "ORDER" as const, currency: "USD", requestedShipDate: "2027-03-04", externalRef: "", listingOverrideReason: "" };
    const ok = validateOrder({ ...base, lines: [{ ...emptyLine("2027-03-04"), productId: PRODUCT, siteId: SITE, warehouseId: WAREHOUSE, unitPrice: "10.5" }] });
    expect(ok.input?.invoiceAccountId).toBe(CUSTOMER);
    expect(ok.input?.lines[0].unitPrice).toBe("10.5");
    expect(ok.input?.orderType).toBe("ORDER");
    const byDefault = validateOrder({ ...base, orderType: "", lines: [{ ...emptyLine("2027-03-04"), productId: PRODUCT, siteId: SITE, warehouseId: WAREHOUSE, unitPrice: "10.5" }] });
    expect(byDefault.input && "orderType" in byDefault.input).toBe(false);
    const bad = validateOrder({ ...base, lines: [{ ...emptyLine("2027-03-04"), quantity: "0", discPct: "101" }] });
    expect(bad.input).toBeUndefined();
    expect(bad.errors["lines[0].quantity"]).toBeDefined();
    expect(bad.errors["lines[0].discPct"]).toBeDefined();
    expect(validateOrder({ ...base, lines: [] }).errors.lines).toBe("Add at least one line.");
  });

  it("validateAtp requires increasing bucket dates", () => {
    const res = validateAtp({
      requested: "2027-03-01",
      quantity: "1",
      completeOnly: true,
      periods: [
        { date: "2027-03-04", supply: "1", demand: "0" },
        { date: "2027-03-04", supply: "1", demand: "0" },
      ],
    });
    expect(res.errors["periods[1].date"]).toBe("Use a date later than the previous bucket.");
  });

  it("validateProfile keeps decimals as strings and checks the dunning level", () => {
    const ok = validateProfile({ ...blankProfile, legalEntityId: LE, customerId: CUSTOMER, limit: "10000.50" });
    expect(ok.input).toMatchObject({ limit: "10000.50", tolerancePct: "5", maxDunningLevel: 3, exposure: { openAr: "0", dunningLevel: 0 } });
    expect(validateProfile({ ...blankProfile, legalEntityId: LE, customerId: CUSTOMER, limit: "1", maxDunningLevel: "10" }).errors.maxDunningLevel).toBeDefined();
  });

  it("promiseSummary covers split and backorder outcomes", () => {
    const base = { periods: [], cumulative: [] };
    expect(
      promiseSummary({ ...base, promise: { complete: false, backorder: "10", source: "ATP", schedule: [] } }, "10"),
    ).toBe("None of 10 can be promised; 10 goes on backorder.");
    expect(
      promiseSummary(
        { ...base, promise: { promisedDate: "2027-03-04", complete: true, backorder: "0", source: "ATP", schedule: [{ date: "2027-03-01", quantity: "40", period: 0 }, { date: "2027-03-04", quantity: "5", period: 1 }] } },
        "45",
      ),
    ).toBe("45 can ship in 2 deliveries, the last on 2027-03-04.");
  });
});
