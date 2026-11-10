// Fulfillment service (/api/v1/fulfillment) for the demo: shipments, packing slips and tracking for the shipped
// sales orders in model.ts. Reads only; picking and posting are not part of the demo.
import type { OperationRule, PackingSlipSummary, TrackingEvent } from "../../../modules/fulfillment/api/client";
import { LEGAL_ENTITY } from "../../company";
import { reply, route } from "../../backend";
import { at } from "../../store";
import { shipments, slips } from "./model";

const BASE = "/api/v1/fulfillment";

route("GET", `${BASE}/shipments`, ({ query }) => {
  const status = query.get("status");
  const orderId = query.get("orderId");
  const warehouseId = query.get("warehouseId");
  const limit = Number(query.get("limit") ?? 0);
  const rows = shipments
    .all()
    .filter((s) => (!status || s.status === status) && (!orderId || s.orderId === orderId) && (!warehouseId || s.warehouseId === warehouseId))
    .sort((a, b) => b.plannedShipDate.localeCompare(a.plannedShipDate) || b.shipmentNumber.localeCompare(a.shipmentNumber))
    .map(({ lines, containers: _containers, ...header }) => ({ ...header, lineCount: lines.length }));
  return { items: limit > 0 ? rows.slice(0, limit) : rows };
});

route("GET", `${BASE}/shipments/{id}/tracking`, ({ params }) => {
  const s = shipments.get(params.id);
  if (!s || !["PARTIALLY_SHIPPED", "SHIPPED", "DELIVERED"].includes(s.status)) return { items: [] };
  const ship = Date.parse(s.actualShipDate ?? at(0));
  const when = (days: number) => new Date(ship + days * 86_400_000).toISOString();
  const number = `${s.carrierCode}-${s.shipmentNumber.slice(-5)}7${s.version}`;
  const base = { provider: s.carrierCode ?? "CARRIER", trackingNumber: number };
  const events: TrackingEvent[] = [
    { ...base, eventKey: `${number}-1`, status: "LABELLED", description: "Shipping label created", eventTime: when(-0.1) },
    { ...base, eventKey: `${number}-2`, status: "IN_TRANSIT", description: "Picked up at Cedar Rapids plant", eventTime: when(0.2) },
  ];
  if (s.status === "DELIVERED") events.push({ ...base, eventKey: `${number}-3`, status: "DELIVERED", description: "Delivered, signed for at dock", eventTime: when(3.4) });
  else events.push({ ...base, eventKey: `${number}-3`, status: "IN_TRANSIT", description: "Arrived at regional terminal", eventTime: when(1.6) });
  return { items: events.reverse() };
});

route("GET", `${BASE}/shipments/{id}`, ({ params }) => {
  const s = shipments.get(params.id);
  return s ? s : reply.problem(404, "FUL-0404", "Shipment not found");
});

function slipSummary(s: ReturnType<typeof slips.all>[number]): PackingSlipSummary {
  return { id: s.id, packingSlipNumber: s.packingSlipNumber, shipmentId: s.shipmentId, orderId: s.orderId, status: s.status, postingDate: s.postingDate, currency: s.currency, totalCost: s.totalCost, reversalOfId: s.reversalOfId };
}

route("GET", `${BASE}/packing-slips`, ({ query }) => {
  const rows = slips
    .all()
    .filter((s) => (!query.get("shipmentId") || s.shipmentId === query.get("shipmentId")) && (!query.get("orderId") || s.orderId === query.get("orderId")) && (!query.get("status") || s.status === query.get("status")))
    .sort((a, b) => b.postingDate.localeCompare(a.postingDate))
    .map(slipSummary);
  return { items: rows };
});
route("GET", `${BASE}/packing-slips/{id}`, ({ params }) => {
  const s = slips.get(params.id);
  return s ?? reply.problem(404, "FUL-0404", "Packing slip not found");
});

// Settings the shipment page reads beside the shipment.
route("GET", `${BASE}/operation-rules/{warehouseId}`, ({ params }): OperationRule => ({
  warehouseId: params.warehouseId,
  legalEntityId: LEGAL_ENTITY.id,
  postingDateSource: "SHIP_TIMESTAMP",
  timeZone: "America/Chicago",
  autoPackEnabled: true,
  workbenchGroupKeyDefault: "CARRIER",
  version: 1,
  updatedAt: at(-90),
}));
route("GET", `${BASE}/legal-entity-settings/{id}`, ({ params }) => ({
  legalEntityId: params.id,
  cogsAtShipment: false,
  asnEnabled: true,
  gs1CompanyPrefix: "0614141",
  ssccExtensionDigit: 0,
  packagingFeeBillable: false,
  rtpTrackingEnabled: true,
  rtpChargeDays: 90,
  version: 1,
  updatedAt: at(-90),
}));
route("GET", `${BASE}/delivery-tolerances`, () => ({ items: [] }));
