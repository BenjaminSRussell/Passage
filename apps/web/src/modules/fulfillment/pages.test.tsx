import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { PackingSlip, PackingSlipSummary, Shipment, ShipmentSummary } from "./api/client";
import { validateRelease, emptyLine } from "./ReleasePage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const SH = "0195f0a0-0000-7000-8000-000000000001";
const LINE = "0195f0a0-0000-7000-8000-000000000002";
const ORDER = "0195f0a0-0000-7000-8000-000000000003";
const ORDER_LINE = "0195f0a0-0000-7000-8000-000000000004";
const SLIP = "0195f0a0-0000-7000-8000-000000000005";
const REV = "0195f0a0-0000-7000-8000-000000000006";
const WH = "0195f0a0-0000-7000-8000-000000000007";
const ID = "0195f0a0-0000-7000-8000-0000000000aa";

const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);
const posts = (suffix: string) => server.calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));

function shipment(status: Shipment["status"], qty: Partial<Shipment["lines"][number]> = {}, version = 3): Shipment {
  return {
    id: SH,
    shipmentNumber: "SH-0000042",
    legalEntityId: ID,
    warehouseId: WH,
    orderId: ORDER,
    customerId: ID,
    status,
    plannedShipDate: "2026-10-26T00:00:00Z",
    actualShipDate: null,
    version,
    lines: [
      { id: LINE, orderLineId: ORDER_LINE, productId: ID, unit: "EA", qtyPlanned: "100", qtyPicked: "0", qtyPacked: "0", qtyShipped: "0", pickConfirmed: false, ...qty },
    ],
    containers: [],
  };
}

const summary = (status: ShipmentSummary["status"], number: string): ShipmentSummary => ({
  id: `${SH.slice(0, -2)}${number.slice(-2)}`,
  shipmentNumber: number,
  legalEntityId: ID,
  warehouseId: WH,
  orderId: ORDER,
  customerId: ID,
  status,
  plannedShipDate: "2026-10-26T00:00:00Z",
  actualShipDate: null,
  version: 1,
  lineCount: 2,
});

const slip: PackingSlip = {
  id: SLIP,
  packingSlipNumber: "PS-0000001",
  shipmentId: SH,
  orderId: ORDER,
  status: "POSTED",
  postingDate: "2026-10-26T00:00:00Z",
  currency: "USD",
  totalCost: "125",
  reversalOfId: null,
  lines: [{ orderLineId: ORDER_LINE, shipmentLineId: LINE, qty: "10", costAmount: "125", costSnapshotId: "snap-1" }],
  journal: [
    { account: "1405", amount: "125", currency: "USD", orderLineId: ORDER_LINE },
    { account: "1400", amount: "-125", currency: "USD", orderLineId: ORDER_LINE },
  ],
  packagingFees: [],
};

describe("shipments list", () => {
  it("lists shipments, filters by status and opens a shipment", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/fulfillment/shipments") {
        return json(200, { items: url.searchParams.get("status") === "PACKED" ? [summary("PACKED", "SH-0000002")] : [summary("RELEASED", "SH-0000001"), summary("PACKED", "SH-0000002")] });
      }
      if (url.pathname.startsWith("/api/v1/fulfillment/shipments/")) return json(200, shipment("RELEASED"));
      if (url.pathname === "/api/v1/fulfillment/packing-slips") return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt("/fulfillment/shipments");
    const table = await screen.findByRole("table", { name: "Shipments" });
    await within(table).findByText("SH-0000001");
    expect(within(table).getByText("Released")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "PACKED" } });
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Shipments" })).queryByText("SH-0000001")).toBeNull());
    expect(server.calls.at(-1)?.url.searchParams.get("status")).toBe("PACKED");

    fireEvent.click(within(screen.getByRole("table", { name: "Shipments" })).getByRole("button", { name: "SH-0000002" }));
    expect(await screen.findByRole("heading", { name: "Shipment SH-0000042" })).toBeInTheDocument();
  });

  it("shows the empty state when nothing is released yet", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/fulfillment/shipments");
    expect(await screen.findByText("No shipments yet")).toBeInTheDocument();
  });

  it("shows the error with its code and retries", async () => {
    let fail = true;
    server.handler = () => (fail ? json(503, { code: "COM-0500", detail: "internal error" }) : json(200, { items: [summary("RELEASED", "SH-0000009")] }));
    renderAt("/fulfillment/shipments");
    expect((await screen.findByRole("alert")).textContent).toContain("COM-0500");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("SH-0000009")).toBeInTheDocument();
  });
});

describe("workspace", () => {
  it("counts shipments by stage", async () => {
    server.handler = () =>
      json(200, { items: [summary("RELEASED", "SH-01"), summary("PACKED", "SH-02"), summary("PACKED", "SH-03"), summary("SHIPPED", "SH-04")] });
    renderAt("/fulfillment");
    const tiles = await screen.findByRole("list", { name: "Fulfillment tiles" });
    expect(tiles.textContent).toContain("Packed, not shipped 2");
    expect(tiles.textContent).toContain("Released, not picked 1");
    expect(tiles.textContent).toContain("Shipped 1");
    expect(server.calls[0].url.searchParams.get("limit")).toBe("500");
  });
});

describe("release lines", () => {
  it("validates like the service before sending", () => {
    const line = { ...emptyLine(), orderId: ORDER, orderLineId: ORDER_LINE, customerId: ID, legalEntityId: ID, productId: ID, qty: "0" };
    const { errors, input } = validateRelease({ warehouseId: "nope", horizonDays: "31", requireFullAtp: true, lines: [line, { ...line, qty: "1.1234567" }] });
    expect(input).toBeUndefined();
    expect(errors.warehouseId).toMatch(/UUID/);
    expect(errors.horizonDays).toMatch(/0 to 30/);
    expect(errors["lines[0].qty"]).toMatch(/above 0/);
    expect(errors["lines[1].qty"]).toMatch(/6 decimals/);
    expect(errors["lines[1].orderLineId"]).toMatch(/already listed/);
  });

  it("runs the release with an Idempotency-Key and shows created and skipped lines", async () => {
    server.handler = (url) =>
      url.pathname === "/api/v1/fulfillment/releases:run"
        ? json(201, { shipments: [shipment("RELEASED")], skipped: [{ orderLineId: ID, reason: "already_released" }] })
        : undefined;
    const { container } = renderAt("/fulfillment/release");
    fireEvent.change(screen.getByLabelText("Warehouse id"), { target: { value: WH } });
    fireEvent.change(screen.getByLabelText("Order id"), { target: { value: ORDER } });
    fireEvent.change(screen.getByLabelText("Order line id"), { target: { value: ORDER_LINE } });
    fireEvent.change(screen.getByLabelText("Customer id"), { target: { value: ID } });
    fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: ID } });
    fireEvent.change(screen.getByLabelText("Product id"), { target: { value: ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Run release" }));

    expect((await screen.findByRole("status")).textContent).toBe("Created 1 shipment; 1 line not released.");
    const call = posts("/releases:run")[0];
    expect(idempotencyKey(call)).toBeTruthy();
    expect(call.body).toMatchObject({ warehouseId: WH, requireFullAtp: true, lines: [{ orderLineId: ORDER_LINE, qty: "10", unit: "EA", atpOk: true, held: false }] });
    expect(within(screen.getByRole("table", { name: "Created shipments" })).getByRole("link", { name: "SH-0000042" })).toHaveAttribute("href", `/fulfillment/shipments/${SH}`);
    expect(within(screen.getByRole("table", { name: "Lines not released" })).getByText("Already on an open shipment")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the service's field errors next to the fields", async () => {
    server.handler = () => json(400, { code: "FUL-0050", detail: "request failed validation", errors: { "lines[0].customerId": "differs from another line of the same order" } });
    renderAt("/fulfillment/release");
    fireEvent.change(screen.getByLabelText("Warehouse id"), { target: { value: WH } });
    for (const label of ["Order id", "Order line id", "Customer id", "Legal entity id", "Product id"]) {
      fireEvent.change(screen.getByLabelText(label), { target: { value: label === "Order line id" ? ORDER_LINE : ID } });
    }
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Run release" }));
    expect(await screen.findByText("differs from another line of the same order")).toBeInTheDocument();
  });
});

describe("shipment detail", () => {
  function serve(current: () => Shipment, slips: PackingSlipSummary[] = []) {
    server.handler = (url, init) => {
      if (init.method === "POST") return undefined;
      if (url.pathname === `/api/v1/fulfillment/shipments/${SH}`) return json(200, current());
      if (url.pathname === "/api/v1/fulfillment/packing-slips") return json(200, { items: slips });
      return undefined;
    };
  }

  it("confirms a short pick with If-Match and the shipment version", async () => {
    let current = shipment("RELEASED");
    serve(() => current);
    const base = server.handler;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":confirm-pick")) {
        current = shipment("PICKED", { qtyPicked: "90", pickConfirmed: true }, 4);
        return json(200, current);
      }
      return base(url, init);
    };
    const { container } = renderAt(`/fulfillment/shipments/${SH}`);
    const field = await screen.findByLabelText(/picked$/);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(field, { target: { value: "101" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm pick" }));
    expect(await screen.findByText(/Over-pick: at most 100/)).toBeInTheDocument();
    expect(posts(":confirm-pick")).toHaveLength(0);

    fireEvent.change(field, { target: { value: "90" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm pick" }));
    await screen.findByRole("button", { name: "Close container" });
    const call = posts(":confirm-pick")[0];
    expect(header(call, "If-Match")).toBe('"3"');
    expect(idempotencyKey(call)).toBeTruthy();
    expect(call.body).toEqual({ picks: [{ shipmentLineId: LINE, picked: "90" }] });
    expect(screen.queryByRole("button", { name: "Confirm pick" })).toBeNull();
  });

  it("packs the picked quantity and reports the SSCC", async () => {
    let current = shipment("PICKED", { qtyPicked: "30", pickConfirmed: true }, 4);
    serve(() => current);
    const base = server.handler;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/containers")) {
        current = { ...shipment("PACKED", { qtyPicked: "30", qtyPacked: "30", pickConfirmed: true }, 5) };
        current.containers = [{ id: ID, containerType: "C12", sscc: "006141410000000011", status: "CLOSED", tareKg: "0.5", grossKg: "0.5", returnable: false, packagingMaterial: null, lines: [{ shipmentLineId: LINE, qty: "30" }] }];
        return json(201, { container: current.containers[0], shipment: current });
      }
      return base(url, init);
    };
    renderAt(`/fulfillment/shipments/${SH}`);
    fireEvent.click(await screen.findByRole("button", { name: "Close container" }));
    expect(await screen.findByText(/Enter the container type/)).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Container type"), { target: { value: "C12" } });
    expect(screen.getByLabelText(/to pack$/)).toHaveValue("30");
    fireEvent.click(screen.getByRole("button", { name: "Close container" }));
    expect(await within(await screen.findByRole("table", { name: "Containers" })).findByText("006141410000000011")).toBeInTheDocument();
    const call = posts("/containers")[0];
    expect(header(call, "If-Match")).toBe('"4"');
    expect(call.body).toEqual({ containerType: "C12", lines: [{ shipmentLineId: LINE, qty: "30" }] });
    expect(await screen.findByRole("button", { name: "Post packing slip" })).toBeInTheDocument();
  });

  it("previews the journal (AC-28.1) and posts the packing slip", async () => {
    serve(() => shipment("PACKED", { qtyPlanned: "10", qtyPicked: "10", qtyPacked: "10", pickConfirmed: true }, 5));
    const base = server.handler;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":post-packing-slip")) return json(201, slip);
      if (url.pathname === `/api/v1/fulfillment/packing-slips/${SLIP}`) return json(200, slip);
      return base(url, init);
    };
    renderAt(`/fulfillment/shipments/${SH}`);
    fireEvent.change(await screen.findByLabelText("Unit cost"), { target: { value: "12.50" } });
    fireEvent.change(screen.getByLabelText("Cost snapshot id"), { target: { value: "snap-1" } });
    const preview = screen.getByRole("table", { name: "Journal preview" });
    expect(within(preview).getByText("1405").closest("tr")?.textContent).toContain("125.00 USD");
    expect(within(preview).getByText("1400").closest("tr")?.textContent).toContain("125.00 USD");

    fireEvent.click(screen.getByLabelText("Post cost of goods sold at shipment"));
    expect(within(screen.getByRole("table", { name: "Journal preview" })).getByText("5000")).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("Post cost of goods sold at shipment"));

    fireEvent.click(screen.getByRole("button", { name: "Post packing slip" }));
    expect(await screen.findByRole("heading", { name: "Packing slip PS-0000001" })).toBeInTheDocument();
    const call = posts(":post-packing-slip")[0];
    expect(header(call, "If-Match")).toBe('"5"');
    expect(call.body).toMatchObject({ currency: "USD", cogsAtShipment: false, costs: [{ shipmentLineId: LINE, unitCost: "12.50", costSnapshotId: "snap-1" }] });
  });

  it("explains a version conflict", async () => {
    serve(() => shipment("PACKED", { qtyPlanned: "10", qtyPicked: "10", qtyPacked: "10", pickConfirmed: true }, 5));
    const base = server.handler;
    server.handler = (url, init) =>
      init.method === "POST" ? json(412, { code: "conflict_version", detail: "shipment is at version 6" }) : base(url, init);
    renderAt(`/fulfillment/shipments/${SH}`);
    fireEvent.change(await screen.findByLabelText("Unit cost"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Cost snapshot id"), { target: { value: "s" } });
    fireEvent.click(screen.getByRole("button", { name: "Post packing slip" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Someone else changed this shipment");
  });

  it("shows a load error with its code", async () => {
    server.handler = () => json(404, { code: "FUL-0020", detail: "shipment not found" });
    renderAt(`/fulfillment/shipments/${SH}`);
    expect((await screen.findByRole("alert")).textContent).toContain("FUL-0020");
  });
});

describe("packing slips", () => {
  it("lists slips and marks reversals", async () => {
    server.handler = () =>
      json(200, {
        items: [
          { ...slip, id: REV, packingSlipNumber: "PS-0000002", totalCost: "-125", reversalOfId: SLIP },
          { ...slip, status: "CANCELLED" },
        ],
      });
    renderAt("/fulfillment/packing-slips");
    const table = await screen.findByRole("table", { name: "Packing slips" });
    expect((await within(table).findByText("PS-0000002")).closest("tr")?.textContent).toContain("Reversal");
    expect(within(table).getByText("-125.00 USD")).toBeInTheDocument();
  });

  it("shows the empty state", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/fulfillment/packing-slips");
    expect(await screen.findByText("No packing slips yet")).toBeInTheDocument();
  });

  it("shows the journal and cancels after confirmation (AC-28.2)", async () => {
    const reversal: PackingSlip = {
      ...slip,
      id: REV,
      packingSlipNumber: "PS-0000002",
      reversalOfId: SLIP,
      totalCost: "-125",
      journal: slip.journal.map((j) => ({ ...j, amount: j.amount.startsWith("-") ? j.amount.slice(1) : `-${j.amount}` })),
    };
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":cancel")) return json(201, reversal);
      if (url.pathname.endsWith(REV)) return json(200, reversal);
      if (url.pathname.endsWith(SLIP)) return json(200, slip);
      return undefined;
    };
    const { container } = renderAt(`/fulfillment/packing-slips/${SLIP}`);
    const journal = await screen.findByRole("table", { name: "Journal" });
    const debit = within(journal).getByText("1405").closest("tr");
    expect(debit?.children[2].textContent).toBe("125.00 USD");
    expect(within(journal).getByText("1400").closest("tr")?.children[3].textContent).toBe("125.00 USD");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Cancel packing slip" }));
    expect(posts(":cancel")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Yes, cancel it" }));
    expect(await screen.findByRole("heading", { name: "Packing slip PS-0000002" })).toBeInTheDocument();
    expect(idempotencyKey(posts(":cancel")[0])).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Cancel packing slip" })).toBeNull();
    const mirrored = within(await screen.findByRole("table", { name: "Journal" })).getByText("1405").closest("tr");
    expect(mirrored?.children[3].textContent).toBe("125.00 USD");
  });

  it("reports an invoiced slip with FUL-0010", async () => {
    server.handler = (_url, init) =>
      init.method === "POST" ? json(409, { code: "FUL-0010", detail: "cannot cancel: packing slip PS-0000001 is invoiced" }) : json(200, slip);
    renderAt(`/fulfillment/packing-slips/${SLIP}`);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel packing slip" }));
    fireEvent.click(screen.getByRole("button", { name: "Yes, cancel it" }));
    expect((await screen.findByRole("alert")).textContent).toContain("FUL-0010");
  });
});
