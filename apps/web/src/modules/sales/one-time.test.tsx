import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { validateOneTime, emptyOneTime } from "./OneTimeFields";
import { DUTY_PARAMETER_MAINTAIN, validateParameters } from "./ParametersPage";
import sales from "./module";

const LE = "11111111-0000-4000-8000-000000000001";
const CUSTOMER = "22222222-0000-4000-8000-000000000002";
const PRODUCT = "33333333-0000-4000-8000-000000000003";
const SITE = "44444444-0000-4000-8000-000000000004";
const WAREHOUSE = "55555555-0000-4000-8000-000000000005";
const ORDER_ID = "66666666-0000-4000-8000-000000000006";

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
      // Picking a product asks pricing for the customer's price; these tests are about the order calls only.
      if (url.pathname === "/api/v1/pricing/resolve") return new Response("{}", { status: 404, headers: { "Content-Type": "application/json" } });
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

function renderAt(path: string, duties: string[] = ["sales.order.read", DUTY_PARAMETER_MAINTAIN]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {sales.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
          <Route path="*" element={<p>Elsewhere</p>} />
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const walkIn = { name: "Walk-in Buyer GmbH", line1: "Hauptstr. 1", city: "Berlin", postalCode: "10115", country: "DE", taxId: "DE123456789" };

function order(overrides: Record<string, unknown> = {}) {
  return {
    id: ORDER_ID,
    legalEntityId: LE,
    orderNumber: "SO-0000077",
    customerId: CUSTOMER,
    invoiceAccountId: CUSTOMER,
    orderType: "ORDER",
    status: "DRAFT",
    currency: "USD",
    requestedShipDate: "2027-03-04",
    channel: "UI",
    netTotal: "3000",
    version: 1,
    lines: [],
    holds: [],
    createdAt: "2027-03-01T09:00:00Z",
    updatedAt: "2027-03-01T09:00:00Z",
    ...overrides,
  };
}

const set = (label: string | RegExp, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

function fillOrderBasics() {
  set("Legal entity", LE);
  set("Customer", CUSTOMER);
  set("Product", PRODUCT);
  set("Quantity", "3");
  set("Unit price", "1000");
  set("Site", SITE);
  set("Warehouse", WAREHOUSE);
}

describe("validateOneTime", () => {
  it("requires the name and address and trims the optional parts away", () => {
    const errors: Record<string, string> = {};
    expect(validateOneTime(emptyOneTime, errors)).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual(["oneTime.city", "oneTime.country", "oneTime.line1", "oneTime.name", "oneTime.postalCode"]);
    const ok = validateOneTime({ ...emptyOneTime, ...walkIn, line2: " ", region: "", taxId: " DE1 " }, {});
    expect(ok).toEqual({ name: "Walk-in Buyer GmbH", line1: "Hauptstr. 1", city: "Berlin", postalCode: "10115", country: "DE", taxId: "DE1" });
  });
  it("refuses a country that is not two capital letters and over-long values", () => {
    const errors: Record<string, string> = {};
    validateOneTime({ ...emptyOneTime, ...walkIn, country: "Germany", name: "x".repeat(141) }, errors);
    expect(Object.keys(errors).sort()).toEqual(["oneTime.country", "oneTime.name"]);
  });
});

describe("One-time customer order", () => {
  it("sends the buyer with the order and shows it on the order page", async () => {
    handler = (_url, init) => ({ status: init.method === "POST" ? 201 : 200, body: order({ oneTime: walkIn }), etag: '"1"' });
    const { container } = renderAt("/sales/orders/new");
    expect(screen.queryByText("Buyer name")).not.toBeInTheDocument();
    fireEvent.click(screen.getByLabelText(/One-time customer/));
    expect(screen.getByText(/The customer above is/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fillOrderBasics();
    fireEvent.click(screen.getByRole("button", { name: "Save draft order" }));
    expect(await screen.findByText("Enter the buyer's name.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    set("Buyer name", walkIn.name);
    set("Address line 1", walkIn.line1);
    set("City", walkIn.city);
    set("Postal code", walkIn.postalCode);
    set("Country", "de");
    set("Tax registration number", walkIn.taxId);
    fireEvent.click(screen.getByRole("button", { name: "Save draft order" }));
    expect(await screen.findByRole("heading", { name: "Sales order SO-0000077" })).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({ oneTime: walkIn });
    expect(await screen.findByRole("heading", { name: "One-time customer" })).toBeInTheDocument();
    expect(screen.getByText("Walk-in Buyer GmbH")).toBeInTheDocument();
    expect(screen.getByText("Hauptstr. 1, 10115 Berlin, DE")).toBeInTheDocument();
    expect(screen.getByText("DE123456789")).toBeInTheDocument();
  });

  it("leaves oneTime out of an ordinary order", async () => {
    handler = () => ({ status: 201, body: order(), etag: '"1"' });
    renderAt("/sales/orders/new");
    fillOrderBasics();
    fireEvent.click(screen.getByRole("button", { name: "Save draft order" }));
    await screen.findByRole("heading", { name: "Sales order SO-0000077" });
    expect(calls.find((c) => c.method === "POST")?.body).not.toHaveProperty("oneTime");
    expect(screen.queryByRole("heading", { name: "One-time customer" })).not.toBeInTheDocument();
  });

  it("shows the limit refusal SLS-0016", async () => {
    handler = () => ({ status: 422, body: { status: 422, code: "SLS-0016", detail: "One-time order exceeds limit 5000.00 USD; create a regular customer" } });
    renderAt("/sales/orders/new");
    fireEvent.click(screen.getByLabelText(/One-time customer/));
    fillOrderBasics();
    set("Buyer name", walkIn.name);
    set("Address line 1", walkIn.line1);
    set("City", walkIn.city);
    set("Postal code", walkIn.postalCode);
    set("Country", "DE");
    fireEvent.click(screen.getByRole("button", { name: "Save draft order" }));
    expect(await screen.findByText("The order is over the one-time order limit")).toBeInTheDocument();
    expect(screen.getByText(/One-time order exceeds limit 5000.00 USD/)).toBeInTheDocument();
  });
});

describe("One-time order limit parameter", () => {
  const ok = { quotationExpiryDays: "30", quotationProbabilityDefault: "50", creditReleaseValidityDays: "7", creditHoldSlaBusinessDays: "2", defaultOrderType: "ORDER" as const, creditCheckPoints: ["confirm" as const], intakeDedupWindowDays: "30" };
  it("accepts both or neither and keeps the amount a string", () => {
    expect(validateParameters({ ...ok, oneTimeOrderLimit: "2500.50", oneTimeOrderLimitCurrency: "GBP" }).input).toMatchObject({ oneTimeOrderLimit: "2500.50", oneTimeOrderLimitCurrency: "GBP" });
    expect(validateParameters({ ...ok, oneTimeOrderLimit: "", oneTimeOrderLimitCurrency: "" }).input).not.toHaveProperty("oneTimeOrderLimit");
    expect(validateParameters({ ...ok, oneTimeOrderLimit: "100" }).errors.oneTimeOrderLimit).toMatch(/together/);
    expect(validateParameters({ ...ok, oneTimeOrderLimit: "1.234", oneTimeOrderLimitCurrency: "usd" }).errors).toEqual({
      oneTimeOrderLimit: "Enter an amount of 0 or more with at most 2 decimal places.",
      oneTimeOrderLimitCurrency: "Enter a three-letter ISO currency code, such as USD.",
    });
  });

  it("loads the saved limit and saves a new one", async () => {
    const saved = { legalEntityId: LE, quotationExpiryDays: 30, quotationProbabilityDefault: 50, creditReleaseValidityDays: 7, creditHoldSlaBusinessDays: 2, defaultOrderType: "ORDER", oneTimeOrderLimit: "1000", oneTimeOrderLimitCurrency: "USD", version: 2 };
    handler = (_u, init) => ({ status: 200, body: init.method === "PUT" ? { ...saved, version: 3 } : saved, etag: init.method === "PUT" ? '"3"' : '"2"' });
    renderAt("/sales/parameters");
    set("Legal entity", LE);
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    await screen.findByRole("form", { name: "Sales parameters" });
    expect(screen.getByLabelText(/^One-time order limit/)).toHaveValue("1000");
    expect(screen.getByLabelText(/One-time limit currency/)).toHaveValue("USD");
    set(/^One-time order limit/, "2500");
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("Sales parameters saved.")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "PUT")?.body).toMatchObject({ oneTimeOrderLimit: "2500", oneTimeOrderLimitCurrency: "USD" });
  });
});
