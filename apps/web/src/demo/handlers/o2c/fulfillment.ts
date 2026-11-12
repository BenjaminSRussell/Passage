// Fulfillment service (/api/v1/fulfillment) for the demo: shipments, packing slips and tracking for the shipped
// sales orders in model.ts. Reads only; picking and posting are not part of the demo.
import type { ContainerType, LegalEntitySetting, OperationRule, PackingSlipSummary, ReleaseRule, TrackingEvent, WarehouseSetting } from "../../../modules/fulfillment/api/client";
import { CUSTOMERS, ITEMS, LEGAL_ENTITY, WH_DAL, WH_FG } from "../../company";
import { reply, route } from "../../backend";
import { at, matchesQ } from "../../store";
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
    .filter((s) => (!query.get("shipmentId") || s.shipmentId === query.get("shipmentId")) && (!query.get("orderId") || s.orderId === query.get("orderId")) && (!query.get("status") || s.status === query.get("status")) && matchesQ(query, s.packingSlipNumber))
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

// Fulfillment setup lists (#505): container types, posting settings of the legal entity, and the per-warehouse
// operation, release and short-pick rules of the two shipping warehouses.
const containerTypes: ContainerType[] = [
  { code: "CTN-S", tareKg: "0.40", returnable: false, packagingMaterial: "CARDBOARD" },
  { code: "CTN-L", tareKg: "0.95", returnable: false, packagingMaterial: "CARDBOARD" },
  { code: "CRATE-PUMP", tareKg: "18.50", returnable: true, packagingMaterial: "WOOD" },
  { code: "PALLET-EUR", tareKg: "25.00", returnable: true, packagingMaterial: "WOOD" },
];
route("GET", `${BASE}/container-types`, () => ({ items: containerTypes }));

const legalEntitySettings: LegalEntitySetting[] = [
  { legalEntityId: LEGAL_ENTITY.id, cogsAtShipment: false, asnEnabled: true, gs1CompanyPrefix: "0614141", ssccExtensionDigit: 3, packagingFeeBillable: true, rtpTrackingEnabled: true, rtpChargeDays: 90, version: 2, updatedAt: at(-40) },
];
route("GET", `${BASE}/legal-entity-settings`, () => ({ items: legalEntitySettings }));

const shippingWarehouses = [WH_FG, WH_DAL];
route("GET", `${BASE}/operation-rules`, (): { items: OperationRule[] } => ({
  items: shippingWarehouses.map((w, i) => ({ warehouseId: w.id, legalEntityId: LEGAL_ENTITY.id, postingDateSource: i === 0 ? "SHIP_TIMESTAMP" : "TODAY", timeZone: "America/Chicago", autoPackEnabled: true, workbenchGroupKeyDefault: i === 0 ? "CUSTOMER" : "CARRIER", version: 1, updatedAt: at(-60) })),
}));
route("GET", `${BASE}/release-rules`, (): { items: ReleaseRule[] } => ({
  items: shippingWarehouses.map((w, i) => ({ warehouseId: w.id, legalEntityId: LEGAL_ENTITY.id, horizonDays: i === 0 ? 3 : 2, requireFullAtp: i === 0, waveMaxLines: 200, waveMaxOrders: 40, version: 1, updatedAt: at(-60) })),
}));
route("GET", `${BASE}/warehouse-settings`, (): { items: WarehouseSetting[] } => ({
  items: shippingWarehouses.map((w, i) => ({ warehouseId: w.id, legalEntityId: LEGAL_ENTITY.id, shortPickAction: i === 0 ? "REMAINDER_BACKORDER" : "KEEP_OPEN", version: 1, updatedAt: at(-30) })),
}));

// Packing rules, packaging materials and returnable balances (#505 lists): a few rows for the Cedar Ridge Pumps
// pumps and the two biggest customers. Drop-ships: none are open in the demo.
route("GET", `${BASE}/packing-rules`, ({ query }) => {
  const rows = [
    { id: "0f1a0000-0000-4000-8000-000000000001", productId: ITEMS[0].id, customerId: null, containerType: "CRATE-PUMP", qtyPerContainer: "1", maxWeightKg: "180" },
    { id: "0f1a0000-0000-4000-8000-000000000002", productId: ITEMS[1].id, customerId: null, containerType: "CTN-L", qtyPerContainer: "4", maxWeightKg: "40" },
    { id: "0f1a0000-0000-4000-8000-000000000003", productId: ITEMS[1].id, customerId: CUSTOMERS[0].id, containerType: "PALLET-EUR", qtyPerContainer: "24", maxWeightKg: "900" },
  ];
  const product = query.get("productId");
  return { items: rows.filter((r) => !product || r.productId === product) };
});
route("GET", `${BASE}/product-packaging`, ({ query }) => {
  const rows = [
    { productId: ITEMS[0].id, materialCode: "WOOD", weightPerUnitKg: "18.5" },
    { productId: ITEMS[1].id, materialCode: "CARDBOARD", weightPerUnitKg: "0.95" },
  ];
  const product = query.get("productId");
  return { items: rows.filter((r) => !product || r.productId === product) };
});
route("GET", `${BASE}/returnable-packaging`, ({ query }) => {
  const rows = [
    { customerId: CUSTOMERS[0].id, containerType: "PALLET-EUR", balance: "6", lastShippedAt: at(-4), lastReturnedAt: at(-20) },
    { customerId: CUSTOMERS[2].id, containerType: "CRATE-PUMP", balance: "2", lastShippedAt: at(-9), lastReturnedAt: null },
  ];
  const customer = query.get("customerId");
  return { items: rows.filter((r) => !customer || r.customerId === customer) };
});
route("GET", `${BASE}/drop-ships`, () => ({ items: [] }));
