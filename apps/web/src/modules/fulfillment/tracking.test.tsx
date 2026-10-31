import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Shipment } from "./api/client";
import { emptyLine, validateRelease } from "./ReleasePage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const SH = "0195f0a0-0000-7000-8000-000000000001";
const LINE = "0195f0a0-0000-7000-8000-000000000002";
const ID = "0195f0a0-0000-7000-8000-0000000000aa";

const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

function shipment(status: Shipment["status"], line: Partial<Shipment["lines"][number]> = {}, version = 5): Shipment {
  return {
    id: SH,
    shipmentNumber: "SH-0000042",
    legalEntityId: ID,
    warehouseId: ID,
    orderId: ID,
    customerId: ID,
    status,
    plannedShipDate: "2026-10-26T00:00:00Z",
    actualShipDate: null,
    carrierCode: null,
    version,
    lines: [
      { id: LINE, orderLineId: ID, productId: ID, unit: "EA", qtyPlanned: "10", qtyPicked: "10", qtyPacked: "0", qtyShipped: "0", pickConfirmed: true, zone: null, trackingRequired: true, tracking: [], ...line },
    ],
    containers: [],
  };
}

function serve(current: () => Shipment) {
  server.handler = (url, init) => {
    if (init.method === "POST" && url.pathname.endsWith(":capture-tracking")) return undefined;
    if (url.pathname === `/api/v1/fulfillment/shipments/${SH}`) return json(200, current());
    if (url.pathname === `/api/v1/fulfillment/shipments/${SH}/tracking`) return json(200, { items: [] });
    if (url.pathname === "/api/v1/fulfillment/packing-slips") return json(200, { items: [] });
    return undefined;
  };
}

describe("batch and serial tracking on the shipment detail", () => {
  it("flags a tracked line without entries and sends the entries with If-Match", async () => {
    let current = shipment("PICKED");
    serve(() => current);
    const base = server.handler;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":capture-tracking")) {
        current = shipment(
          "PICKED",
          {
            tracking: [
              { id: "e1", batchNo: "L-1", serialNo: null, locationCode: "A-01", qty: "6", packingSlipId: null },
              { id: "e2", batchNo: "L-2", serialNo: null, locationCode: null, qty: "4", packingSlipId: null },
            ],
          },
          6,
        );
        return json(200, current);
      }
      return base(url, init);
    };
    const { container } = renderAt(`/fulfillment/shipments/${SH}`);
    expect(await screen.findByText("Required")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    // A blank entry needs a batch or serial.
    fireEvent.change(screen.getByLabelText("Entry 1 quantity"), { target: { value: "6" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tracking" }));
    expect(await screen.findByText(/Enter a batch or a serial number/)).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Entry 1 batch"), { target: { value: "L-1" } });
    fireEvent.change(screen.getByLabelText("Entry 1 location"), { target: { value: "A-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Add entry" }));
    fireEvent.change(screen.getByLabelText("Entry 2 batch"), { target: { value: "L-2" } });
    fireEvent.change(screen.getByLabelText("Entry 2 quantity"), { target: { value: "4" } });
    expect(screen.getByText(/Covered 10 of 10 picked/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save tracking" }));

    await waitFor(() => expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(1));
    const call = server.calls.find((c) => c.method === "POST")!;
    expect(call.url.pathname).toBe(`/api/v1/fulfillment/shipments/${SH}/lines/${LINE}:capture-tracking`);
    expect(header(call, "If-Match")).toBe('"5"');
    expect(idempotencyKey(call)).toBeTruthy();
    expect(call.body).toEqual({
      entries: [
        { batchNo: "L-1", locationCode: "A-01", qty: "6" },
        { batchNo: "L-2", qty: "4" },
      ],
    });
    expect(await screen.findByText(/L-1@A-01 × 6, L-2 × 4 \(10 of 10\)/)).toBeInTheDocument();
  });

  it("refuses a serial of more than one unit and tracking above the picked quantity before calling", async () => {
    serve(() => shipment("PICKED", { qtyPicked: "2" }));
    renderAt(`/fulfillment/shipments/${SH}`);
    fireEvent.change(await screen.findByLabelText("Entry 1 serial"), { target: { value: "SN-1" } });
    fireEvent.change(screen.getByLabelText("Entry 1 quantity"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tracking" }));
    expect(await screen.findByText("A serial number is one unit.")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Entry 1 serial"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("Entry 1 batch"), { target: { value: "L-9" } });
    fireEvent.change(screen.getByLabelText("Entry 1 quantity"), { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tracking" }));
    expect(await screen.findByText(/cannot exceed the picked quantity 2 \(FUL-0035\)/)).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("shows the service's refusal of a serial already used", async () => {
    serve(() => shipment("PICKED"));
    const base = server.handler;
    server.handler = (url, init) =>
      init.method === "POST" && url.pathname.endsWith(":capture-tracking")
        ? json(409, { code: "FUL-0036", detail: "serial SN-1 is already assigned to another shipment line of this product" })
        : base(url, init);
    renderAt(`/fulfillment/shipments/${SH}`);
    fireEvent.change(await screen.findByLabelText("Entry 1 serial"), { target: { value: "SN-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tracking" }));
    expect(await screen.findByText(/already assigned to another shipment line/)).toBeInTheDocument();
    expect(screen.getByText(/FUL-0036/)).toBeInTheDocument();
  });

  it("offers no tracking form before anything is picked or after the shipment shipped", async () => {
    serve(() => shipment("RELEASED", { qtyPicked: "0", pickConfirmed: false }));
    const first = renderAt(`/fulfillment/shipments/${SH}`);
    await screen.findByText("Shipment SH-0000042");
    expect(screen.queryByRole("button", { name: "Save tracking" })).toBeNull();
    first.unmount();
    serve(() => shipment("SHIPPED"));
    renderAt(`/fulfillment/shipments/${SH}`);
    await screen.findByText("Shipment SH-0000042");
    expect(screen.queryByRole("button", { name: "Save tracking" })).toBeNull();
  });

  it("counts entries already shipped and puts a row's error on that row", async () => {
    serve(() =>
      shipment("PARTIALLY_SHIPPED", {
        qtyPicked: "3",
        qtyShipped: "1",
        tracking: [{ id: "e0", batchNo: null, serialNo: "SN-0", locationCode: null, qty: "1", packingSlipId: ID }],
      }),
    );
    renderAt(`/fulfillment/shipments/${SH}`);
    // The shipped serial counts against the picked quantity and is not offered for editing.
    expect(await screen.findByText(/Covered 1 of 3 picked \(1 already shipped\), 2 untracked/)).toBeInTheDocument();
    expect(screen.getByLabelText("Entry 1 serial")).toHaveValue("");
    fireEvent.click(screen.getByRole("button", { name: "Add entry" }));
    // Entry 1 stays blank (ignored); entry 2 has a location only.
    fireEvent.change(screen.getByLabelText("Entry 2 location"), { target: { value: "A-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tracking" }));
    const message = await screen.findByText("Enter a batch or a serial number.");
    expect(screen.getByLabelText("Entry 2 batch")).toHaveAccessibleDescription(expect.stringContaining(message.textContent ?? ""));
    expect(screen.getByLabelText("Entry 1 batch")).not.toHaveAccessibleDescription(expect.stringContaining("batch or a serial"));
    expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });
});

describe("release lines with tracking", () => {
  it("sends trackingRequired only for a tracked line", () => {
    const line = { ...emptyLine(), orderId: ID, orderLineId: LINE, customerId: ID, legalEntityId: ID, productId: ID, qty: "2" };
    const tracked = validateRelease({ warehouseId: ID, horizonDays: "2", requireFullAtp: true, lines: [{ ...line, trackingRequired: true }] });
    expect(tracked.input?.lines[0].trackingRequired).toBe(true);
    const plain = validateRelease({ warehouseId: ID, horizonDays: "2", requireFullAtp: true, lines: [line] });
    expect(plain.input?.lines[0]).not.toHaveProperty("trackingRequired");
  });
});
