import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { configureApi } from "../src/api";
import { App } from "../src/App";
import { NewOrder } from "../src/pages/NewOrder";
import { NewReturn } from "../src/pages/NewReturn";
import { OrderDetail } from "../src/pages/Orders";
import { ReturnDetail } from "../src/pages/Returns";

const PUMP = "22222222-2222-4222-8222-2222222222a1";
const VALVE = "22222222-2222-4222-8222-2222222222a2";
const ORDER_ID = "33333333-3333-4333-8333-333333333301";
const RETURN_ID = "33333333-3333-4333-8333-333333333302";
const INVOICE = "44444444-4444-4444-8444-444444444401";
const HQ = "55555555-5555-4555-8555-555555555501";
const STORE = "55555555-5555-4555-8555-555555555502";
const LE_1 = "66666666-6666-4666-8666-666666666601";
const LE_2 = "66666666-6666-4666-8666-666666666602";

const products = { items: [
  { productId: PUMP, productName: "Centrifugal pump", unit: "EA", unitPrice: "108", currency: "USD", lastOrderedAt: "2027-02-01T00:00:00Z" },
  { productId: VALVE, productName: "Gate valve", unit: "EA", unitPrice: "40", currency: "USD", lastOrderedAt: "2027-01-01T00:00:00Z" },
] };
const placed = {
  id: ORDER_ID, orderNumber: "SO-0000042", orderType: "ORDER", status: "OPEN", currency: "USD", requestedShipDate: "2099-01-02", externalRef: "PO-77", netTotal: "324", createdAt: "2027-03-01T00:00:00Z",
  lines: [{ lineNo: 1, productId: PUMP, productName: "Centrifugal pump", quantity: "3", unit: "EA", unitPrice: "108", lineNet: "324", requestedShipDate: "2099-01-02", lineStatus: "OPEN", deliveredQty: "0", invoicedQty: "0" }],
};
const invoices = { items: [
  { id: INVOICE, type: "SHIPMENT", status: "POSTED", number: "INV-2027-1", currency: "USD", invoiceDate: "2027-03-01" },
  { id: "44444444-4444-4444-8444-444444444402", type: "CREDIT_NOTE", status: "POSTED", number: "CN-1", currency: "USD", invoiceDate: "2027-03-05" },
] };
const invoice = { ...invoices.items[0], lines: [
  { lineNo: 1, productId: PUMP, description: "Centrifugal pump", qty: "4" },
  { lineNo: 2, description: "Freight", qty: "1" },
] };
const created = {
  id: RETURN_ID, rmaNumber: "RMA-0000007", originalInvoiceNumber: "INV-2027-1", returnReasonCode: "DAMAGED", status: "REQUESTED", currency: "USD",
  gross: "0", restocking: "0", net: "0", tax: "0", createdAt: "2027-03-02T00:00:00Z",
  lines: [{ lineNo: 1, productId: PUMP, productName: "Centrifugal pump", quantityExpected: "2", quantityReceived: "0", quantityAccepted: "0", unit: "EA", unitPrice: "108" }],
};

interface Call { method: string; url: string; headers: Record<string, string>; body?: string }
let calls: Call[];
let routes: Record<string, unknown | ((c: Call) => Response | unknown)>;

beforeEach(() => {
  calls = [];
  routes = {};
  configureApi({ csrfToken: "csrf-1", legalEntityId: "le-1", accountId: "", onSignedOut: () => undefined });
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const call: Call = { method, url, headers: (init?.headers ?? {}) as Record<string, string>, ...(typeof init?.body === "string" ? { body: init.body } : {}) };
    calls.push(call);
    const key = `${method} ${url.split("?")[0]}`;
    if (!(key in routes)) return new Response(JSON.stringify({ code: "NF", detail: `no stub for ${key}` }), { status: 404 });
    const r = typeof routes[key] === "function" ? (routes[key] as (c: Call) => unknown)(call) : routes[key];
    return r instanceof Response ? r : new Response(JSON.stringify(r), { status: 200, headers: { "content-type": "application/json" } });
  }));
});

async function accessible(container: HTMLElement) {
  const r = await axe.run(container, { rules: { "color-contrast": { enabled: false } } });
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.html}`)).toEqual([]);
}

function orderApp(entry: string | { pathname: string; state: unknown }) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/orders/new" element={<NewOrder />} />
        <Route path="/orders/:id" element={<OrderDetail />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("placing an order", () => {
  it("offers only what the customer has bought, by name, and posts a product, a unit and a quantity", async () => {
    routes["GET /api/v1/products"] = products;
    routes["POST /api/v1/orders"] = () => new Response(JSON.stringify(placed), { status: 201, headers: { "content-type": "application/json" } });
    routes[`GET /api/v1/orders/${ORDER_ID}`] = placed;
    const { container } = orderApp("/orders/new");
    expect(await screen.findByRole("option", { name: "Centrifugal pump (EA, 108 USD last time)" })).toBeInTheDocument();
    expect(container.textContent).not.toContain(PUMP);
    await accessible(container);

    await userEvent.clear(screen.getByLabelText("Quantity 1"));
    await userEvent.type(screen.getByLabelText("Quantity 1"), "3");
    await userEvent.type(screen.getByLabelText("Ship on"), "2099-01-02");
    await userEvent.type(screen.getByLabelText("Your reference"), "PO-77");
    await userEvent.click(screen.getByRole("button", { name: "Place the order" }));

    expect(await screen.findByText(/Your order SO-0000042 has been placed/)).toBeInTheDocument();
    expect(screen.getAllByText("Centrifugal pump").length).toBeGreaterThan(0);
    const sent = calls.find((c) => c.method === "POST")!;
    expect(sent.url).toBe("/api/v1/orders");
    expect(JSON.parse(sent.body as string)).toEqual({ requestedShipDate: "2099-01-02", externalRef: "PO-77", lines: [{ productId: PUMP, unit: "EA", quantity: "3" }] });
    expect(sent.headers["x-csrf-token"]).toBe("csrf-1");
    expect(sent.headers["idempotency-key"]).toMatch(/^p-[0-9a-f-]{36}$/);
    expect(sent.headers["x-legal-entity"]).toBe("le-1");
    expect(sent.body).not.toMatch(/customer|price|warehouse|site/i);
  });

  it("checks the date and the quantities before anything is sent", async () => {
    routes["GET /api/v1/products"] = products;
    orderApp("/orders/new");
    await screen.findByLabelText("Quantity 1");
    await userEvent.click(screen.getByRole("button", { name: "Place the order" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Choose the date");
    await userEvent.type(screen.getByLabelText("Ship on"), "2099-01-02");
    await userEvent.clear(screen.getByLabelText("Quantity 1"));
    await userEvent.type(screen.getByLabelText("Quantity 1"), "0");
    await userEvent.click(screen.getByRole("button", { name: "Place the order" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("more than zero");
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("sends a retry of the same order with the same key, and a changed order with a new one", async () => {
    routes["GET /api/v1/products"] = products;
    let answers = 0;
    routes["POST /api/v1/orders"] = () => (++answers === 1 ? new Response(JSON.stringify({ code: "COM-0500", detail: "try again" }), { status: 503 }) : new Response(JSON.stringify({ code: "SAL-CUS-002", detail: "some lines cannot be ordered", errors: { "lines[0].productId": "is not a product you have ordered before" } }), { status: 422 }));
    orderApp("/orders/new");
    await screen.findByLabelText("Quantity 1");
    await userEvent.type(screen.getByLabelText("Ship on"), "2099-01-02");
    await userEvent.click(screen.getByRole("button", { name: "Place the order" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("try again");
    await userEvent.click(screen.getByRole("button", { name: "Place the order" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "POST")).toHaveLength(2));
    expect(await screen.findByRole("alert")).toHaveTextContent("is not a product you have ordered before");
    const [first, second] = calls.filter((c) => c.method === "POST");
    expect(second?.headers["idempotency-key"]).toBe(first?.headers["idempotency-key"]);
    await userEvent.clear(screen.getByLabelText("Quantity 1"));
    await userEvent.type(screen.getByLabelText("Quantity 1"), "5");
    await userEvent.click(screen.getByRole("button", { name: "Place the order" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "POST")).toHaveLength(3));
    expect(calls.filter((c) => c.method === "POST")[2]?.headers["idempotency-key"]).not.toBe(first?.headers["idempotency-key"]);
  });

  it("adds and removes lines, and starts from an earlier order's lines when ordering again", async () => {
    routes["GET /api/v1/products"] = products;
    orderApp({ pathname: "/orders/new", state: { lines: [{ productId: VALVE, unit: "EA", quantity: "2" }, { productId: "99999999-9999-4999-8999-999999999999", unit: "EA", quantity: "1" }] } });
    const valve = await screen.findByLabelText("Product 1");
    expect(valve).toHaveValue(`${VALVE}|EA`);
    expect(screen.getByLabelText("Quantity 1")).toHaveValue("2");
    expect(screen.queryByLabelText("Product 2")).toBeNull(); // a product the customer no longer can order is dropped
    await userEvent.click(screen.getByRole("button", { name: "Add a line" }));
    expect(screen.getByLabelText("Product 2")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Remove line 2" }));
    expect(screen.queryByLabelText("Product 2")).toBeNull();
  });

  it("says so when there is nothing to order yet", async () => {
    routes["GET /api/v1/products"] = { items: [] };
    orderApp("/orders/new");
    expect(await screen.findByText("Nothing here yet.")).toBeInTheDocument();
  });
});

function returnApp() {
  return render(
    <MemoryRouter initialEntries={["/returns/new"]}>
      <Routes>
        <Route path="/returns/new" element={<NewReturn />} />
        <Route path="/returns/:id" element={<ReturnDetail />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("requesting a return", () => {
  it("returns goods of one of the customer's invoices, and says which lines it cannot take", async () => {
    routes["GET /api/v1/invoices"] = invoices;
    routes[`GET /api/v1/invoices/${INVOICE}`] = invoice;
    routes["POST /api/v1/returns"] = () => new Response(JSON.stringify(created), { status: 201, headers: { "content-type": "application/json" } });
    routes[`GET /api/v1/returns/${RETURN_ID}`] = created;
    const { container } = returnApp();
    expect(await screen.findByText("Centrifugal pump")).toBeInTheDocument();
    // A credit note is not an invoice to return.
    expect(screen.queryByRole("option", { name: /CN-1/ })).toBeNull();
    expect(screen.getByText(/1 line of this invoice cannot be returned here/)).toBeInTheDocument();
    expect(container.textContent).not.toContain(INVOICE);
    await accessible(container);

    await userEvent.type(screen.getByLabelText("Quantity to return of Centrifugal pump"), "2");
    await userEvent.selectOptions(screen.getByLabelText("Reason"), "DAMAGED");
    await userEvent.click(screen.getByRole("button", { name: "Request the return" }));
    expect(await screen.findByText(/Your return RMA-0000007 has been requested/)).toBeInTheDocument();
    const sent = calls.find((c) => c.method === "POST")!;
    expect(sent.url).toBe("/api/v1/returns");
    expect(JSON.parse(sent.body as string)).toEqual({ originalInvoiceId: INVOICE, returnReasonCode: "DAMAGED", lines: [{ productId: PUMP, quantity: "2" }] });
    expect(sent.headers["idempotency-key"]).toMatch(/^p-/);
    expect(sent.body).not.toMatch(/customer/i);
  });

  it("refuses an empty return or more than was invoiced before anything is sent", async () => {
    routes["GET /api/v1/invoices"] = invoices;
    routes[`GET /api/v1/invoices/${INVOICE}`] = invoice;
    returnApp();
    await screen.findByText("Centrifugal pump");
    await userEvent.click(screen.getByRole("button", { name: "Request the return" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("at least one line");
    await userEvent.type(screen.getByLabelText("Quantity to return of Centrifugal pump"), "5");
    await userEvent.click(screen.getByRole("button", { name: "Request the return" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("at most 4");
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("shows the answer when the invoice cannot be returned", async () => {
    routes["GET /api/v1/invoices"] = invoices;
    routes[`GET /api/v1/invoices/${INVOICE}`] = invoice;
    routes["POST /api/v1/returns"] = new Response(JSON.stringify({ code: "SAL-CUS-003", detail: "that invoice is not one of your posted invoices" }), { status: 422 });
    returnApp();
    await screen.findByText("Centrifugal pump");
    await userEvent.type(screen.getByLabelText("Quantity to return of Centrifugal pump"), "1");
    await userEvent.click(screen.getByRole("button", { name: "Request the return" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("not one of your posted invoices");
  });
});

describe("the shell", () => {
  const me = { userId: "u", name: "Pat Buyer", legalEntityIds: [LE_1, LE_2], csrfToken: "csrf-1" };

  it("names the companies, and lists the accounts the contact may act for, sending X-Customer only for another account", async () => {
    routes["GET /bff/me"] = me;
    routes["GET /api/v1/companies"] = { items: [{ legalEntityId: LE_1, name: "Cedar Ridge Pumps Inc." }, { legalEntityId: LE_2, name: null }] };
    routes["GET /api/v1/accounts"] = { items: [
      { id: HQ, accountNumber: "C-1", name: "Head office", relation: "self" },
      { id: STORE, accountNumber: "C-2", name: "Store two", relation: "ship-to" },
    ] };
    routes["GET /api/v1/open-items"] = { items: [] };
    routes["GET /api/v1/orders"] = { items: [] };
    const { container } = render(<MemoryRouter initialEntries={["/orders"]}><App /></MemoryRouter>);
    expect(await screen.findByRole("option", { name: "Cedar Ridge Pumps Inc." })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Company 2" })).toBeInTheDocument(); // no name from identity: a numbered one, never the id
    expect(await screen.findByRole("option", { name: "Store two (C-2, ships for you)" })).toBeInTheDocument();
    expect(container.textContent).not.toContain(HQ);
    const header = (url: string) => calls.filter((c) => c.url.startsWith(url)).map((c) => c.headers["x-customer"]);
    expect(header("/api/v1/orders")).toEqual([undefined]);
    expect(calls.find((c) => c.url === "/api/v1/accounts")?.headers["x-legal-entity"]).toBe(LE_1);

    await userEvent.selectOptions(screen.getByLabelText("Acting for"), STORE);
    await waitFor(() => expect(header("/api/v1/orders")).toEqual([undefined, STORE]));
    // The accounts list itself is always the contact's own, whichever account is chosen.
    expect(calls.filter((c) => c.url === "/api/v1/accounts").every((c) => c.headers["x-customer"] === undefined)).toBe(true);
    // Changing the company starts again from the contact's own account.
    await userEvent.selectOptions(screen.getByLabelText(/Account with/), LE_2);
    await waitFor(() => expect(calls.filter((c) => c.url.startsWith("/api/v1/orders")).length).toBeGreaterThan(2));
    expect(calls.filter((c) => c.url.startsWith("/api/v1/orders")).at(-1)?.headers["x-customer"]).toBeUndefined();
    await accessible(container);
  });

  it("shows no account selector for a contact with a single account", async () => {
    routes["GET /bff/me"] = { ...me, legalEntityIds: [LE_1] };
    routes["GET /api/v1/accounts"] = { items: [{ id: HQ, accountNumber: "C-1", name: "Head office", relation: "self" }] };
    routes["GET /api/v1/open-items"] = { items: [] };
    render(<MemoryRouter initialEntries={["/"]}><App /></MemoryRouter>);
    await screen.findByText("You have nothing to pay.");
    expect(screen.queryByLabelText("Acting for")).toBeNull();
    expect(screen.queryByLabelText(/Account with/)).toBeNull();
  });

  it("signs out at Keycloak: the session ends here and the browser goes to the end-session URL", async () => {
    routes["GET /bff/me"] = { ...me, legalEntityIds: [LE_1] };
    routes["GET /api/v1/open-items"] = { items: [] };
    routes["POST /bff/logout"] = { logoutUrl: "https://keycloak.example.invalid/realms/passage/protocol/openid-connect/logout?id_token_hint=x" };
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    render(<MemoryRouter initialEntries={["/"]}><App /></MemoryRouter>);
    await screen.findByText("You have nothing to pay.");
    await userEvent.click(within(screen.getByRole("banner")).getByRole("button", { name: "Sign out" }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("https://keycloak.example.invalid/realms/passage/protocol/openid-connect/logout?id_token_hint=x"));
    expect(calls.find((c) => c.url === "/bff/logout")?.headers["x-csrf-token"]).toBe("csrf-1");
  });

  it("goes home when the sign-out call fails", async () => {
    routes["GET /bff/me"] = { ...me, legalEntityIds: [LE_1] };
    routes["GET /api/v1/open-items"] = { items: [] };
    routes["POST /bff/logout"] = new Response("{}", { status: 503 });
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    render(<MemoryRouter initialEntries={["/"]}><App /></MemoryRouter>);
    await screen.findByText("You have nothing to pay.");
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/"));
  });
});
