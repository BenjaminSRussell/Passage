import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { DeliveryTolerance, Shipment } from "./api/client";
import { validateTolerance } from "./DeliveryTolerancesPage";
import { pickCeiling, resolveTolerance } from "./format";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const SH = "0195f0a0-0000-7000-8000-000000000001";
const LINE = "0195f0a0-0000-7000-8000-000000000002";
const PRODUCT = "0195f0a0-0000-7000-8000-0000000000a1";
const PRODUCT2 = "0195f0a0-0000-7000-8000-0000000000a2";
const CUSTOMER = "0195f0a0-0000-7000-8000-0000000000c1";
const CUSTOMER2 = "0195f0a0-0000-7000-8000-0000000000c2";
const ID = "0195f0a0-0000-7000-8000-0000000000aa";

const calls = (method: string, suffix: string) => server.calls.filter((c) => c.method === method && c.url.pathname.endsWith(suffix));
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

const tol = (id: string, productId: string | null, customerId: string | null, pct: string, version = 1): DeliveryTolerance => ({
  id,
  productId,
  customerId,
  overdeliveryPct: pct,
  version,
  updatedAt: "2026-11-01T08:00:00Z",
});

const MAINTAINER = ["WarehouseView", "PackingRuleMaintain"];

describe("tolerance helpers (BR-28.7)", () => {
  it("picks the most specific scope and computes the ceiling exactly", () => {
    const list = [tol("a", null, null, "1"), tol("b", null, CUSTOMER, "2"), tol("c", PRODUCT, null, "3"), tol("d", PRODUCT, CUSTOMER, "4")];
    expect(resolveTolerance(list, PRODUCT, CUSTOMER)).toBe("4");
    expect(resolveTolerance(list, PRODUCT, CUSTOMER2)).toBe("3");
    expect(resolveTolerance(list, PRODUCT2, CUSTOMER)).toBe("2");
    expect(resolveTolerance(list, PRODUCT2, CUSTOMER2)).toBe("1");
    expect(resolveTolerance([], PRODUCT, CUSTOMER)).toBe("0");
    expect(pickCeiling("100", "5")).toBe("105");
    expect(pickCeiling("3", "0.3333")).toBe("3.009999");
    expect(pickCeiling("10", "0")).toBe("10");
    expect(pickCeiling("0.5", "100")).toBe("1");
  });

  it("validates the form like the service", () => {
    expect(validateTolerance({ productId: "", customerId: "", overdeliveryPct: "0" })).toEqual({});
    expect(validateTolerance({ productId: PRODUCT, customerId: CUSTOMER, overdeliveryPct: "100" })).toEqual({});
    expect(Object.keys(validateTolerance({ productId: "x", customerId: "y", overdeliveryPct: "100.5" })).sort()).toEqual(["customerId", "overdeliveryPct", "productId"]);
    expect(validateTolerance({ productId: "", customerId: "", overdeliveryPct: "1.00001" }).overdeliveryPct).toBeTruthy();
    expect(validateTolerance({ productId: "", customerId: "", overdeliveryPct: "" }).overdeliveryPct).toBeTruthy();
  });
});

describe("over-delivery tolerances page", () => {
  it("lists the scopes, opens one, saves it with its version and reloads", async () => {
    let current = [tol("t1", PRODUCT, CUSTOMER, "5", 3), tol("t2", null, null, "1", 1)];
    server.handler = (url, init) => {
      if (init.method === "PUT") {
        current = [tol("t1", PRODUCT, CUSTOMER, "7.5", 4), current[1]];
        return json(200, current[0]);
      }
      if (url.pathname === "/api/v1/fulfillment/delivery-tolerances") return json(200, { items: current });
      return undefined;
    };
    const { container } = renderAt("/fulfillment/delivery-tolerances", MAINTAINER);
    const table = await findTable({ name: "Over-delivery tolerances" });
    expect(within(table).getByText("All products")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: PRODUCT.slice(0, 8) }));
    expect(screen.getByLabelText(/Over-delivery \(%\)/)).toHaveValue("5");
    fireEvent.change(screen.getByLabelText(/Over-delivery \(%\)/), { target: { value: "7.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tolerance" }));
    expect(await screen.findByText(/Saved: a pick may exceed the plan by up to 7.5%/)).toBeInTheDocument();
    const put = calls("PUT", "/delivery-tolerances")[0];
    expect(header(put, "If-Match")).toBe('"3"');
    expect(idempotencyKey(put)).toBeTruthy();
    expect(put.body).toEqual({ productId: PRODUCT, customerId: CUSTOMER, overdeliveryPct: "7.5" });
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Over-delivery tolerances" })).getByText("7.5")).toBeInTheDocument());
  });

  it("takes the saved version of a typed scope, 0 for a new one, and shows field errors without calling", async () => {
    server.handler = (url, init) => {
      if (init.method === "PUT") return json(200, tol("t9", null, null, "2", 2));
      if (url.pathname === "/api/v1/fulfillment/delivery-tolerances") return json(200, { items: [tol("t2", null, null, "1", 1)] });
      return undefined;
    };
    renderAt("/fulfillment/delivery-tolerances", MAINTAINER);
    await findTable({ name: "Over-delivery tolerances" });
    fireEvent.click(screen.getByRole("button", { name: "Save tolerance" }));
    expect(await screen.findByText(/Enter a percentage from 0 to 100/)).toBeInTheDocument();
    expect(calls("PUT", "/delivery-tolerances")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText(/Over-delivery \(%\)/), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tolerance" }));
    await screen.findByText(/Saved:/);
    expect(header(calls("PUT", "/delivery-tolerances")[0], "If-Match")).toBe('"1"');
    fireEvent.change(screen.getByLabelText(/Product id/), { target: { value: PRODUCT } });
    fireEvent.click(screen.getByRole("button", { name: "Save tolerance" }));
    await waitFor(() => expect(calls("PUT", "/delivery-tolerances")).toHaveLength(2));
    expect(header(calls("PUT", "/delivery-tolerances")[1], "If-Match")).toBe('"0"');
  });

  it("explains a stale version and reads only for a viewer", async () => {
    server.handler = (url, init) => {
      if (init.method === "PUT") return json(412, { type: "about:blank", title: "x", status: 412, code: "conflict_version", detail: "stale" });
      if (url.pathname === "/api/v1/fulfillment/delivery-tolerances") return json(200, { items: [tol("t2", null, null, "1", 1)] });
      return undefined;
    };
    const { unmount } = renderAt("/fulfillment/delivery-tolerances", MAINTAINER);
    await findTable({ name: "Over-delivery tolerances" });
    fireEvent.change(screen.getByLabelText(/Over-delivery \(%\)/), { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tolerance" }));
    expect(await screen.findByText(/Someone else changed this tolerance/)).toBeInTheDocument();
    unmount();

    renderAt("/fulfillment/delivery-tolerances", ["WarehouseView"]);
    const table = await findTable({ name: "Over-delivery tolerances" });
    expect(within(table).getByText("All customers")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save tolerance" })).not.toBeInTheDocument();
    expect(screen.getByText(/needs the PackingRuleMaintain duty/)).toBeInTheDocument();
  });

  it("shows the empty state and the error with retry", async () => {
    let fail = true;
    server.handler = () => (fail ? json(503, { code: "COM-0500", detail: "down" }) : json(200, { items: [] }));
    renderAt("/fulfillment/delivery-tolerances");
    expect((await findAlert()).textContent).toContain("COM-0500");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No tolerances yet")).toBeInTheDocument();
  });
});

describe("confirm pick above plan (BR-28.7)", () => {
  function shipment(status: Shipment["status"], line: Partial<Shipment["lines"][number]> = {}, version = 3): Shipment {
    return {
      id: SH,
      shipmentNumber: "SH-0000042",
      legalEntityId: ID,
      warehouseId: ID,
      orderId: ID,
      customerId: CUSTOMER,
      status,
      plannedShipDate: "2026-10-26T00:00:00Z",
      actualShipDate: null,
      carrierCode: "UPS",
      version,
      lines: [{ id: LINE, orderLineId: ID, productId: PRODUCT, unit: "EA", qtyPlanned: "100", qtyPicked: "0", qtyPacked: "0", qtyShipped: "0", pickConfirmed: false, zone: null, ...line }],
      containers: [],
    };
  }

  function serve(tolerances: DeliveryTolerance[]) {
    let current = shipment("RELEASED");
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":confirm-pick")) {
        current = shipment("PICKED", { qtyPicked: "105", pickConfirmed: true, overTolerancePct: "5" }, 4);
        return json(200, current);
      }
      if (url.pathname === `/api/v1/fulfillment/shipments/${SH}`) return json(200, current);
      if (url.pathname === `/api/v1/fulfillment/shipments/${SH}/tracking`) return json(200, { items: [] });
      if (url.pathname === "/api/v1/fulfillment/packing-slips") return json(200, { items: [] });
      if (url.pathname === "/api/v1/fulfillment/delivery-tolerances") return json(200, { items: tolerances });
      return undefined;
    };
  }

  it("refuses above the ceiling, accepts within it and shows the excess on the line", async () => {
    serve([tol("t1", null, null, "1"), tol("t2", PRODUCT, CUSTOMER, "5")]);
    renderAt(`/fulfillment/shipments/${SH}`);
    const field = await screen.findByLabelText(/picked$/);
    fireEvent.change(field, { target: { value: "105.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm pick" }));
    expect(await screen.findByText(/Over-ship: at most 105 is allowed, 100 planned plus a 5% tolerance \(FUL-0005\)/)).toBeInTheDocument();
    expect(calls("POST", ":confirm-pick")).toHaveLength(0);

    fireEvent.change(field, { target: { value: "105" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm pick" }));
    await screen.findByText("105 (+5 over plan)");
    expect(calls("POST", ":confirm-pick")[0].body).toEqual({ picks: [{ shipmentLineId: LINE, picked: "105" }] });
  });

  it("keeps the no-tolerance message and reads the tolerances only for a quantity above plan", async () => {
    serve([]);
    renderAt(`/fulfillment/shipments/${SH}`);
    const field = await screen.findByLabelText(/picked$/);
    fireEvent.change(field, { target: { value: "90" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm pick" }));
    await waitFor(() => expect(calls("POST", ":confirm-pick")).toHaveLength(1));
    expect(server.calls.filter((c) => c.url.pathname.endsWith("/delivery-tolerances"))).toHaveLength(0);
  });

  it("leaves the decision to the service when the tolerances cannot be read, and shows its refusal", async () => {
    serve([]);
    const base = server.handler;
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/fulfillment/delivery-tolerances") return json(403, { code: "AUTH-0003", detail: "missing duty" });
      if (init.method === "POST" && url.pathname.endsWith(":confirm-pick"))
        return json(422, { code: "FUL-0066", detail: "FUL-0066: sales cancelled the remainder of the order line; it keeps 100" });
      return base(url, init);
    };
    renderAt(`/fulfillment/shipments/${SH}`);
    const field = await screen.findByLabelText(/picked$/);
    fireEvent.change(field, { target: { value: "103" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm pick" }));
    await waitFor(() => expect(calls("POST", ":confirm-pick")).toHaveLength(1));
    expect(await screen.findByText(/it keeps 100/)).toBeInTheDocument();
    expect(screen.queryByText(/FUL-0004\)/)).not.toBeInTheDocument();
  });

  it("says no tolerance applies when none is saved", async () => {
    serve([]);
    renderAt(`/fulfillment/shipments/${SH}`);
    const field = await screen.findByLabelText(/picked$/);
    fireEvent.change(field, { target: { value: "101" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm pick" }));
    expect(await screen.findByText(/Over-pick: at most 100 is planned and no over-delivery tolerance applies \(FUL-0004\)/)).toBeInTheDocument();
    expect(calls("POST", ":confirm-pick")).toHaveLength(0);
  });
});
