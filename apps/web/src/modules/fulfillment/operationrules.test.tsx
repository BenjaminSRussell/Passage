import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { OperationRule, Shipment } from "./api/client";
import { validateOperationRule } from "./OperationRulesPage";
import { validateWorkbench } from "./WorkbenchPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const WH = "0195f0a0-0000-7000-8000-000000000007";
const WH2 = "0195f0a0-0000-7000-8000-000000000008";
const LE = "0195f0a0-0000-7000-8000-0000000000aa";
const SH = "0195f0a0-0000-7000-8000-000000000001";
const LINE = "0195f0a0-0000-7000-8000-000000000002";
const ID = "0195f0a0-0000-7000-8000-0000000000bb";

const calls = (method: string, suffix: string) => server.calls.filter((c) => c.method === method && c.url.pathname.endsWith(suffix));
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

const SUPERVISOR = ["fulfillment.warehouse.view", "fulfillment.warehouse.release"];

const rules: OperationRule[] = [
  { warehouseId: WH, legalEntityId: LE, postingDateSource: "MANUAL", timeZone: "UTC", autoPackEnabled: true, workbenchGroupKeyDefault: "CUSTOMER", version: 0, updatedAt: null },
  { warehouseId: WH2, legalEntityId: LE, postingDateSource: "SHIP_TIMESTAMP", timeZone: "Europe/Berlin", autoPackEnabled: false, workbenchGroupKeyDefault: "ZONE", version: 3, updatedAt: "2026-11-01T08:00:00Z" },
];

describe("posting and packing rules page (BR-28.3, FR-28.15, FR-28.13)", () => {
  it("lists the rules, opens one, saves it with its version and reloads", async () => {
    let current = rules;
    server.handler = (url, init) => {
      if (init.method === "PUT") {
        current = [rules[0], { ...rules[1], postingDateSource: "TODAY", autoPackEnabled: true, version: 4 }];
        return json(200, current[1]);
      }
      if (url.pathname === "/api/v1/fulfillment/operation-rules") return json(200, { items: current });
      return undefined;
    };
    const { container } = renderAt("/fulfillment/operation-rules", SUPERVISOR);
    const table = await findTable({ name: "Posting and packing rules" });
    expect(within(table).getByText("Ship timestamp (warehouse time zone)")).toBeInTheDocument();
    expect(within(table).getByText("Defaults")).toBeInTheDocument();
    expect(within(table).getByText("Off")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: WH2 }));
    expect(screen.getByLabelText(/Warehouse time zone/)).toHaveValue("Europe/Berlin");
    fireEvent.change(screen.getByLabelText(/Posting date of a packing slip/), { target: { value: "TODAY" } });
    fireEvent.click(screen.getByLabelText("Allow auto-pack"));
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText(/Saved: today \(warehouse time zone\), changeable, auto-pack on, workbench groups by/)).toBeInTheDocument();
    const put = calls("PUT", WH2)[0];
    expect(header(put, "If-Match")).toBe('"3"');
    expect(idempotencyKey(put)).toBeTruthy();
    expect(put.body).toEqual({ legalEntityId: LE, postingDateSource: "TODAY", timeZone: "Europe/Berlin", autoPackEnabled: true, workbenchGroupKeyDefault: "ZONE" });
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Posting and packing rules" })).getByText("Today (warehouse time zone), changeable")).toBeInTheDocument());
  });

  it("validates before calling the service", () => {
    const good = { warehouseId: WH, legalEntityId: LE, postingDateSource: "SHIP_TIMESTAMP" as const, timeZone: " Pacific/Auckland ", autoPackEnabled: false, workbenchGroupKeyDefault: "PRODUCT" as const };
    expect(validateOperationRule(good).input?.body).toEqual({ legalEntityId: LE, postingDateSource: "SHIP_TIMESTAMP", timeZone: "Pacific/Auckland", autoPackEnabled: false, workbenchGroupKeyDefault: "PRODUCT" });
    expect(validateOperationRule({ ...good, timeZone: "" }).input?.body.timeZone).toBe("UTC");
    const bad = validateOperationRule({ ...good, warehouseId: "nope", legalEntityId: "", timeZone: "Mars/Olympus" });
    expect(bad.input).toBeUndefined();
    expect(Object.keys(bad.errors).sort()).toEqual(["legalEntityId", "timeZone", "warehouseId"]);
  });

  it("shows field errors and sends nothing, and explains a stale version and another legal entity", async () => {
    server.handler = (url, init) => {
      if (init.method === "PUT") {
        const stale = calls("PUT", "").length === 1;
        return json(stale ? 412 : 422, { type: "about:blank", title: "x", status: stale ? 412 : 422, code: stale ? "conflict_version" : "FUL-0057", detail: "refused" });
      }
      if (url.pathname === "/api/v1/fulfillment/operation-rules") return json(200, { items: rules });
      return undefined;
    };
    renderAt("/fulfillment/operation-rules", SUPERVISOR);
    const table = await findTable({ name: "Posting and packing rules" });
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText(/Enter the warehouse id/)).toBeInTheDocument();
    expect(calls("PUT", "")).toHaveLength(0);
    fireEvent.click(within(table).getByRole("button", { name: WH2 }));
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText(/Someone else changed this rule/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText(/belongs to another legal entity/)).toBeInTheDocument();
  });

  it("reads only for a viewer", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/fulfillment/operation-rules" ? json(200, { items: rules }) : undefined);
    renderAt("/fulfillment/operation-rules", ["fulfillment.warehouse.view"]);
    const table = await findTable({ name: "Posting and packing rules" });
    expect(within(table).getByText("Europe/Berlin")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save rule" })).not.toBeInTheDocument();
    expect(within(table).queryByRole("button", { name: WH2 })).not.toBeInTheDocument();
    expect(screen.getByText(/needs the fulfillment.warehouse.release duty/)).toBeInTheDocument();
  });

  it("shows the empty state and the error with retry", async () => {
    let fail = true;
    server.handler = () => (fail ? json(503, { code: "COM-0500", detail: "down" }) : json(200, { items: [] }));
    renderAt("/fulfillment/operation-rules");
    expect((await findAlert()).textContent).toContain("COM-0500");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No warehouses yet")).toBeInTheDocument();
  });
});

const packed: Shipment = {
  id: SH,
  shipmentNumber: "SH-0000042",
  legalEntityId: LE,
  warehouseId: WH2,
  orderId: ID,
  customerId: ID,
  status: "PACKED",
  plannedShipDate: "2026-10-26T00:00:00Z",
  actualShipDate: null,
  carrierCode: "UPS",
  version: 6,
  lines: [{ id: LINE, orderLineId: ID, productId: ID, unit: "EA", qtyPlanned: "10", qtyPicked: "10", qtyPacked: "10", qtyShipped: "0", pickConfirmed: true, zone: "A1" }],
  containers: [],
};

function serveShipment(rule: OperationRule | null) {
  server.handler = (url, init) => {
    if (init.method === "POST" && url.pathname.endsWith(":post-packing-slip")) return json(500, { code: "TEST", detail: "stop" });
    if (url.pathname === `/api/v1/fulfillment/operation-rules/${WH2}`) return rule ? json(200, rule) : json(404, { code: "COM-0404", detail: "none" });
    if (url.pathname === `/api/v1/fulfillment/shipments/${SH}`) return json(200, packed);
    if (url.pathname.endsWith("/tracking")) return json(200, { items: [] });
    if (url.pathname === "/api/v1/fulfillment/packing-slips") return json(200, { items: [] });
    return undefined;
  };
}

describe("post form follows the warehouse posting date source (BR-28.3)", () => {
  async function post() {
    fireEvent.change(await screen.findByLabelText("Unit cost"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Cost version"), { target: { value: "0195d6a0-0000-7000-8000-00000000c057" } });
    fireEvent.click(screen.getByRole("button", { name: "Post packing slip" }));
    await waitFor(() => expect(calls("POST", ":post-packing-slip")).toHaveLength(1));
    return calls("POST", ":post-packing-slip")[0].body as Record<string, unknown>;
  }

  it("asks for no date and sends none when the warehouse derives it", async () => {
    serveShipment(rules[1]);
    renderAt(`/fulfillment/shipments/${SH}`);
    expect(await screen.findByText(/The posting date is the day of the post in the warehouse time zone \(Europe\/Berlin\)/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Posting date")).not.toBeInTheDocument();
    expect(await post()).not.toHaveProperty("postingDate");
  });

  it("offers an optional date for a TODAY warehouse: empty sends none, a named one is sent", async () => {
    serveShipment({ ...rules[1], postingDateSource: "TODAY", timeZone: "Pacific/Auckland" });
    const first = renderAt(`/fulfillment/shipments/${SH}`);
    expect(await screen.findByText(/Empty: today in the warehouse time zone \(Pacific\/Auckland\)/)).toBeInTheDocument();
    expect(await post()).not.toHaveProperty("postingDate");
    first.unmount();
    server.calls = [];

    serveShipment({ ...rules[1], postingDateSource: "TODAY", timeZone: "Pacific/Auckland" });
    renderAt(`/fulfillment/shipments/${SH}`);
    await screen.findByText(/Empty: today in the warehouse time zone/);
    fireEvent.change(screen.getByLabelText(/Posting date/), { target: { value: "2027-02-27" } });
    expect(await post()).toMatchObject({ postingDate: "2027-02-27" });
  });

  it("keeps the date field for a MANUAL warehouse and when the rule cannot be read", async () => {
    serveShipment({ ...rules[1], postingDateSource: "MANUAL" });
    const first = renderAt(`/fulfillment/shipments/${SH}`);
    expect(await screen.findByLabelText("Posting date")).toBeInTheDocument();
    expect(await post()).toHaveProperty("postingDate");
    first.unmount();
    server.calls = [];

    serveShipment(null);
    renderAt(`/fulfillment/shipments/${SH}`);
    expect(await screen.findByLabelText("Posting date")).toBeInTheDocument();
    expect(await post()).toHaveProperty("postingDate");
  });
});

describe("auto-pack follows the warehouse switch (FR-28.15)", () => {
  const pickedShipment: Shipment = { ...packed, status: "PICKED", lines: [{ ...packed.lines[0], qtyPacked: "0" }] };

  it("disables auto-pack with an explanation when the warehouse turned it off", async () => {
    server.handler = (url) => {
      if (url.pathname === `/api/v1/fulfillment/operation-rules/${WH2}`) return json(200, rules[1]);
      if (url.pathname === `/api/v1/fulfillment/shipments/${SH}`) return json(200, pickedShipment);
      if (url.pathname.endsWith("/tracking")) return json(200, { items: [] });
      if (url.pathname === "/api/v1/fulfillment/packing-slips") return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/fulfillment/shipments/${SH}`);
    expect(await screen.findByText(/Auto-pack is turned off for this warehouse/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Auto-pack" })).toBeDisabled();
  });
});

describe("picking workbench group key (FR-28.13)", () => {
  it("sends no group when the form leaves it at the warehouse default, and the chosen one otherwise", () => {
    const blank = { warehouseId: WH, customerId: "", carrier: "", zone: "", shipFrom: "", shipTo: "", groupBy: "" as const, maxLines: "", maxOrders: "" };
    expect(validateWorkbench(blank).input).toEqual({ warehouseId: WH });
    expect(validateWorkbench({ ...blank, groupBy: "ZONE" }).input).toEqual({ warehouseId: WH, groupBy: "ZONE" });
  });
});
