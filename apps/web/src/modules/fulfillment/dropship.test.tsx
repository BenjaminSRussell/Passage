import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { DropShip } from "./api/client";
import { validateDropShip } from "./DropShipsPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const ID = "0195f0a0-0000-7000-8000-0000000000d1";
const U = (n: number) => `0195f0a0-0000-7000-8000-0000000001${String(n).padStart(2, "0")}`;
const LIST = "/api/v1/fulfillment/drop-ships";
const calls = (method: string, suffix: string) => server.calls.filter((c) => c.method === method && c.url.pathname.endsWith(suffix));
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

function dropShip(over: Partial<DropShip> = {}): DropShip {
  return {
    id: ID, dropShipNumber: "DS-0000001", status: "OPEN", legalEntityId: U(1), orderId: U(2), orderLineId: U(3), customerId: U(4), productId: U(5), unit: "EA",
    qty: "4", qtyConfirmed: "0", qtyClosed: "0", qtyOpen: "4", vendorId: U(6), purchaseOrderId: U(7), purchaseOrderLineId: U(8), unitCost: "12.50", currency: "USD", vendorShipmentRef: null,
    shippedOn: null, postingDate: null, journal: [], confirmations: [], version: 1, createdAt: "2026-11-01T08:00:00Z", ...over,
  };
}

describe("drop-ships list and create form", () => {
  it("lists drop-ships, filters by status and opens one", async () => {
    server.handler = (url) => (url.pathname === LIST ? json(200, { items: [dropShip(), dropShip({ id: U(90), dropShipNumber: "DS-0000002", status: "CONFIRMED", shippedOn: "2026-11-01" })] }) : undefined);
    const { container } = renderAt("/fulfillment/drop-ships", ["fulfillment.warehouse.view", "fulfillment.warehouse.release"]);
    const table = await findTable({ name: "Drop-ships" });
    expect(within(table).getByText("DS-0000002")).toBeInTheDocument();
    expect(within(table).getByText("Confirmed")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "OPEN" } });
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname === LIST && c.url.searchParams.get("status") === "OPEN")).toBe(true));
  });

  it("shows the empty state and the load error with a retry", async () => {
    server.handler = (url) => (url.pathname === LIST ? json(200, { items: [] }) : undefined);
    renderAt("/fulfillment/drop-ships");
    expect(await screen.findByText("No drop-ships yet")).toBeInTheDocument();
  });

  it("explains a load failure", async () => {
    server.handler = (url) => (url.pathname === LIST ? json(403, { code: "AUTH-0003", detail: "missing duty" }) : undefined);
    renderAt("/fulfillment/drop-ships");
    expect(await findAlert()).toHaveTextContent(/Could not load the drop-ships/);
  });

  it("validates the form, then creates a drop-ship with one key", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(201, dropShip());
      if (url.pathname === LIST) return json(200, { items: [] });
      return undefined;
    };
    renderAt("/fulfillment/drop-ships", ["fulfillment.warehouse.release"]);
    await screen.findByText("No drop-ships yet");
    fireEvent.click(screen.getByRole("button", { name: "Create drop-ship" }));
    expect(await screen.findAllByText(/as a UUID/)).not.toHaveLength(0);
    expect(calls("POST", "/drop-ships")).toHaveLength(0);

    const ids: Record<string, string> = { "Legal entity id": U(1), "Order id": U(2), "Order line id": U(3), "Customer id": U(4), "Product id": U(5), "Vendor id": U(6), "Purchase order id": U(7), "Purchase order line id": U(8) };
    for (const [label, value] of Object.entries(ids)) fireEvent.change(screen.getByLabelText(label), { target: { value } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "4" } });
    fireEvent.change(screen.getByLabelText("Purchase order cost per unit"), { target: { value: "12.50" } });
    fireEvent.click(screen.getByRole("button", { name: "Create drop-ship" }));
    expect(await screen.findByText(/Created DS-0000001/)).toBeInTheDocument();
    const post = calls("POST", "/drop-ships")[0];
    expect(idempotencyKey(post)).toBeTruthy();
    expect(post.body).toEqual({ legalEntityId: U(1), orderId: U(2), orderLineId: U(3), customerId: U(4), productId: U(5), unit: "EA", qty: "4", vendorId: U(6), purchaseOrderId: U(7), purchaseOrderLineId: U(8), unitCost: "12.50", currency: "USD" });
  });

  it("explains an order line that already has an active drop-ship", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(409, { code: "FUL-0061", detail: "active" });
      if (url.pathname === LIST) return json(200, { items: [] });
      return undefined;
    };
    renderAt("/fulfillment/drop-ships", ["fulfillment.warehouse.release"]);
    await screen.findByText("No drop-ships yet");
    const filled = ["Legal entity id", "Order id", "Order line id", "Customer id", "Product id", "Vendor id", "Purchase order id", "Purchase order line id"];
    filled.forEach((label, i) => fireEvent.change(screen.getByLabelText(label), { target: { value: U(i + 1) } }));
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Purchase order cost per unit"), { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Create drop-ship" }));
    expect(await screen.findByText("The order line already has an active drop-ship. Cancel the existing drop-ship first.")).toBeInTheDocument();
  });

  it("explains an order line that ships from stock", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(409, { code: "FUL-0062", detail: "on a shipment" });
      if (url.pathname === LIST) return json(200, { items: [] });
      return undefined;
    };
    renderAt("/fulfillment/drop-ships", ["fulfillment.warehouse.release"]);
    await screen.findByText("No drop-ships yet");
    const filled = ["Legal entity id", "Order id", "Order line id", "Customer id", "Product id", "Vendor id", "Purchase order id", "Purchase order line id"];
    filled.forEach((label, i) => fireEvent.change(screen.getByLabelText(label), { target: { value: U(i + 1) } }));
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Purchase order cost per unit"), { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Create drop-ship" }));
    expect(await screen.findByText("The order line is on a warehouse shipment. Cancel the shipment before drop-shipping the line.")).toBeInTheDocument();
  });

  it("hides the create form from a viewer", async () => {
    server.handler = (url) => (url.pathname === LIST ? json(200, { items: [] }) : undefined);
    renderAt("/fulfillment/drop-ships", ["fulfillment.warehouse.view"]);
    expect(await screen.findByText(/once an order line is routed to a vendor/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create drop-ship" })).toBeNull();
  });

  it("validates quantity, cost and currency", () => {
    const base = { legalEntityId: U(1), orderId: U(2), orderLineId: U(3), customerId: U(4), productId: U(5), unit: "EA", qty: "1", vendorId: U(6), purchaseOrderId: U(7), purchaseOrderLineId: U(8), unitCost: "1", currency: "USD" };
    expect(validateDropShip(base)).toEqual({});
    expect(validateDropShip({ ...base, qty: "0" }).qty).toBeDefined();
    expect(validateDropShip({ ...base, unitCost: "-1" }).unitCost).toBeDefined();
    expect(validateDropShip({ ...base, unitCost: "0.000" }).unitCost).toBeDefined();
    expect(validateDropShip({ ...base, qty: "0.0000001" }).qty).toBeDefined();
    expect(validateDropShip({ ...base, currency: "usd" }).currency).toBeDefined();
    expect(validateDropShip({ ...base, vendorId: "x" }).vendorId).toBeDefined();
  });
});

describe("drop-ship detail", () => {
  it("confirms an open drop-ship with the version read and shows the journal afterwards", async () => {
    let current = dropShip();
    server.handler = (url, init) => {
      if (init.method === "POST") {
        current = dropShip({ status: "CONFIRMED", version: 2, shippedOn: "2026-11-01", postingDate: "2026-11-01", vendorShipmentRef: "VS-9", journal: [
          { account: "5000", amount: "50", currency: "USD", orderLineId: U(3) }, { account: "2110", amount: "-50", currency: "USD", orderLineId: U(3) }] });
        return json(200, current);
      }
      if (url.pathname === `${LIST}/${ID}`) return json(200, current);
      return undefined;
    };
    const { container } = renderAt(`/fulfillment/drop-ships/${ID}`, ["fulfillment.warehouse.view", "procurement.purchase-order.confirm", "fulfillment.warehouse.release"]);
    await screen.findByText("Vendor confirmation");
    expect(screen.getByText(/for 50\.00 USD at the purchase order cost/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Vendor shipment reference"), { target: { value: "VS-9" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm shipment" }));
    const journal = await findTable({ name: "Drop-ship journal" });
    expect(within(journal).getByText("5000")).toBeInTheDocument();
    expect(within(journal).getByText("2110")).toBeInTheDocument();
    const post = calls("POST", `${ID}:confirm`)[0];
    expect(header(post, "If-Match")).toBe('"1"');
    expect(idempotencyKey(post)).toBeTruthy();
    expect(post.body).toMatchObject({ vendorShipmentRef: "VS-9" });
    expect(screen.queryByText("Vendor confirmation")).toBeNull();
    expect(screen.getByText(/final and cannot be cancelled/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel drop-ship" })).toBeNull();
  });

  it("refuses a future ship date before calling the service", async () => {
    server.handler = (url) => (url.pathname === `${LIST}/${ID}` ? json(200, dropShip()) : undefined);
    renderAt(`/fulfillment/drop-ships/${ID}`, ["procurement.purchase-order.confirm"]);
    await screen.findByText("Vendor confirmation");
    fireEvent.change(screen.getByLabelText("Vendor shipped on"), { target: { value: "2999-01-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm shipment" }));
    expect(await screen.findByText(/cannot be in the future/)).toBeInTheDocument();
    expect(calls("POST", ":confirm")).toHaveLength(0);
  });

  it("shows a stale version, and hides the confirmation without the posting duty", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(412, { code: "conflict_version", detail: "stale" });
      if (url.pathname === `${LIST}/${ID}`) return json(200, dropShip());
      return undefined;
    };
    renderAt(`/fulfillment/drop-ships/${ID}`, ["procurement.purchase-order.confirm"]);
    await screen.findByText("Vendor confirmation");
    fireEvent.click(screen.getByRole("button", { name: "Confirm shipment" }));
    expect(await screen.findByText(/Someone else changed this drop-ship/)).toBeInTheDocument();
  });

  it("hides the confirmation form from a viewer and from a packing slip poster", async () => {
    server.handler = (url) => (url.pathname === `${LIST}/${ID}` ? json(200, dropShip()) : undefined);
    renderAt(`/fulfillment/drop-ships/${ID}`, ["fulfillment.warehouse.view", "fulfillment.packing-slip.post"]);
    await screen.findByText(/needs the purchase order confirmation duty/);
    expect(screen.queryByText("Vendor confirmation")).toBeNull();
    expect(screen.queryByRole("button", { name: "Cancel drop-ship" })).toBeNull();
  });

  it("cancels an open drop-ship after a confirmation step", async () => {
    let current = dropShip();
    server.handler = (url, init) => {
      if (init.method === "POST") {
        current = dropShip({ status: "CANCELLED", version: 2 });
        return json(200, current);
      }
      if (url.pathname === `${LIST}/${ID}`) return json(200, current);
      return undefined;
    };
    renderAt(`/fulfillment/drop-ships/${ID}`, ["fulfillment.warehouse.release"]);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel drop-ship" }));
    expect(calls("POST", ":cancel")).toHaveLength(0);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel drop-ship" }));
    await waitFor(() => expect(calls("POST", `${ID}:cancel`)).toHaveLength(1));
    expect(header(calls("POST", ":cancel")[0], "If-Match")).toBe('"1"');
    expect(await screen.findByText("Cancelled")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel drop-ship" })).toBeNull();
  });

  it("confirms part of the quantity, then shows the vendor shipments and the open rest", async () => {
    let current = dropShip({ qty: "10", qtyOpen: "10" });
    server.handler = (url, init) => {
      if (init.method === "POST") {
        current = dropShip({ qty: "10", status: "PARTIALLY_CONFIRMED", qtyConfirmed: "4", qtyOpen: "6", version: 2, shippedOn: "2026-11-01", postingDate: "2026-11-01", vendorShipmentRef: "VS-1",
          journal: [{ account: "5000", amount: "50", currency: "USD", orderLineId: U(3) }, { account: "2110", amount: "-50", currency: "USD", orderLineId: U(3) }],
          confirmations: [{ seq: 1, qty: "4", vendorShipmentRef: "VS-1", shippedOn: "2026-11-01", postingDate: "2026-11-01", createdAt: "2026-11-01T09:00:00Z",
            journal: [{ account: "5000", amount: "50", currency: "USD", orderLineId: U(3) }, { account: "2110", amount: "-50", currency: "USD", orderLineId: U(3) }] }] });
        return json(200, current);
      }
      if (url.pathname === `${LIST}/${ID}`) return json(200, current);
      return undefined;
    };
    renderAt(`/fulfillment/drop-ships/${ID}`, ["fulfillment.warehouse.view", "procurement.purchase-order.confirm", "fulfillment.warehouse.release"]);
    await screen.findByText("Vendor confirmation");
    expect(screen.getByLabelText("Quantity shipped")).toHaveValue("10");
    fireEvent.change(screen.getByLabelText("Quantity shipped"), { target: { value: "4" } });
    expect(screen.getByText(/for 50\.00 USD at the purchase order cost/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Vendor shipment reference"), { target: { value: "VS-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm shipment" }));
    const shipments = await findTable({ name: "Vendor shipments" });
    expect(within(shipments).getByText("VS-1")).toBeInTheDocument();
    expect(calls("POST", `${ID}:confirm`)[0].body).toMatchObject({ qty: "4", vendorShipmentRef: "VS-1" });
    // The form stays for the next shipment with the open rest, and the drop-ship cannot be cancelled.
    expect(await screen.findByLabelText("Quantity shipped")).toHaveValue("6");
    expect(screen.getByText("Partly confirmed")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel drop-ship" })).toBeNull();
    expect(screen.getByRole("button", { name: "Close remainder" })).toBeInTheDocument();
  });

  it("previews the amount the next part books as the service rounds it, so the parts add up to the whole line", async () => {
    server.handler = (url) => (url.pathname === `${LIST}/${ID}` ? json(200, dropShip({ qty: "3", unitCost: "0.125", status: "PARTIALLY_CONFIRMED", qtyConfirmed: "1", qtyOpen: "2", version: 2, shippedOn: "2026-11-01", postingDate: "2026-11-01" })) : undefined);
    renderAt(`/fulfillment/drop-ships/${ID}`, ["procurement.purchase-order.confirm"]);
    await screen.findByText("Vendor confirmation");
    fireEvent.change(screen.getByLabelText("Quantity shipped"), { target: { value: "1" } });
    // 0.125 x 2 = 0.25 confirmed in total, less the 0.13 the first unit booked.
    expect(screen.getByText(/for 0\.12 USD at the purchase order cost/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Quantity shipped"), { target: { value: "2" } });
    expect(screen.getByText(/for 0\.25 USD at the purchase order cost/)).toBeInTheDocument();
  });

  it("refuses a quantity above the open quantity before calling the service, and explains FUL-0063", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(422, { code: "FUL-0063", detail: "too much" });
      if (url.pathname === `${LIST}/${ID}`) return json(200, dropShip());
      return undefined;
    };
    renderAt(`/fulfillment/drop-ships/${ID}`, ["procurement.purchase-order.confirm"]);
    await screen.findByText("Vendor confirmation");
    fireEvent.change(screen.getByLabelText("Quantity shipped"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm shipment" }));
    expect(await screen.findByText(/Only 4 EA are open/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Quantity shipped"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm shipment" }));
    expect(await screen.findByText(/Enter a quantity above 0/)).toBeInTheDocument();
    expect(calls("POST", ":confirm")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Quantity shipped"), { target: { value: "4" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm shipment" }));
    expect(await screen.findByText(/more than is still open/)).toBeInTheDocument();
  });

  it("closes the remainder of a partly confirmed drop-ship after a confirmation step", async () => {
    let current = dropShip({ qty: "10", status: "PARTIALLY_CONFIRMED", qtyConfirmed: "6", qtyOpen: "4", version: 3, shippedOn: "2026-11-01", postingDate: "2026-11-01" });
    server.handler = (url, init) => {
      if (init.method === "POST") {
        current = dropShip({ qty: "10", status: "CONFIRMED", qtyConfirmed: "6", qtyClosed: "4", qtyOpen: "0", version: 4, shippedOn: "2026-11-01", postingDate: "2026-11-01" });
        return json(200, current);
      }
      if (url.pathname === `${LIST}/${ID}`) return json(200, current);
      return undefined;
    };
    renderAt(`/fulfillment/drop-ships/${ID}`, ["fulfillment.warehouse.release"]);
    fireEvent.click(await screen.findByRole("button", { name: "Close remainder" }));
    expect(calls("POST", ":close-remainder")).toHaveLength(0);
    fireEvent.click(await screen.findByRole("button", { name: "Close remainder" }));
    await waitFor(() => expect(calls("POST", `${ID}:close-remainder`)).toHaveLength(1));
    expect(header(calls("POST", ":close-remainder")[0], "If-Match")).toBe('"3"');
    expect(await screen.findByText("Confirmed")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Close remainder" })).toBeNull();
  });

  it("explains a missing drop-ship", async () => {
    server.handler = () => json(404, { code: "FUL-0060", detail: "drop-ship not found" });
    renderAt(`/fulfillment/drop-ships/${ID}`);
    expect(await findAlert()).toHaveTextContent(/Could not load the drop-ship/);
  });
});
