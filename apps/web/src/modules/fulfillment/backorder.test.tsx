import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Shipment, WarehouseSetting } from "./api/client";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const WH = "0195f0a0-0000-7000-8000-000000000007";
const WH2 = "0195f0a0-0000-7000-8000-000000000008";
const SH = "0195f0a0-0000-7000-8000-000000000001";
const SH2 = "0195f0a0-0000-7000-8000-000000000011";
const LINE = "0195f0a0-0000-7000-8000-000000000002";
const ID = "0195f0a0-0000-7000-8000-0000000000aa";
const LE2 = "0195f0a0-0000-7000-8000-0000000000bb";
const SLIP = "0195f0a0-0000-7000-8000-0000000000cc";

const calls = (method: string, suffix: string) => server.calls.filter((c) => c.method === method && c.url.pathname.endsWith(suffix));
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

function shipment(id: string, status: Shipment["status"], extra: Partial<Shipment> = {}, line: Partial<Shipment["lines"][number]> = {}, version = 6): Shipment {
  return {
    id,
    shipmentNumber: id === SH ? "SH-0000042" : "SH-0000043",
    legalEntityId: ID,
    warehouseId: WH,
    orderId: ID,
    customerId: ID,
    status,
    plannedShipDate: "2026-10-26T00:00:00Z",
    actualShipDate: null,
    carrierCode: "UPS",
    version,
    lines: [{ id: LINE, orderLineId: ID, productId: ID, unit: "EA", qtyPlanned: "100", qtyPicked: "90", qtyPacked: "90", qtyShipped: "90", pickConfirmed: true, zone: "A1", ...line }],
    containers: [],
    ...extra,
  };
}

describe("backorder remainder on the shipment detail (FR-28.8)", () => {
  it("moves the remainder to a new shipment after confirmation and links to it", async () => {
    let current = shipment(SH, "PARTIALLY_SHIPPED");
    const created = shipment(SH2, "RELEASED", { backorderOfId: SH }, { qtyPlanned: "10", qtyPicked: "0", qtyPacked: "0", qtyShipped: "0", pickConfirmed: false }, 1);
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":backorder-remainder")) {
        current = shipment(SH, "SHIPPED", { backorderId: SH2 }, { qtyPlanned: "90", qtyClosed: "10" }, 7);
        return json(200, { shipment: current, backorder: created });
      }
      if (url.pathname === `/api/v1/fulfillment/shipments/${SH}`) return json(200, current);
      if (url.pathname === `/api/v1/fulfillment/shipments/${SH}/tracking`) return json(200, { items: [] });
      if (url.pathname === "/api/v1/fulfillment/packing-slips") return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt(`/fulfillment/shipments/${SH}`);
    fireEvent.click(await screen.findByRole("button", { name: "Backorder remainder" }));
    expect(calls("POST", ":backorder-remainder")).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText(/Planned ship date/), { target: { value: "2026-12-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Yes, create the backorder" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Backorder remainder" })).toBeNull());
    const call = calls("POST", ":backorder-remainder")[0];
    expect(header(call, "If-Match")).toBe('"6"');
    expect(idempotencyKey(call)).toBeTruthy();
    expect(call.body).toEqual({ plannedShipDate: "2026-12-01" });
    const link = await screen.findByRole("link", { name: "its backorder shipment" });
    expect(link.getAttribute("href")).toBe(`/fulfillment/shipments/${SH2}`);
  });

  it("sends an empty body for today and rejects a malformed date before calling the service", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(200, { shipment: shipment(SH, "SHIPPED", {}, {}, 7), backorder: shipment(SH2, "RELEASED", {}, {}, 1) });
      if (url.pathname === `/api/v1/fulfillment/shipments/${SH}`) return json(200, shipment(SH, "PARTIALLY_SHIPPED"));
      if (url.pathname.endsWith("/tracking")) return json(200, { items: [] });
      if (url.pathname === "/api/v1/fulfillment/packing-slips") return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/fulfillment/shipments/${SH}`);
    fireEvent.click(await screen.findByRole("button", { name: "Backorder remainder" }));
    fireEvent.change(screen.getByLabelText(/Planned ship date/), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Yes, create the backorder" }));
    await waitFor(() => expect(calls("POST", ":backorder-remainder")).toHaveLength(1));
    expect(calls("POST", ":backorder-remainder")[0].body).toEqual({});
  });

  it("shows the service refusal", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(409, { type: "about:blank", title: "Conflict", status: 409, code: "FUL-0021", detail: "FUL-0021: packed quantity is waiting to ship, post it first" });
      if (url.pathname === `/api/v1/fulfillment/shipments/${SH}`) return json(200, shipment(SH, "PARTIALLY_SHIPPED"));
      if (url.pathname.endsWith("/tracking")) return json(200, { items: [] });
      if (url.pathname === "/api/v1/fulfillment/packing-slips") return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/fulfillment/shipments/${SH}`);
    fireEvent.click(await screen.findByRole("button", { name: "Backorder remainder" }));
    fireEvent.click(screen.getByRole("button", { name: "Yes, create the backorder" }));
    expect(await screen.findByText("Could not create the backorder")).toBeInTheDocument();
  });

  it("offers no backorder for a shipment that shipped nothing, and links a backorder shipment to its origin", async () => {
    server.handler = (url) => {
      if (url.pathname === `/api/v1/fulfillment/shipments/${SH2}`) return json(200, shipment(SH2, "RELEASED", { backorderOfId: SH }, { qtyPicked: "0", qtyPacked: "0", qtyShipped: "0", pickConfirmed: false }, 1));
      if (url.pathname.endsWith("/tracking")) return json(200, { items: [] });
      if (url.pathname === "/api/v1/fulfillment/packing-slips") return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/fulfillment/shipments/${SH2}`);
    await findTable({ name: "Shipment lines" });
    expect(screen.queryByRole("button", { name: "Backorder remainder" })).toBeNull();
    expect(screen.getByRole("link", { name: "an earlier shipment" }).getAttribute("href")).toBe(`/fulfillment/shipments/${SH}`);
  });
});

const settings: WarehouseSetting[] = [
  { warehouseId: WH, legalEntityId: ID, shortPickAction: "KEEP_OPEN", version: 1, updatedAt: "2026-10-26T08:00:00Z" },
  { warehouseId: WH2, legalEntityId: LE2, shortPickAction: "REMAINDER_BACKORDER", version: 2, updatedAt: "2026-10-27T08:00:00Z" },
];

describe("warehouse settings page (FR-28.8)", () => {
  it("lists the warehouses and saves the short pick action with the version read", async () => {
    let rows = settings;
    server.handler = (url, init) => {
      if (init.method === "PUT") {
        rows = [settings[0], { ...settings[1], shortPickAction: "CANCEL_REMAINDER", version: 3 }];
        return json(200, rows[1]);
      }
      if (url.pathname === "/api/v1/fulfillment/warehouse-settings") return json(200, { items: rows });
      return undefined;
    };
    const { container } = renderAt("/fulfillment/warehouse-settings");
    const table = await findTable({ name: "Warehouse settings" });
    expect(within(table).getByText("Move the remainder to a backorder shipment")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: WH2 }));
    fireEvent.change(screen.getByLabelText("Short pick action"), { target: { value: "CANCEL_REMAINDER" } });
    fireEvent.click(screen.getByRole("button", { name: "Save setting" }));
    expect(await screen.findByText(/Saved: close the remainder/)).toBeInTheDocument();
    const put = calls("PUT", WH2)[0];
    expect(header(put, "If-Match")).toBe('"2"');
    expect(idempotencyKey(put)).toBeTruthy();
    expect(put.body).toEqual({ shortPickAction: "CANCEL_REMAINDER", legalEntityId: LE2 });
    const after = await findTable({ name: "Warehouse settings" });
    await waitFor(() => expect(within(after).getByText("Close the remainder")).toBeInTheDocument());
  });

  it("requires the legal entity of a typed-in warehouse and explains a refusal for another legal entity", async () => {
    server.handler = (url, init) => {
      if (init.method === "PUT") return json(422, { type: "about:blank", title: "Unprocessable", status: 422, code: "FUL-0057", detail: "the warehouse belongs to another legal entity" });
      if (url.pathname === "/api/v1/fulfillment/warehouse-settings") return json(200, { items: settings });
      return undefined;
    };
    renderAt("/fulfillment/warehouse-settings");
    await findTable({ name: "Warehouse settings" });
    const typed = "0195f0a0-0000-7000-8000-000000000099";
    fireEvent.change(screen.getByLabelText("Warehouse"), { target: { value: typed } });
    fireEvent.click(screen.getByRole("button", { name: "Save setting" }));
    expect(await screen.findByText(/Enter the id of the legal entity/)).toBeInTheDocument();
    expect(calls("PUT", "")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText(/Legal entity/), { target: { value: LE2 } });
    fireEvent.click(screen.getByRole("button", { name: "Save setting" }));
    expect(await screen.findByText(/belongs to another legal entity/)).toBeInTheDocument();
    const put = calls("PUT", typed)[0];
    expect(header(put, "If-Match")).toBe('"0"');
    expect(put.body).toEqual({ shortPickAction: "REMAINDER_BACKORDER", legalEntityId: LE2 });
  });

  it("checks the warehouse id and shows a stale version", async () => {
    server.handler = (url, init) => {
      if (init.method === "PUT") return json(412, { type: "about:blank", title: "Precondition failed", status: 412, code: "conflict_version", detail: "stale" });
      if (url.pathname === "/api/v1/fulfillment/warehouse-settings") return json(200, { items: settings });
      return undefined;
    };
    renderAt("/fulfillment/warehouse-settings");
    const table = await findTable({ name: "Warehouse settings" });
    fireEvent.click(screen.getByRole("button", { name: "Save setting" }));
    expect(await screen.findByText(/Enter the warehouse id/)).toBeInTheDocument();
    expect(calls("PUT", "")).toHaveLength(0);
    fireEvent.click(within(table).getByRole("button", { name: WH2 }));
    fireEvent.click(screen.getByRole("button", { name: "Save setting" }));
    expect(await screen.findByText("Could not save the setting")).toBeInTheDocument();
    expect(screen.getByText(/Someone else changed this setting/)).toBeInTheDocument();
  });

  it("shows the empty state and the error with retry", async () => {
    let fail = true;
    server.handler = () => (fail ? json(503, { code: "COM-0500", detail: "down" }) : json(200, { items: [] }));
    renderAt("/fulfillment/warehouse-settings");
    expect((await findAlert()).textContent).toContain("COM-0500");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No warehouses yet")).toBeInTheDocument();
  });
});

describe("packing slip post with a short pick action (FR-28.8)", () => {
  function slip(): Record<string, unknown> {
    return {
      id: SLIP,
      packingSlipNumber: "PS-0000007",
      shipmentId: SH,
      orderId: ID,
      status: "POSTED",
      postingDate: "2026-10-26T00:00:00Z",
      currency: "USD",
      totalCost: "90",
      reversalOfId: null,
      lines: [],
      journal: [],
      packagingFees: [],
    };
  }

  it("tells the poster that the remainder moved to a backorder shipment and links to it", async () => {
    const packed = shipment(SH, "PACKED", {}, { qtyPicked: "90", qtyPacked: "90", qtyShipped: "0" });
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":post-packing-slip")) {
        return json(200, { ...slip(), remainderAction: "REMAINDER_BACKORDER", backorderShipmentId: SH2 });
      }
      if (url.pathname === `/api/v1/fulfillment/shipments/${SH}`) return json(200, packed);
      if (url.pathname === `/api/v1/fulfillment/packing-slips/${SLIP}`) return json(200, slip());
      if (url.pathname.endsWith("/tracking")) return json(200, { items: [] });
      if (url.pathname === "/api/v1/fulfillment/packing-slips") return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/fulfillment/shipments/${SH}`);
    fireEvent.change(await screen.findByLabelText("Unit cost"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Cost snapshot id"), { target: { value: "snap-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Post packing slip" }));
    expect(await screen.findByRole("heading", { name: "Packing slip PS-0000007" })).toBeInTheDocument();
    expect(await screen.findByText("Remainder moved to a backorder shipment")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "the backorder shipment" }).getAttribute("href")).toBe(`/fulfillment/shipments/${SH2}`);
  });

  it("says nothing about the remainder when the slip is opened directly", async () => {
    server.handler = (url) => (url.pathname === `/api/v1/fulfillment/packing-slips/${SLIP}` ? json(200, slip()) : undefined);
    renderAt(`/fulfillment/packing-slips/${SLIP}`);
    expect(await screen.findByRole("heading", { name: "Packing slip PS-0000007" })).toBeInTheDocument();
    expect(screen.queryByText(/Remainder/)).toBeNull();
  });
});
