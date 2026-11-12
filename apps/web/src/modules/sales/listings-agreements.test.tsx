import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { validateRelease } from "./AgreementDetailPage";
import { validateAgreement } from "./AgreementsPage";
import sales from "./module";
import { validateListing } from "./ProductListingsPage";
import { nameLookups } from "./testLookup";

const LE = "11111111-0000-4000-8000-000000000001";
const CUSTOMER = "22222222-0000-4000-8000-000000000002";
const PRODUCT = "33333333-0000-4000-8000-000000000003";
const SITE = "44444444-0000-4000-8000-000000000004";
const WAREHOUSE = "55555555-0000-4000-8000-000000000005";
const ORDER_ID = "66666666-0000-4000-8000-000000000006";
const LISTING_ID = "77777777-0000-4000-8000-000000000007";
const AGREEMENT_ID = "88888888-0000-4000-8000-000000000008";

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
      // The price prefill reads the customer group and asks pricing for the price; these tests are about the order calls only.
      if (url.pathname === "/api/v1/pricing/resolve" || url.pathname.startsWith("/api/v1/party/customers/")) return new Response("{}", { status: 404, headers: { "Content-Type": "application/json" } });
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

const listing = {
  id: LISTING_ID,
  legalEntityId: LE,
  scopeType: "CUSTOMER",
  scopeKey: CUSTOMER,
  productId: PRODUCT,
  listingType: "DENY",
  createdBy: "listing-admin",
  createdAt: "2027-03-01T09:00:00Z",
};

const agreement = {
  id: AGREEMENT_ID,
  legalEntityId: LE,
  agreementNumber: "SA-0000007",
  customerId: CUSTOMER,
  invoiceAccountId: CUSTOMER,
  productId: PRODUCT,
  unit: "EA",
  unitPrice: "12.5",
  currency: "USD",
  commitmentQty: "1000",
  releasedQty: "600",
  remainingQty: "400",
  status: "ACTIVE",
  version: 2,
  createdBy: "key-account",
  createdAt: "2027-03-01T09:00:00Z",
  updatedAt: "2027-03-02T09:00:00Z",
  releases: [{ id: "r1", quantity: "600", salesOrderId: ORDER_ID, orderNumber: "SO-0000041", actor: "key-account", releasedAt: "2027-03-02T09:00:00Z" }],
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
    listingOverrides: [],
    createdAt: "2027-03-01T09:00:00Z",
    updatedAt: "2027-03-01T09:00:00Z",
    ...overrides,
  };
}

function fill(label: string | RegExp, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("Product listings", () => {
  it("lists entries and passes the filter to the inquiry", async () => {
    handler = () => ({ status: 200, body: { items: [listing] } });
    nameLookups({ [PRODUCT]: "Hex bolt" });
    const { container } = renderAt("/sales/product-listings");
    const table = await findTable({ name: "Product listings" });
    await waitFor(() => expect(within(table).getByText("Hex bolt")).toBeInTheDocument());
    expect(within(table).getByText("Deny")).toBeInTheDocument();
    expect(within(table).getByText("Always")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fill("Product", PRODUCT);
    fill(/Customer or group/, "EU-RETAIL");
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("productId")).toBe(PRODUCT));
    expect(calls.at(-1)?.url.searchParams.get("scopeKey")).toBe("EU-RETAIL");
  });

  it("refuses a malformed product filter without calling the service", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/sales/product-listings");
    await screen.findByRole("heading", { name: "No listing entries" });
    const before = calls.length;
    fill("Product", "FG-120");
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    expect(screen.getByText("Enter a product UUID.")).toBeInTheDocument();
    expect(calls.length).toBe(before);
  });

  it("shows loading, then the problem code with a retry", async () => {
    let fail = true;
    handler = () => (fail ? { status: 503, body: { code: "COM-0503", detail: "store unavailable" } } : { status: 200, body: { items: [listing] } });
    nameLookups({ [PRODUCT]: "Hex bolt" });
    renderAt("/sales/product-listings");
    expect(await screen.findByText(/COM-0503/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Hex bolt")).toBeInTheDocument();
  });

  it("asks before removing an entry, then deletes it and reloads", async () => {
    handler = (_url, init) => (init.method === "DELETE" ? { status: 204, body: null } : { status: 200, body: { items: [listing] } });
    renderAt("/sales/product-listings");
    fireEvent.click(await screen.findByRole("button", { name: `Remove the entry for ${PRODUCT}` }));
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: `Confirm removing the entry for ${PRODUCT}` }));
    expect(await screen.findByText(/entry for 33333333 was removed/)).toBeInTheDocument();
    const del = calls.find((c) => c.method === "DELETE");
    expect(del?.url.pathname).toBe(`/api/v1/sales/product-listings/${LISTING_ID}`);
    await waitFor(() => expect(calls.filter((c) => c.method === "GET").length).toBe(2));
  });

  it("the new entry form validates, posts the entry and returns to the list", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 201, body: listing } : { status: 200, body: { items: [listing] } });
    const { container } = renderAt("/sales/product-listings/new");
    fireEvent.click(await screen.findByRole("button", { name: "Save entry" }));
    expect(screen.getByText("Enter the legal entity's UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);

    fill("Legal entity", LE);
    fill("Customer", CUSTOMER);
    fill("Product", PRODUCT);
    fill(/Valid from/, "2027-04-01");
    fireEvent.click(screen.getByRole("button", { name: "Save entry" }));
    expect(await screen.findByRole("heading", { name: "Product listings" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe("/api/v1/sales/product-listings");
    expect(post?.body).toEqual({ legalEntityId: LE, scopeType: "CUSTOMER", scopeKey: CUSTOMER, productId: PRODUCT, listingType: "DENY", validFrom: "2027-04-01" });
  });

  it("validateListing mirrors the service rules", () => {
    const base = { legalEntityId: LE, scopeType: "GROUP" as const, scopeKey: "EU-RETAIL", productId: PRODUCT, listingType: "ALLOW" as const, validFrom: "", validTo: "" };
    expect(validateListing(base).input).toEqual({ legalEntityId: LE, scopeType: "GROUP", scopeKey: "EU-RETAIL", productId: PRODUCT, listingType: "ALLOW" });
    expect(validateListing({ ...base, scopeType: "CUSTOMER" }).errors.scopeKey).toBe("Enter the customer's UUID.");
    expect(validateListing({ ...base, scopeKey: "x".repeat(81) }).errors.scopeKey).toMatch(/80 characters/);
    expect(validateListing({ ...base, validFrom: "2027-06-01", validTo: "2027-05-31" }).errors.validTo).toBe("The end must not be before the start.");
  });
});

describe("Sales agreements", () => {
  it("lists agreements with remaining quantity and filters by status", async () => {
    handler = () => ({ status: 200, body: { items: [agreement] } });
    const { container } = renderAt("/sales/agreements");
    const table = await findTable({ name: "Sales agreements" });
    await waitFor(() => expect(within(table).getByText("SA-0000007")).toBeInTheDocument());
    expect(within(table).getByText("400 EA")).toBeInTheDocument();
    expect(within(table).getByText("12.5 USD / EA")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "EXHAUSTED" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("status")).toBe("EXHAUSTED"));
  });

  it("shows a loading table, then explains an empty list", async () => {
    let resolve: (r: Reply) => void = () => undefined;
    handler = () => new Promise<Reply>((r) => (resolve = r));
    renderAt("/sales/agreements");
    expect(await screen.findByRole("table", { name: "Sales agreements" })).toHaveAttribute("aria-busy", "true");
    resolve({ status: 200, body: { items: [] } });
    expect(await screen.findByRole("heading", { name: "No sales agreements yet" })).toBeInTheDocument();
  });

  it("says when the caller may not view agreements", async () => {
    handler = () => ({ status: 403, body: { code: "AUTH-0003", detail: "the legal entity is outside the caller's scope" } });
    renderAt("/sales/agreements");
    expect(await screen.findByText("You can't view sales agreements")).toBeInTheDocument();
  });

  it("the new agreement form validates, posts with an Idempotency-Key and opens the agreement", async () => {
    handler = (url, init) =>
      init.method === "POST" ? { status: 201, body: agreement, etag: '"1"' } : url.pathname.endsWith(AGREEMENT_ID) ? { status: 200, body: agreement, etag: '"2"' } : { status: 404, body: {} };
    const { container } = renderAt("/sales/agreements/new");
    fireEvent.click(await screen.findByRole("button", { name: "Save agreement" }));
    expect(screen.getByText("Enter the customer's UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);

    fill("Legal entity", LE);
    fill("Customer", CUSTOMER);
    fill("Product", PRODUCT);
    fill("Committed quantity", "1000");
    fill("Unit price", "12.50");
    fireEvent.click(screen.getByRole("button", { name: "Save agreement" }));
    expect(await screen.findByRole("heading", { name: "Sales agreement SA-0000007" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe("/api/v1/sales/agreements");
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post?.body).toEqual({ legalEntityId: LE, customerId: CUSTOMER, productId: PRODUCT, unit: "EA", unitPrice: "12.50", currency: "USD", commitmentQty: "1000" });
  });

  it("the agreement page releases an order and links to it", async () => {
    const created = order({ id: "99999999-0000-4000-8000-000000000009", orderNumber: "SO-0000043", status: "DRAFT", channel: "AGREEMENT" });
    handler = (_url, init) =>
      init.method === "POST" ? { status: 201, body: { agreement: { ...agreement, releasedQty: "640", remainingQty: "360" }, order: created } } : { status: 200, body: agreement, etag: '"2"' };
    const { container } = renderAt(`/sales/agreements/${AGREEMENT_ID}`);
    expect(await screen.findByRole("heading", { name: "Sales agreement SA-0000007" })).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Releases" })).getByRole("link", { name: "SO-0000041" })).toHaveAttribute("href", `/sales/orders/${ORDER_ID}`);
    expect(pageViolations(container)).toEqual([]);

    fill("Quantity", "40");
    fill("Site", SITE);
    fill("Warehouse", WAREHOUSE);
    fill("Requested ship date", "2027-04-15");
    fireEvent.click(screen.getByRole("button", { name: "Release order" }));
    expect(await screen.findByRole("link", { name: "SO-0000043" })).toHaveAttribute("href", `/sales/orders/${created.id}`);
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/agreements/${AGREEMENT_ID}:release-order`);
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post?.body).toEqual({ quantity: "40", siteId: SITE, warehouseId: WAREHOUSE, requestedShipDate: "2027-04-15" });
    await waitFor(() => expect(calls.filter((c) => c.method === "GET" && !c.url.pathname.endsWith("/versions")).length).toBe(2));
  });

  it("explains AGREEMENT_EXHAUSTED and hides the form once the commitment is used up", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 422, body: { code: "SLS-0008", detail: "AGREEMENT_EXHAUSTED: agreement SA-0000007 has 400 EA remaining; the release asks for 401" } }
        : { status: 200, body: agreement, etag: '"2"' };
    renderAt(`/sales/agreements/${AGREEMENT_ID}`);
    await screen.findByRole("heading", { name: "Sales agreement SA-0000007" });
    fill("Quantity", "401");
    fill("Site", SITE);
    fill("Warehouse", WAREHOUSE);
    fireEvent.click(screen.getByRole("button", { name: "Release order" }));
    expect(await screen.findByText("Not enough commitment left")).toBeInTheDocument();
    expect(screen.getByText(/SLS-0008/)).toBeInTheDocument();
  });

  it("an exhausted agreement offers no release", async () => {
    handler = () => ({ status: 200, body: { ...agreement, status: "EXHAUSTED", releasedQty: "1000", remainingQty: "0" }, etag: '"3"' });
    renderAt(`/sales/agreements/${AGREEMENT_ID}`);
    expect(await screen.findByRole("heading", { name: "Commitment used up" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Release order" })).not.toBeInTheDocument();
  });

  it("shows the problem when the agreement cannot be loaded", async () => {
    handler = () => ({ status: 404, body: { code: "SLS-0103", detail: "sales agreement not found" } });
    renderAt(`/sales/agreements/${AGREEMENT_ID}`);
    expect(await screen.findByText(/SLS-0103/)).toBeInTheDocument();
  });

  it("validateAgreement and validateRelease mirror the service rules", () => {
    const base = { legalEntityId: LE, customerId: CUSTOMER, invoiceAccountId: "", productId: PRODUCT, unit: "EA", unitPrice: "1", currency: "USD", commitmentQty: "10", validFrom: "", validTo: "", cumTolerance: "", overdeliveryPct: "" };
    expect(validateAgreement(base).input).toBeDefined();
    expect(validateAgreement({ ...base, commitmentQty: "0" }).errors.commitmentQty).toMatch(/above 0/);
    expect(validateAgreement({ ...base, unitPrice: "1.00001" }).errors.unitPrice).toMatch(/4 decimals/);
    expect(validateAgreement({ ...base, invoiceAccountId: "INV" }).errors.invoiceAccountId).toBeDefined();
    expect(validateAgreement({ ...base, overdeliveryPct: "5.5" }).input?.overdeliveryPct).toBe("5.5");
    expect(validateAgreement({ ...base, overdeliveryPct: "100" }).input).toBeDefined();
    expect(validateAgreement({ ...base, overdeliveryPct: "101" }).errors.overdeliveryPct).toMatch(/0 to 100/);
    expect(validateAgreement({ ...base, overdeliveryPct: "1.005" }).errors.overdeliveryPct).toBeDefined();
    const release = { quantity: "5", siteId: SITE, warehouseId: WAREHOUSE, requestedShipDate: "2027-04-15" };
    expect(validateRelease(release).input).toEqual(release);
    expect(validateRelease({ ...release, quantity: "0.0" }).errors.quantity).toBeDefined();
    expect(validateRelease({ ...release, siteId: "S1" }).errors.siteId).toBe("Enter the site's UUID.");
  });
});

describe("Order holds and listing overrides", () => {
  it("applies a fraud hold at the current ETag and shows logged listing overrides", async () => {
    const overrides = [{ lineNo: 1, productId: PRODUCT, decision: "CUSTOMER_DENY", reason: "replacement for recalled batch", actor: "sales-manager" }];
    const held = order({
      version: 2,
      listingOverrides: overrides,
      holds: [{ id: "h1", holdType: "FRAUD", reasonCode: "CARD-MISMATCH", status: "ACTIVE", orderAmount: "3000", appliedBy: "u1", appliedAt: "2027-03-01T10:00:00Z" }],
    });
    handler = (url, init) =>
      init.method === "POST" ? { status: 201, body: held, etag: '"2"' } : url.pathname.endsWith("/versions") ? { status: 200, body: { items: [] } } : { status: 200, body: order({ listingOverrides: overrides }), etag: '"1"' };
    const { container } = renderAt(`/sales/orders/${ORDER_ID}`);
    const table = await findTable({ name: "Listing overrides" });
    expect(within(table).getByText("replacement for recalled batch")).toBeInTheDocument();
    expect(within(table).getByText("Customer deny")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Apply hold" }));
    expect(screen.getByText("Enter a reason code of up to 80 characters.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);

    fireEvent.change(screen.getByLabelText(/Hold type/), { target: { value: "FRAUD" } });
    fill("Reason code", "CARD-MISMATCH");
    fireEvent.click(screen.getByRole("button", { name: "Apply hold" }));
    expect(await screen.findByText("Fraud hold applied.")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/orders/${ORDER_ID}/holds`);
    expect(post?.headers.get("If-Match")).toBe('"1"');
    expect(post?.body).toEqual({ holdType: "FRAUD", reasonCode: "CARD-MISMATCH" });
    expect(screen.getByRole("button", { name: "Confirm order" })).toBeDisabled();
  });

  it("offers no hold form on a cancelled order", async () => {
    handler = (url) => (url.pathname.endsWith("/versions") ? { status: 200, body: { items: [] } } : { status: 200, body: order({ status: "CANCELLED" }), etag: '"5"' });
    renderAt(`/sales/orders/${ORDER_ID}`);
    await screen.findByRole("heading", { name: "Sales order SO-0000042" });
    expect(screen.queryByRole("button", { name: "Apply hold" })).not.toBeInTheDocument();
  });

  it("new order sends the override reason, never a customer group (party owns it), and names a listing denial", async () => {
    handler = () => ({ status: 422, body: { code: "SLS-0011", detail: "PRODUCT_NOT_LISTED: the product is not listed for this customer on line 1 (CUSTOMER_DENY)" } });
    renderAt("/sales/orders/new");
    fill("Legal entity", LE);
    fill("Customer", CUSTOMER);
    expect(screen.queryByLabelText(/Customer group/)).not.toBeInTheDocument();
    fill(/Listing override reason/, "replacement for recalled batch");
    fill("Product", PRODUCT);
    fill("Unit price", "10");
    fill("Site", SITE);
    fill("Warehouse", WAREHOUSE);
    fireEvent.click(screen.getByRole("button", { name: "Save draft order" }));
    expect(await screen.findByText("A product is not listed for this customer")).toBeInTheDocument();
    expect(calls[0].body).toMatchObject({ listingOverrideReason: "replacement for recalled batch" });
    expect(calls[0].body).not.toHaveProperty("customerGroup");
  });
});
