import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Container, Shipment } from "./api/client";
import { canUnpack } from "./ShipmentTracking";
import { canUnnest, isFreeCarton } from "./PalletForm";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const SH = "0195f0a0-0000-7000-8000-000000000001";
const LINE = "0195f0a0-0000-7000-8000-000000000002";
const ID = "0195f0a0-0000-7000-8000-0000000000aa";
const WH = "0195f0a0-0000-7000-8000-000000000007";
const C1 = "0195f0a0-0000-7000-8000-0000000000c1";
const C2 = "0195f0a0-0000-7000-8000-0000000000c2";
const PAL = "0195f0a0-0000-7000-8000-0000000000c3";

const posts = (suffix: string) => server.calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

const carton = (id: string, sscc: string, extra: Partial<Container> = {}): Container => ({
  id,
  containerType: "BOX",
  sscc,
  status: "CLOSED",
  tareKg: "0.5",
  grossKg: "5",
  returnable: false,
  packagingMaterial: null,
  packingSlipId: null,
  parentContainerId: null,
  childContainerIds: [],
  lines: [{ shipmentLineId: LINE, qty: "50" }],
  ...extra,
});

function shipment(status: Shipment["status"], containers: Container[], version = 3): Shipment {
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
    lines: [{ id: LINE, orderLineId: ID, productId: ID, unit: "EA", qtyPlanned: "100", qtyPicked: "100", qtyPacked: "100", qtyShipped: "0", pickConfirmed: true, zone: "A1" }],
    containers,
  };
}

function serve(current: () => Shipment) {
  server.handler = (url, init) => {
    if (init.method === "POST") return undefined;
    if (url.pathname === `/api/v1/fulfillment/shipments/${SH}`) return json(200, current());
    if (url.pathname === `/api/v1/fulfillment/shipments/${SH}/tracking`) return json(200, { items: [] });
    if (url.pathname === "/api/v1/fulfillment/packing-slips") return json(200, { items: [] });
    return undefined;
  };
}

describe("nested containers (pallet to carton)", () => {
  it("knows which containers can nest, dissolve or unpack", () => {
    const packed = shipment("PACKED", []);
    const onPallet = carton(C1, "106141411234567897", { parentContainerId: PAL });
    const pallet = carton(PAL, "106141411234567824", { containerType: "EUR-PALLET", childContainerIds: [C1], lines: [] });
    expect(isFreeCarton(carton(C2, "1"))).toBe(true);
    expect(isFreeCarton(onPallet)).toBe(false);
    expect(isFreeCarton(pallet)).toBe(false);
    expect(canUnnest(packed, pallet)).toBe(true);
    expect(canUnnest(packed, onPallet)).toBe(false);
    expect(canUnnest(shipment("SHIPPED", []), pallet)).toBe(false);
    expect(canUnpack(packed, onPallet)).toBe(false);
    expect(canUnpack(packed, pallet)).toBe(false);
    expect(canUnpack(packed, carton(C2, "1"))).toBe(true);
  });

  it("puts the chosen cartons on a pallet with one Idempotency-Key and shows the pallet", async () => {
    let current = shipment("PACKED", [carton(C1, "106141411234567897"), carton(C2, "106141411234567800")]);
    serve(() => current);
    const base = server.handler;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":nest-containers")) {
        const pallet = carton(PAL, "106141411234567824", { containerType: "EUR-PALLET", grossKg: "35", childContainerIds: [C1, C2], lines: [] });
        current = shipment("PACKED", [carton(C1, "106141411234567897", { parentContainerId: PAL }), carton(C2, "106141411234567800", { parentContainerId: PAL }), pallet], 4);
        return json(201, { container: pallet, shipment: current });
      }
      return base(url, init);
    };
    const { container } = renderAt(`/fulfillment/shipments/${SH}`);
    expect(await screen.findByRole("heading", { name: "Put cartons on a pallet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Close pallet" }));
    expect(await screen.findByText("Enter the pallet type, such as EUR-PALLET.")).toBeInTheDocument();
    expect(screen.getByText("Choose at least one carton.")).toBeInTheDocument();
    expect(posts(":nest-containers")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Pallet type"), { target: { value: " EUR-PALLET " } });
    fireEvent.click(screen.getByRole("checkbox", { name: /106141411234567897/ }));
    fireEvent.click(screen.getByRole("checkbox", { name: /106141411234567800/ }));
    fireEvent.click(screen.getByRole("button", { name: "Close pallet" }));
    await screen.findByRole("button", { name: "Dissolve pallet 106141411234567824" });
    const call = posts(":nest-containers")[0];
    expect(header(call, "If-Match")).toBe('"3"');
    expect(idempotencyKey(call)).toBeTruthy();
    expect(call.body).toEqual({ containerType: "EUR-PALLET", containerIds: [C1, C2] });
    const table = await findTable({ name: "Containers" });
    expect(table).toHaveTextContent("2 cartons");
    expect(screen.queryByRole("button", { name: "Unpack 106141411234567897" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "Put cartons on a pallet" })).toBeNull();
  });

  it("shows the service refusal when the cartons cannot nest", async () => {
    serve(() => shipment("PACKED", [carton(C1, "106141411234567897")]));
    const base = server.handler;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":nest-containers")) {
        return json(422, { type: "about:blank", title: "Unprocessable", status: 422, code: "FUL-0056", detail: "FUL-0056: container nesting is not allowed: container is already on a pallet" });
      }
      return base(url, init);
    };
    renderAt(`/fulfillment/shipments/${SH}`);
    fireEvent.change(await screen.findByLabelText("Pallet type"), { target: { value: "EUR-PALLET" } });
    fireEvent.click(screen.getByRole("checkbox", { name: /106141411234567897/ }));
    fireEvent.click(screen.getByRole("button", { name: "Close pallet" }));
    expect(await screen.findByText("Could not nest the containers")).toBeInTheDocument();
  });

  it("dissolves a pallet after confirmation and offers the cartons again", async () => {
    const pallet = carton(PAL, "106141411234567824", { containerType: "EUR-PALLET", childContainerIds: [C1], lines: [] });
    let current = shipment("PACKED", [carton(C1, "106141411234567897", { parentContainerId: PAL }), pallet]);
    serve(() => current);
    const base = server.handler;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/containers/${PAL}:unnest`)) {
        current = shipment("PACKED", [carton(C1, "106141411234567897")], 4);
        return json(200, current);
      }
      return base(url, init);
    };
    renderAt(`/fulfillment/shipments/${SH}`);
    expect(screen.queryByRole("heading", { name: "Put cartons on a pallet" })).toBeNull();
    const dissolve = await screen.findByRole("button", { name: "Dissolve pallet 106141411234567824" });
    const rows = within(await findTable({ name: "Containers" })).getAllByRole("row");
    expect(rows.find((r) => r.textContent?.includes("106141411234567897"))).toHaveTextContent("106141411234567824");
    expect(rows.find((r) => r.textContent?.includes("EUR-PALLET"))).toHaveTextContent(/1 carton(?!s)/);
    fireEvent.click(dissolve);
    expect(posts(":unnest")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Confirm dissolve 106141411234567824" }));
    expect(await screen.findByRole("heading", { name: "Put cartons on a pallet" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Unpack 106141411234567897" })).toBeInTheDocument());
    expect(header(posts(":unnest")[0], "If-Match")).toBe('"3"');
  });

  it("offers no nesting once the shipment has shipped", async () => {
    serve(() => shipment("SHIPPED", [carton(C1, "106141411234567897", { status: "SHIPPED" })]));
    renderAt(`/fulfillment/shipments/${SH}`);
    await findTable({ name: "Containers" });
    expect(screen.queryByRole("heading", { name: "Put cartons on a pallet" })).toBeNull();
  });
});
