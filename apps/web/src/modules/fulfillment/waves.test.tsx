import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Shipment, TrackingEvent, Wave, WavePlan, WaveSummary } from "./api/client";
import { emptyLine, validateRelease } from "./ReleasePage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";
import { validateWorkbench, type WorkbenchValues } from "./WorkbenchPage";

useFakeServer();

const WH = "0195f0a0-0000-7000-8000-000000000007";
const SH = "0195f0a0-0000-7000-8000-000000000001";
const SH2 = "0195f0a0-0000-7000-8000-000000000011";
const LINE = "0195f0a0-0000-7000-8000-000000000002";
const WAVE = "0195f0a0-0000-7000-8000-0000000000e1";
const ID = "0195f0a0-0000-7000-8000-0000000000aa";
const CONTAINER = "0195f0a0-0000-7000-8000-0000000000c1";

const posts = (suffix: string) => server.calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

const waveShipment = (id: string, number: string, lines: number) => ({
  id,
  shipmentNumber: number,
  orderId: ID,
  customerId: ID,
  status: "RELEASED" as const,
  carrierCode: "UPS",
  plannedShipDate: "2026-10-27T00:00:00Z",
  lineCount: lines,
});

const plans: WavePlan[] = [
  { groupKey: "UPS", legalEntityId: ID, lineCount: 20, orderCount: 8, shipments: [waveShipment(SH, "SH-0000001", 3), waveShipment(SH2, "SH-0000002", 2)] },
  { groupKey: "UNASSIGNED", legalEntityId: ID, lineCount: 4, orderCount: 1, shipments: [waveShipment(ID, "SH-0000003", 4)] },
];

const summary: WaveSummary = {
  id: WAVE,
  waveNumber: "WV-0000001",
  legalEntityId: ID,
  warehouseId: WH,
  groupBy: "CARRIER",
  groupKey: "UPS",
  status: "OPEN",
  lineCount: 20,
  orderCount: 8,
  createdAt: "2026-10-27T08:00:00Z",
};

const wave: Wave = { ...summary, shipments: plans[0].shipments.map((s) => ({ ...s, status: "PICKING" })) };

function blank(): WorkbenchValues {
  return { warehouseId: WH, customerId: "", carrier: "", zone: "", shipFrom: "", shipTo: "", groupBy: "CUSTOMER", maxLines: "", maxOrders: "" };
}

describe("picking workbench (FR-28.13, AC-28.10)", () => {
  it("validates like the service before sending", () => {
    const { errors, input } = validateWorkbench({ ...blank(), warehouseId: "w1", carrier: "U P S", shipFrom: "2026-10-31", shipTo: "2026-10-01", maxLines: "5000", maxOrders: "0" });
    expect(input).toBeUndefined();
    expect(errors).toMatchObject({ warehouseId: expect.stringMatching(/UUID/), carrier: expect.any(String), shipTo: expect.stringMatching(/before/), maxLines: expect.stringMatching(/2000/), maxOrders: expect.stringMatching(/500/) });
    expect(validateWorkbench({ ...blank(), carrier: " UPS ", maxLines: "20" }).input).toEqual({ warehouseId: WH, carrier: "UPS", groupBy: "CUSTOMER", maxLines: 20 });
  });

  it("previews without a key, then creates every wave with one Idempotency-Key", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/fulfillment/waves:preview") return json(200, { warehouseId: WH, groupBy: "CARRIER", waveCount: 2, lineCount: 24, message: "Preview: 2 waves from 24 lines", waves: plans });
      if (url.pathname === "/api/v1/fulfillment/waves" && init.method === "POST") {
        return json(201, { warehouseId: WH, groupBy: "CARRIER", waveCount: 1, lineCount: 20, message: "FUL-0016: Workbench created 1 wave from 20 lines", waves: [wave] });
      }
      return undefined;
    };
    const { container } = renderAt("/fulfillment/workbench");
    fireEvent.change(screen.getByLabelText("Warehouse id"), { target: { value: WH } });
    fireEvent.change(screen.getByLabelText("Carrier"), { target: { value: "UPS" } });
    fireEvent.change(screen.getByLabelText("Group by"), { target: { value: "CARRIER" } });
    fireEvent.change(screen.getByLabelText("Max lines per wave"), { target: { value: "20" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview waves" }));

    expect((await findStatus()).textContent).toBe("2 waves from 24 lines, grouped by carrier.");
    const table = screen.getByRole("table", { name: "Planned waves" });
    expect(within(table).getByText("SH-0000001, SH-0000002")).toBeInTheDocument();
    expect(within(table).getByText("Unassigned")).toBeInTheDocument();
    const preview = posts("/waves:preview")[0];
    expect(idempotencyKey(preview)).toBeNull();
    expect(preview.body).toEqual({ warehouseId: WH, carrier: "UPS", groupBy: "CARRIER", maxLines: 20 });
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Create 2 waves" }));
    expect(await screen.findByText("Workbench created 1 wave from 20 lines")).toBeInTheDocument();
    const create = posts("/waves")[0];
    expect(idempotencyKey(create)).toBeTruthy();
    expect(create.body).toEqual(preview.body);
    expect(within(screen.getByRole("table", { name: "Created waves" })).getByRole("link", { name: "WV-0000001" })).toHaveAttribute("href", `/fulfillment/waves/${WAVE}`);
    expect(screen.queryByRole("button", { name: /^Create/ })).toBeNull();
  });

  it("shows the empty preview without a create button", async () => {
    server.handler = () => json(200, { warehouseId: WH, groupBy: "CUSTOMER", waveCount: 0, lineCount: 0, message: "Preview: 0 waves from 0 lines", waves: [] });
    renderAt("/fulfillment/workbench");
    fireEvent.change(screen.getByLabelText("Warehouse id"), { target: { value: WH } });
    fireEvent.click(screen.getByRole("button", { name: "Preview waves" }));
    expect(await screen.findByText("No released shipments match")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Create/ })).toBeNull();
  });

  it("asks for a new preview when the grouping went stale (FUL-0053)", async () => {
    server.handler = (url) =>
      url.pathname.endsWith(":preview")
        ? json(200, { warehouseId: WH, groupBy: "CUSTOMER", waveCount: 1, lineCount: 4, message: "", waves: [plans[1]] })
        : json(409, { code: "FUL-0053", detail: "a shipment left RELEASED while the waves were planned; preview again" });
    renderAt("/fulfillment/workbench");
    fireEvent.change(screen.getByLabelText("Warehouse id"), { target: { value: WH } });
    fireEvent.click(screen.getByRole("button", { name: "Preview waves" }));
    fireEvent.click(await screen.findByRole("button", { name: "Create 1 wave" }));
    const alert = await findAlert();
    expect(alert.textContent).toContain("FUL-0053");
    expect(alert.textContent).toContain("Preview again");
    expect(screen.queryByRole("table", { name: "Planned waves" })).toBeNull();
  });

  it("does not call the service when the filter is invalid", () => {
    renderAt("/fulfillment/workbench");
    fireEvent.click(screen.getByRole("button", { name: "Preview waves" }));
    expect(screen.getByText("Enter the warehouse id (a UUID).")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });
});

describe("pick waves", () => {
  it("lists waves and opens one with its shipments", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/fulfillment/waves") return json(200, { items: [summary] });
      if (url.pathname === `/api/v1/fulfillment/waves/${WAVE}`) return json(200, wave);
      return undefined;
    };
    const { container } = renderAt("/fulfillment/waves");
    const table = await findTable({ name: "Pick waves" });
    expect((await within(table).findByText("WV-0000001")).closest("tr")?.textContent).toContain("Carrier");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "WV-0000001" }));
    expect(await screen.findByRole("heading", { name: "Wave WV-0000001" })).toBeInTheDocument();
    const shipments = screen.getByRole("table", { name: "Wave shipments" });
    expect(within(shipments).getByText("SH-0000002").closest("tr")?.textContent).toContain("Picking");
  });

  it("shows the empty state and the error with retry", async () => {
    let fail = true;
    server.handler = () => (fail ? json(503, { code: "COM-0500", detail: "down" }) : json(200, { items: [] }));
    renderAt("/fulfillment/waves");
    expect((await findAlert()).textContent).toContain("COM-0500");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No waves yet")).toBeInTheDocument();
  });

  it("reports an unknown wave", async () => {
    server.handler = () => json(404, { code: "FUL-0052", detail: "wave not found" });
    renderAt(`/fulfillment/waves/${WAVE}`);
    expect((await findAlert()).textContent).toContain("FUL-0052");
  });
});

function shipment(status: Shipment["status"], line: Partial<Shipment["lines"][number]> = {}, version = 3): Shipment {
  return {
    id: SH,
    shipmentNumber: "SH-0000042",
    legalEntityId: ID,
    warehouseId: WH,
    orderId: ID,
    customerId: ID,
    status,
    plannedShipDate: "2026-10-26T00:00:00Z",
    actualShipDate: null,
    carrierCode: "UPS",
    version,
    lines: [{ id: LINE, orderLineId: ID, productId: ID, unit: "EA", qtyPlanned: "100", qtyPicked: "0", qtyPacked: "0", qtyShipped: "0", pickConfirmed: false, zone: "A1", ...line }],
    containers: [],
  };
}

function serveShipment(current: () => Shipment, tracking: () => TrackingEvent[] = () => []) {
  server.handler = (url, init) => {
    if (init.method === "POST") return undefined;
    if (url.pathname === `/api/v1/fulfillment/shipments/${SH}`) return json(200, current());
    if (url.pathname === `/api/v1/fulfillment/shipments/${SH}/tracking`) return json(200, { items: tracking() });
    if (url.pathname === "/api/v1/fulfillment/packing-slips") return json(200, { items: [] });
    return undefined;
  };
}

describe("shipment detail, slice 3", () => {
  it("re-picks the remainder of a partially shipped shipment (FR-28.8, AC-28.4)", async () => {
    let current = shipment("PARTIALLY_SHIPPED", { qtyPicked: "90", qtyPacked: "90", qtyShipped: "90", pickConfirmed: true }, 6);
    serveShipment(() => current);
    const base = server.handler;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":confirm-pick")) {
        current = shipment("PICKED", { qtyPicked: "100", qtyPacked: "90", qtyShipped: "90", pickConfirmed: true }, 7);
        return json(200, current);
      }
      return base(url, init);
    };
    const { container } = renderAt(`/fulfillment/shipments/${SH}`);
    expect(await screen.findByRole("heading", { name: "Pick the remainder" })).toBeInTheDocument();
    expect(screen.getByText(/A packing slip has shipped part of this shipment/)).toBeInTheDocument();
    // "To ship" is planned minus shipped.
    expect(within(screen.getByRole("table", { name: "Shipment lines" })).getByText("10")).toBeInTheDocument();
    const field = screen.getByLabelText(/picked$/);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(field, { target: { value: "80" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm pick" }));
    expect(await screen.findByText(/90 is already packed or shipped/)).toBeInTheDocument();
    expect(posts(":confirm-pick")).toHaveLength(0);

    fireEvent.change(field, { target: { value: "100" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm pick" }));
    await screen.findByRole("button", { name: "Close container" });
    const call = posts(":confirm-pick")[0];
    expect(header(call, "If-Match")).toBe('"6"');
    expect(call.body).toEqual({ picks: [{ shipmentLineId: LINE, picked: "100" }] });
  });

  it("cancels a released shipment after confirmation (BR-28.6)", async () => {
    let current = shipment("RELEASED");
    serveShipment(() => current);
    const base = server.handler;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":cancel")) {
        current = shipment("CANCELLED", {}, 4);
        return json(200, current);
      }
      return base(url, init);
    };
    renderAt(`/fulfillment/shipments/${SH}`);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel shipment" }));
    expect(posts(":cancel")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Yes, cancel it" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Cancel shipment" })).toBeNull());
    const call = posts(":cancel")[0];
    expect(header(call, "If-Match")).toBe('"3"');
    expect(idempotencyKey(call)).toBeTruthy();
    expect(await screen.findByText("Cancelled")).toBeInTheDocument();
  });

  it("closes the remainder of a partially shipped shipment after confirmation (FR-28.8)", async () => {
    let current = shipment("PARTIALLY_SHIPPED", { qtyPicked: "90", qtyPacked: "90", qtyShipped: "90", pickConfirmed: true }, 6);
    serveShipment(() => current);
    const base = server.handler;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":close-remainder")) {
        current = shipment("SHIPPED", { qtyPlanned: "90", qtyPicked: "90", qtyPacked: "90", qtyShipped: "90", qtyClosed: "10", pickConfirmed: true }, 7);
        return json(200, current);
      }
      return base(url, init);
    };
    renderAt(`/fulfillment/shipments/${SH}`);
    fireEvent.click(await screen.findByRole("button", { name: "Close remainder" }));
    expect(posts(":close-remainder")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Yes, close it" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Close remainder" })).toBeNull());
    const call = posts(":close-remainder")[0];
    expect(header(call, "If-Match")).toBe('"6"');
    expect(idempotencyKey(call)).toBeTruthy();
    expect((await screen.findAllByText("Shipped")).length).toBeGreaterThan(0);
    expect(screen.queryByText("Partially shipped")).toBeNull();
    const lines = await findTable({ name: "Shipment lines" });
    expect(within(lines).getByText("10")).toBeInTheDocument();
  });

  it("shows the service refusal when the remainder cannot close", async () => {
    serveShipment(() => shipment("PARTIALLY_SHIPPED", { qtyPicked: "90", qtyPacked: "90", qtyShipped: "90", pickConfirmed: true }, 6));
    const base = server.handler;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":close-remainder")) {
        return json(409, { type: "about:blank", title: "Conflict", status: 409, code: "FUL-0021", detail: "FUL-0021: packed quantity is waiting to ship, post it first" });
      }
      return base(url, init);
    };
    renderAt(`/fulfillment/shipments/${SH}`);
    fireEvent.click(await screen.findByRole("button", { name: "Close remainder" }));
    fireEvent.click(screen.getByRole("button", { name: "Yes, close it" }));
    expect(await screen.findByText("Could not close the remainder")).toBeInTheDocument();
  });

  it("offers no close for a shipment that shipped nothing or has packed stock waiting", async () => {
    serveShipment(() => shipment("PICKED", { qtyPicked: "100", pickConfirmed: true }));
    renderAt(`/fulfillment/shipments/${SH}`);
    await findTable({ name: "Shipment lines" });
    expect(screen.queryByRole("button", { name: "Close remainder" })).toBeNull();
  });

  it("unpacks a closed container after confirmation, then the shipment can be cancelled", async () => {
    const container: Shipment["containers"][number] = { id: CONTAINER, containerType: "PALLET", sscc: "106141411234567897", status: "CLOSED", tareKg: "25", grossKg: "25", returnable: true, packagingMaterial: null, packingSlipId: null, lines: [{ shipmentLineId: LINE, qty: "100" }] };
    let current = shipment("PACKED", { qtyPicked: "100", qtyPacked: "100", pickConfirmed: true });
    current.containers = [container];
    serveShipment(() => current);
    const base = server.handler;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/containers/${CONTAINER}:unpack`)) {
        current = shipment("PICKED", { qtyPicked: "100", qtyPacked: "0", pickConfirmed: true }, 4);
        return json(200, current);
      }
      return base(url, init);
    };
    renderAt(`/fulfillment/shipments/${SH}`);
    expect(screen.queryByRole("button", { name: "Cancel shipment" })).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "Unpack 106141411234567897" }));
    expect(posts(":unpack")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Confirm unpack 106141411234567897" }));
    expect(await screen.findByRole("button", { name: "Cancel shipment" })).toBeInTheDocument();
    const call = posts(":unpack")[0];
    expect(header(call, "If-Match")).toBe('"3"');
    expect(idempotencyKey(call)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Unpack 106141411234567897" })).toBeNull();
  });

  it("shows the service refusal when a container cannot be unpacked", async () => {
    const packed = shipment("PACKED", { qtyPicked: "100", qtyPacked: "100", pickConfirmed: true });
    packed.containers = [{ id: CONTAINER, containerType: "PALLET", sscc: "106141411234567897", status: "CLOSED", tareKg: "25", grossKg: "25", returnable: true, packagingMaterial: null, packingSlipId: null, lines: [{ shipmentLineId: LINE, qty: "100" }] }];
    serveShipment(() => packed);
    const base = server.handler;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":unpack")) {
        return json(409, { type: "about:blank", title: "Conflict", status: 409, code: "FUL-0021", detail: "FUL-0021: only a CLOSED container can be unpacked" });
      }
      return base(url, init);
    };
    renderAt(`/fulfillment/shipments/${SH}`);
    fireEvent.click(await screen.findByRole("button", { name: "Unpack 106141411234567897" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm unpack 106141411234567897" }));
    expect(await screen.findByText("Could not unpack the container")).toBeInTheDocument();
  });

  it("offers no unpack for a shipped container", async () => {
    const done = shipment("PARTIALLY_SHIPPED", { qtyPicked: "50", qtyPacked: "50", qtyShipped: "50", pickConfirmed: true });
    done.containers = [{ id: CONTAINER, containerType: "PALLET", sscc: "106141411234567897", status: "SHIPPED", tareKg: "25", grossKg: "25", returnable: true, packagingMaterial: null, packingSlipId: ID, lines: [{ shipmentLineId: LINE, qty: "50" }] }];
    serveShipment(() => done);
    renderAt(`/fulfillment/shipments/${SH}`);
    await findTable({ name: "Containers" });
    expect(screen.queryByRole("button", { name: /^Unpack/ })).toBeNull();
  });

  it("offers no cancel once a container is packed", async () => {
    const packed = shipment("PACKED", { qtyPicked: "100", qtyPacked: "100", pickConfirmed: true });
    packed.containers = [{ id: CONTAINER, containerType: "PALLET", sscc: "106141411234567897", status: "CLOSED", tareKg: "25", grossKg: "25", returnable: true, packagingMaterial: null, packingSlipId: null, lines: [{ shipmentLineId: LINE, qty: "100" }] }];
    serveShipment(() => packed);
    renderAt(`/fulfillment/shipments/${SH}`);
    await findTable({ name: "Containers" });
    expect(screen.queryByRole("button", { name: "Cancel shipment" })).toBeNull();
  });

  it("shows tracking and records a manual event (FR-28.10)", async () => {
    let events: TrackingEvent[] = [];
    serveShipment(() => shipment("SHIPPED", { qtyPicked: "100", qtyPacked: "100", qtyShipped: "100", pickConfirmed: true }, 8), () => events);
    const base = server.handler;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/tracking")) {
        const body = JSON.parse(String(init.body)) as TrackingEvent;
        events = [{ ...body }];
        return json(201, { ...body, created: true });
      }
      return base(url, init);
    };
    renderAt(`/fulfillment/shipments/${SH}`);
    expect(await screen.findByText("No tracking yet")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add tracking event" }));
    expect(await screen.findByText(/Enter the tracking number/)).toBeInTheDocument();
    expect(posts("/tracking")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Tracking number"), { target: { value: "1Z999AA1" } });
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "DELIVERED" } });
    fireEvent.change(screen.getByLabelText("Event time"), { target: { value: "2026-10-27T09:30" } });
    fireEvent.click(screen.getByRole("button", { name: "Add tracking event" }));
    expect(await screen.findByText("Tracking event recorded.")).toBeInTheDocument();
    const call = posts("/tracking")[0];
    expect(call.body).toMatchObject({ provider: "manual", trackingNumber: "1Z999AA1", status: "DELIVERED" });
    expect((call.body as TrackingEvent).eventKey).toMatch(/^manual-/);
    expect(new Date((call.body as TrackingEvent).eventTime).toISOString()).toBe((call.body as TrackingEvent).eventTime);
    expect(await within(screen.getByRole("table", { name: "Tracking events" })).findByText("1Z999AA1")).toBeInTheDocument();
  });

  it("shows the ZPL label of a container (FR-28.3)", async () => {
    const packed = shipment("PACKED", { qtyPicked: "100", qtyPacked: "100", pickConfirmed: true });
    packed.containers = [{ id: CONTAINER, containerType: "PALLET", sscc: "106141411234567897", status: "CLOSED", tareKg: "25", grossKg: "25", returnable: true, packagingMaterial: null, packingSlipId: null, lines: [{ shipmentLineId: LINE, qty: "100" }] }];
    serveShipment(() => packed);
    const base = server.handler;
    server.handler = (url, init) => (url.pathname.endsWith("/label") ? json(200, "^XA^FD>;>800106141411234567897^FS^XZ") : base(url, init));
    renderAt(`/fulfillment/shipments/${SH}`);
    fireEvent.click(await screen.findByRole("button", { name: "ZPL label 106141411234567897" }));
    const program = await screen.findByLabelText("ZPL program 106141411234567897");
    expect(program.textContent).toContain(">;>800106141411234567897");
    fireEvent.click(screen.getByRole("button", { name: "Close label" }));
    expect(screen.queryByLabelText("ZPL program 106141411234567897")).toBeNull();
  });

  it("sends the ASN flag with the packing slip post (FR-28.9)", async () => {
    serveShipment(() => shipment("PACKED", { qtyPlanned: "10", qtyPicked: "10", qtyPacked: "10", pickConfirmed: true }, 5));
    const base = server.handler;
    server.handler = (url, init) => (init.method === "POST" ? json(422, { code: "FUL-0042", detail: "stop here" }) : base(url, init));
    renderAt(`/fulfillment/shipments/${SH}`);
    fireEvent.change(await screen.findByLabelText("Unit cost"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Cost snapshot id"), { target: { value: "s" } });
    fireEvent.click(screen.getByLabelText("Send an advance ship notice (ASN)"));
    fireEvent.click(screen.getByRole("button", { name: "Post packing slip" }));
    await screen.findByText(/stop here/);
    expect(posts(":post-packing-slip")[0].body).toMatchObject({ asnEnabled: true });
  });
});

describe("release with carrier and zone", () => {
  it("checks the codes and sends them only when given", () => {
    const line = { ...emptyLine(), orderId: ID, orderLineId: LINE, customerId: ID, legalEntityId: ID, productId: ID, qty: "1" };
    const bad = validateRelease({ warehouseId: WH, horizonDays: "", requireFullAtp: true, lines: [{ ...line, carrierCode: "U P S", zone: "a/1" }] });
    expect(bad.errors["lines[0].carrierCode"]).toBeTruthy();
    expect(bad.errors["lines[0].zone"]).toBeTruthy();
    const good = validateRelease({ warehouseId: WH, horizonDays: "", requireFullAtp: true, lines: [{ ...line, carrierCode: "UPS", zone: "" }] });
    expect(good.input?.lines[0]).toMatchObject({ carrierCode: "UPS" });
    expect(good.input?.lines[0]).not.toHaveProperty("zone");
  });
});
