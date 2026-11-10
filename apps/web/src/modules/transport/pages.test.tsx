import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { CarrierInvoice, FreightBill, FreightBillDetail, Quote, RateCard } from "./api/client";
import transport from "./module";

const CARRIER = "0195f0a0-0000-7000-8000-0000000000c1";
const CARRIER_B = "0195f0a0-0000-7000-8000-0000000000c2";
const SERVICE = "0195f0a0-0000-7000-8000-0000000000e1";
const LOAD = "0195f0a0-0000-7000-8000-0000000000a1";
const BILL = "0195f0a0-0000-7000-8000-0000000000b1";
const INVOICE = "0195f0a0-0000-7000-8000-0000000000d1";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function Where() {
  const location = useLocation();
  return <p data-testid="location">{location.pathname + location.search}</p>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {transport.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

const card: RateCard = {
  id: "0195f0a0-0000-7000-8000-0000000000f1",
  carrierId: CARRIER,
  serviceId: SERVICE,
  basis: "WEIGHT_BREAK",
  currency: "USD",
  validFrom: "2026-01-01",
  validTo: null,
  minimumCharge: "15",
  dimDivisor: 5000,
  fuelPct: "18.5",
  rows: [{ origin: "CHI", destination: "DAL", breakFrom: "0", breakTo: null, rate: "0.5", unit: "KG" }],
  accessorials: [],
};

const bill = (extra: Partial<FreightBill> = {}): FreightBill => ({
  id: BILL,
  loadId: LOAD,
  carrierId: CARRIER,
  currency: "USD",
  expectedLines: [{ chargeCode: "BASE", amount: "412.5" }],
  expectedTotal: "412.5",
  matchStatus: "UNMATCHED",
  version: 1,
  createdAt: "2026-10-26T08:00:00Z",
  ...extra,
});

const invoice = (extra: Partial<CarrierInvoice> = {}): CarrierInvoice => ({
  id: INVOICE,
  carrierId: CARRIER,
  invoiceNo: "INV-1",
  invoiceDate: "2026-10-27",
  source: "EDI210",
  freightBillId: BILL,
  currency: "USD",
  lines: [{ chargeCode: "BASE", amount: "418" }],
  total: "418",
  matchStatus: "VARIANCE",
  varianceTotal: "5.5",
  createdAt: "2026-10-27T09:00:00Z",
  ...extra,
});

describe("Transportation home", () => {
  it("links every area of the slice", () => {
    const { container } = renderAt("/transport");
    expect(screen.getByRole("heading", { level: 2, name: "Transportation" })).toBeInTheDocument();
    const areas = screen.getByRole("list", { name: "Transportation areas" });
    expect(within(areas).getByRole("link", { name: "Open rate shop" })).toHaveAttribute("href", "/transport/rate-shop");
    expect(within(areas).getByRole("link", { name: "Open carrier invoices" })).toHaveAttribute("href", "/transport/carrier-invoices");
    expect(pageViolations(container)).toEqual([]);
  });
});

describe("Rate cards", () => {
  it("lists rate cards from the service", async () => {
    handler = (url) => (url.pathname === "/api/v1/transport/rate-cards" ? json(200, { items: [card] }) : undefined);
    const { container } = renderAt("/transport/rate-cards");
    const table = await findTable({ name: "Rate cards" });
    await waitFor(() => expect(table).toHaveTextContent("WEIGHT_BREAK"));
    expect(table).toHaveTextContent("2026-01-01");
    expect(table).toHaveTextContent("Open");
    expect(table).toHaveTextContent("15.00");
    expect(screen.getByRole("link", { name: "New rate card" })).toHaveAttribute("href", "/transport/rate-cards/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("filters by carrier", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/transport/rate-cards");
    expect(await screen.findByText("No rate cards yet")).toBeInTheDocument();
    fill("Carrier id", CARRIER);
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("carrierId")).toBe(CARRIER));
  });

  it("shows the error with its code and retries", async () => {
    let fail = true;
    handler = () => (fail ? json(503, { code: "COM-0503", detail: "Service unavailable" }) : json(200, { items: [card] }));
    renderAt("/transport/rate-cards");
    expect(await screen.findByText(/Service unavailable/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("WEIGHT_BREAK")).toBeInTheDocument();
  });
});

describe("New rate card", () => {
  function fillCard() {
    fill("Carrier id", CARRIER);
    fill("Service id", SERVICE);
    fill("Valid from", "2026-01-01");
    fill("Row 1 rate", "0.50");
  }

  it("shows field errors without calling the service", async () => {
    renderAt("/transport/rate-cards/new");
    fireEvent.click(screen.getByRole("button", { name: "Save rate card" }));
    expect(await screen.findByText("Enter the carrier id as a lower-case UUID.")).toBeInTheDocument();
    expect(screen.getByText("Enter the first day the card applies.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("creates the card with an Idempotency-Key and returns to the list", async () => {
    handler = (url, init) => (init.method === "POST" && url.pathname.endsWith("/rate-cards") ? json(201, card) : json(200, { items: [card] }));
    renderAt("/transport/rate-cards/new");
    fillCard();
    fireEvent.click(screen.getByRole("button", { name: "Add accessorial" }));
    fill("Accessorial 1 code", "liftgate");
    fill("Accessorial 1 amount", "25");
    fireEvent.click(screen.getByRole("button", { name: "Save rate card" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent("/transport/rate-cards"));
    const [post] = posts("/rate-cards");
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    expect(post.body).toMatchObject({
      carrierId: CARRIER,
      basis: "LANE",
      rows: [{ origin: "*", destination: "*", rate: "0.50", unit: "KG", breakTo: null }],
      accessorials: [{ code: "LIFTGATE", flat: "25", pct: "0" }],
    });
  });

  it("explains an overlapping validity (TMS-0102) on the valid-from field", async () => {
    handler = () => json(422, { code: "TMS-0102", detail: "rate card overlaps rc-1 (2026-01-01 to open)" });
    renderAt("/transport/rate-cards/new");
    fillCard();
    fireEvent.click(screen.getByRole("button", { name: "Save rate card" }));
    expect(await screen.findByText(/overlaps rc-1/)).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent("/transport/rate-cards/new");
  });
});

describe("Rate shop", () => {
  const ok = (carrierId: string, total: string): Quote => ({
    carrierId,
    serviceId: SERVICE,
    currency: "USD",
    status: "OK",
    total,
    charges: [
      { code: "BASE", amount: "40" },
      { code: "FUEL", amount: "7.4" },
    ],
    trace: [
      { step: "chargeable_weight", input: "max(actual 80 kg, dimensional)", amount: "80" },
      { step: "minimum", input: "minimum charge 15 not reached", amount: "40" },
    ],
  });
  const none: Quote = { carrierId: CARRIER_B, serviceId: SERVICE, currency: "USD", status: "NO_VALID_RATE_CARD", total: "0", charges: [], trace: [] };

  function fillShipment() {
    fill("Shipment id", LOAD);
    fill("Origin zone", "chi");
    fill("Destination zone", "dal");
    fill("Weight (kg)", "80");
    fill("Ship date", "2026-06-01");
  }

  it("ranks the options, marks the cheapest and opens its explain trace", async () => {
    handler = (url) => (url.pathname.endsWith(":rate") ? json(200, { shipmentId: LOAD, options: [ok(CARRIER, "47.4"), none], chosen: ok(CARRIER, "47.4") }) : undefined);
    const { container } = renderAt("/transport/rate-shop");
    fillShipment();
    fireEvent.click(screen.getByRole("button", { name: "Rate shop" }));
    await waitFor(() => expect(screen.getByRole("table", { name: "Rate options" })).toHaveTextContent("47.40 USD"));
    const table = screen.getByRole("table", { name: "Rate options" });
    expect(table).toHaveTextContent("Cheapest");
    expect(table).toHaveTextContent("No valid rate card");
    expect(screen.getByRole("status")).toHaveTextContent("1 of 2 carrier services rated.");
    const trace = screen.getByRole("list", { name: "Explain trace" });
    expect(trace).toHaveTextContent("chargeable_weight");
    expect(trace).toHaveTextContent("minimum charge 15 not reached = 40.00");
    const [post] = posts(":rate");
    expect(post.url.pathname).toBe(`/api/v1/transport/shipments/${LOAD}:rate`);
    expect(post.body).toMatchObject({ originZone: "CHI", destinationZone: "DAL", weightKg: "80", pallets: 0 });
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows an empty state when no carrier has a rate card", async () => {
    handler = () => json(200, { shipmentId: LOAD, options: [] });
    renderAt("/transport/rate-shop");
    fillShipment();
    fireEvent.click(screen.getByRole("button", { name: "Rate shop" }));
    expect(await screen.findByText("No rate cards apply")).toBeInTheDocument();
  });

  it("validates before calling and reports a failed call", async () => {
    renderAt("/transport/rate-shop");
    fireEvent.click(screen.getByRole("button", { name: "Rate shop" }));
    expect(await screen.findByText("Enter the shipment id as a lower-case UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    handler = () => json(403, { code: "AUTH-0003", detail: "missing duty transport.rate-card.maintain" });
    fillShipment();
    fireEvent.click(screen.getByRole("button", { name: "Rate shop" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("missing duty transport.rate-card.maintain"));
  });
});

describe("Freight bills", () => {
  it("lists bills, filters by match status and opens one", async () => {
    handler = (url) => (url.pathname === "/api/v1/transport/freight-bills" ? json(200, { items: [bill({ matchStatus: "VARIANCE", varianceTotal: "5.5" })] }) : undefined);
    const { container } = renderAt("/transport/freight-bills");
    const table = await findTable({ name: "Freight bills" });
    await waitFor(() => expect(table).toHaveTextContent("412.50 USD"));
    expect(table).toHaveTextContent("5.50 USD");
    expect(table).toHaveTextContent("Variance");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Match status"), { target: { value: "VARIANCE" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("matchStatus")).toBe("VARIANCE"));
    handler = (url) => (url.pathname === `/api/v1/transport/freight-bills/${BILL}` ? json(200, bill()) : json(200, { items: [bill()] }));
    fireEvent.click(await screen.findByRole("button", { name: BILL.slice(0, 8) + "…" }));
    expect(screen.getByTestId("location")).toHaveTextContent(`/transport/freight-bills/${BILL}`);
    expect(await screen.findByText("No carrier invoice yet")).toBeInTheDocument();
  });

  it("shows the empty state for a filter", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/transport/freight-bills");
    expect(await screen.findByText("No freight bills yet")).toBeInTheDocument();
  });

  it("creates a bill and opens it", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, bill()) : json(200, { ...bill() }));
    renderAt("/transport/freight-bills/new");
    fill("Load id", LOAD);
    fill("Carrier id", CARRIER);
    fill("Charge 1 amount", "400.00");
    fireEvent.click(screen.getByRole("button", { name: "Add charge" }));
    fill("Charge 2 code", "fuel");
    fill("Charge 2 amount", "12.50");
    fireEvent.click(screen.getByRole("button", { name: "Create freight bill" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent(`/transport/freight-bills/${BILL}`));
    const [post] = posts("/freight-bills");
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    expect(post.body).toEqual({
      loadId: LOAD,
      carrierId: CARRIER,
      currency: "USD",
      expectedLines: [
        { chargeCode: "BASE", amount: "400.00" },
        { chargeCode: "FUEL", amount: "12.50" },
      ],
    });
  });

  it("checks the charge lines before calling", async () => {
    renderAt("/transport/freight-bills/new");
    fill("Load id", LOAD);
    fill("Carrier id", CARRIER);
    fill("Charge 1 amount", "-3");
    fireEvent.click(screen.getByRole("button", { name: "Create freight bill" }));
    expect(await screen.findByText("Enter an amount such as 412.50.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});

describe("Freight bill detail", () => {
  it("shows expected vs invoiced per charge and highlights lines outside tolerance (AC-34.4)", async () => {
    const detail: FreightBillDetail = {
      ...bill({ matchStatus: "VARIANCE", varianceTotal: "5.5", carrierInvoiceId: INVOICE }),
      carrierInvoice: invoice(),
      match: {
        status: "VARIANCE",
        varianceTotal: "5.5",
        lines: [{ chargeCode: "BASE", expected: "412.5", invoiced: "418", variance: "5.5", allowed: "5", within: false }],
      },
    };
    handler = (url) => (url.pathname === `/api/v1/transport/freight-bills/${BILL}` ? json(200, detail) : undefined);
    const { container } = renderAt(`/transport/freight-bills/${BILL}`);
    const grid = await findTable({ name: "Expected and invoiced charges" });
    expect(grid).toHaveTextContent("412.50");
    expect(grid).toHaveTextContent("418.00");
    expect(grid).toHaveTextContent("Outside");
    expect(screen.getByRole("heading", { name: "Matched to invoice INV-1" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Register carrier invoice" })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("offers to register the invoice for an unmatched bill", async () => {
    handler = () => json(200, bill());
    renderAt(`/transport/freight-bills/${BILL}`);
    const link = await screen.findByRole("link", { name: "Register carrier invoice" });
    expect(link).toHaveAttribute("href", `/transport/carrier-invoices/new?freightBillId=${BILL}&carrierId=${CARRIER}&currency=USD`);
    expect(screen.getByText("No carrier invoice yet")).toBeInTheDocument();
  });

  it("shows a not-found problem", async () => {
    handler = () => json(404, { code: "TMS-0100", detail: "freight bill not found" });
    renderAt(`/transport/freight-bills/${BILL}`);
    expect(await screen.findByText("freight bill not found")).toBeInTheDocument();
  });
});

describe("Carrier invoices", () => {
  it("lists invoices with their bill link and filters the exception queue", async () => {
    handler = () => json(200, { items: [invoice(), invoice({ id: "x", invoiceNo: "INV-2", freightBillId: undefined, matchStatus: "UNEXPECTED_INVOICE", varianceTotal: undefined })] });
    const { container } = renderAt("/transport/carrier-invoices");
    const table = await findTable({ name: "Carrier invoices" });
    await waitFor(() => expect(table).toHaveTextContent("INV-2"));
    expect(table).toHaveTextContent("Unexpected invoice");
    expect(within(table).getByRole("link", { name: BILL.slice(0, 8) + "…" })).toHaveAttribute("href", `/transport/freight-bills/${BILL}`);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Match status"), { target: { value: "UNEXPECTED_INVOICE" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("matchStatus")).toBe("UNEXPECTED_INVOICE"));
  });

  it("registers an invoice prefilled from the bill and opens the matched bill", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(201, { invoice: invoice(), freightBill: bill({ matchStatus: "VARIANCE" }) }) : json(200, bill({ matchStatus: "VARIANCE" }));
    renderAt(`/transport/carrier-invoices/new?freightBillId=${BILL}&carrierId=${CARRIER}&currency=USD`);
    expect(screen.getByLabelText("Freight bill id")).toHaveValue(BILL);
    expect(screen.getByLabelText("Carrier id")).toHaveValue(CARRIER);
    fill("Invoice number", " INV-1 ");
    fill("Invoice date", "2026-10-27");
    fill("Charge 1 amount", "418.00");
    fireEvent.click(screen.getByRole("button", { name: "Register invoice" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent(`/transport/freight-bills/${BILL}`));
    expect(posts("/carrier-invoices")[0].body).toEqual({
      carrierId: CARRIER,
      invoiceNo: "INV-1",
      invoiceDate: "2026-10-27",
      source: "MANUAL",
      freightBillId: BILL,
      currency: "USD",
      lines: [{ chargeCode: "BASE", amount: "418.00" }],
    });
  });

  it("puts a duplicate invoice number (TMS-0108) on the invoice number field", async () => {
    handler = () => json(409, { code: "TMS-0108", detail: "carrier invoice INV-1 is already registered for this carrier" });
    renderAt("/transport/carrier-invoices/new");
    fill("Carrier id", CARRIER);
    fill("Invoice number", "INV-1");
    fill("Invoice date", "2026-10-27");
    fill("Charge 1 amount", "10");
    fireEvent.click(screen.getByRole("button", { name: "Register invoice" }));
    expect(await screen.findByText("This invoice number is already registered for the carrier.")).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent("/transport/carrier-invoices/new");
  });

  it("shows currency and carrier mismatches (TMS-0112) on their fields", async () => {
    handler = () =>
      json(422, { code: "TMS-0112", detail: "the invoice does not belong to this freight bill", errors: { currency: "must be the freight bill currency USD" } });
    renderAt(`/transport/carrier-invoices/new?freightBillId=${BILL}`);
    fill("Carrier id", CARRIER);
    fill("Invoice number", "INV-9");
    fill("Invoice date", "2026-10-27");
    fill("Currency", "eur");
    fill("Charge 1 amount", "10");
    fireEvent.click(screen.getByRole("button", { name: "Register invoice" }));
    expect(await screen.findByText("must be the freight bill currency USD")).toBeInTheDocument();
    expect(posts("/carrier-invoices")[0].body).toMatchObject({ currency: "EUR" });
  });
});
