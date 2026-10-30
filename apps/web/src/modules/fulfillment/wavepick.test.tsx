import { findAlert } from "../../test-utils";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Wave } from "./api/client";
import { idempotencyKey, json, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const WH = "0195f0a0-0000-7000-8000-000000000007";
const SH = "0195f0a0-0000-7000-8000-000000000001";
const WAVE = "0195f0a0-0000-7000-8000-0000000000e1";
const ID = "0195f0a0-0000-7000-8000-0000000000aa";
const L1 = "0195f0a0-0000-7000-8000-0000000000b1";
const L2 = "0195f0a0-0000-7000-8000-0000000000b2";

const line = (id: string, planned: string, confirmed = false) => ({
  shipmentLineId: id,
  shipmentId: SH,
  shipmentNumber: "SH-0000001",
  productId: ID,
  unit: "EA",
  zone: "Z1",
  qtyPlanned: planned,
  qtyPicked: confirmed ? planned : "0",
  pickConfirmed: confirmed,
  shortPickReason: null,
});

const wave = (lines: Wave["lines"]): Wave => ({
  id: WAVE,
  waveNumber: "WV-0000001",
  legalEntityId: ID,
  warehouseId: WH,
  groupBy: "CARRIER",
  groupKey: "UPS",
  status: "OPEN",
  lineCount: 2,
  orderCount: 1,
  createdAt: "2026-10-27T08:00:00Z",
  shipments: [{ id: SH, shipmentNumber: "SH-0000001", orderId: ID, customerId: ID, status: "PICKING", carrierCode: "UPS", plannedShipDate: "2026-10-27T00:00:00Z", lineCount: 2 }],
  lines,
});

const confirms = () => server.calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(":confirm-pick"));

describe("wave pick confirmation (FR-28.13)", () => {
  it("sends only the short lines with their reason and picks the rest in full", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `/api/v1/fulfillment/waves/${WAVE}:confirm-pick` && init.method === "POST") {
        return json(200, { waveId: WAVE, linesConfirmed: 2, shortLines: 1, shipments: [{ id: SH, shipmentNumber: "SH-0000001", status: "PICKED", version: 4, linesConfirmed: 2 }] });
      }
      if (url.pathname === `/api/v1/fulfillment/waves/${WAVE}`) return json(200, wave([line(L1, "10"), line(L2, "5")]));
      return undefined;
    };
    renderAt(`/fulfillment/waves/${WAVE}`);
    const first = await screen.findByLabelText(/SH-0000001, product .* picked of 10/);
    fireEvent.change(first, { target: { value: "20" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm 2 lines" }));
    expect(await screen.findByText("Over-pick: at most 10 is planned (FUL-0004).")).toBeInTheDocument();
    expect(confirms()).toHaveLength(0);
    fireEvent.change(first, { target: { value: "4" } });
    fireEvent.change(await screen.findByLabelText(/Reason for SH-0000001, product .* short pick/), { target: { value: "OUT_OF_STOCK" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm 2 lines" }));
    expect(await screen.findByText("Wave picks confirmed")).toBeInTheDocument();
    expect(confirms()).toHaveLength(1);
    expect(JSON.parse(String(confirms()[0].init.body))).toEqual({ picks: [{ shipmentLineId: L1, picked: "4", reason: "OUT_OF_STOCK" }], pickRest: true });
    expect(idempotencyKey(confirms()[0])).toBeTruthy();
  });

  it("shows the service problem and keeps the form", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(422, { code: "FUL-0001", detail: "cannot pick a shipment in status CANCELLED" });
      return url.pathname === `/api/v1/fulfillment/waves/${WAVE}` ? json(200, wave([line(L1, "10")])) : undefined;
    };
    renderAt(`/fulfillment/waves/${WAVE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Confirm 1 line" }));
    expect((await findAlert()).textContent).toContain("FUL-0001");
    await waitFor(() => expect(screen.getByRole("button", { name: "Confirm 1 line" })).toBeEnabled());
  });

  it("does not offer the lines of a cancelled shipment", async () => {
    const CANCELLED = "0195f0a0-0000-7000-8000-000000000009";
    const w = wave([line(L1, "10"), { ...line(L2, "5"), shipmentId: CANCELLED, shipmentNumber: "SH-0000009" }]);
    w.shipments.push({ ...w.shipments[0], id: CANCELLED, shipmentNumber: "SH-0000009", status: "CANCELLED", lineCount: 1 });
    server.handler = (url) => (url.pathname === `/api/v1/fulfillment/waves/${WAVE}` ? json(200, w) : undefined);
    renderAt(`/fulfillment/waves/${WAVE}`);
    expect(await screen.findByRole("button", { name: "Confirm 1 line" })).toBeInTheDocument();
    expect(screen.queryByLabelText(/SH-0000009/)).toBeNull();
  });

  it("offers no form when every line is confirmed", async () => {
    server.handler = (url) => (url.pathname === `/api/v1/fulfillment/waves/${WAVE}` ? json(200, wave([line(L1, "10", true)])) : undefined);
    renderAt(`/fulfillment/waves/${WAVE}`);
    expect(await screen.findByRole("heading", { name: "Wave WV-0000001" })).toBeInTheDocument();
    expect(screen.queryByText("Confirm picks for the wave")).toBeNull();
  });
});
