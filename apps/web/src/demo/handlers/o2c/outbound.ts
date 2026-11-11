// Fulfillment service (/api/v1/fulfillment) for the demo, beyond shipments and packing slips in fulfillment.ts: pick
// waves over the open shipments, the short picks and backorders report, and carrier manifests with the labelled
// parcels not on one yet. Everything is derived from the shipments of model.ts, so wave lines, backorders and
// manifest parcels name the same shipments, orders and customers as the shipment list.
import type { BackorderRow, Manifest, ManifestCandidate, ManifestInput, Shipment, ShortPickReason, Wave, WaveLine, WaveShipment } from "../../../modules/fulfillment/api/client";
import { LEGAL_ENTITY, USER, WH_FG } from "../../company";
import { reply, route, type DemoRequest } from "../../backend";
import { at, day, dec, table, uid } from "../../store";
import { K, shipments } from "./model";

const BASE = "/api/v1/fulfillment";
const K_WAVE = 430;
const K_MANIFEST = 431;

const shipmentOf = (n: number): Shipment => shipments.get(uid(K.shipment, n))!;
const ifMatch = (req: DemoRequest): number | undefined => {
  const raw = req.headers.get("If-Match");
  return raw ? Number(raw.replace(/"/g, "")) : undefined;
};
const stale = () => reply.problem(409, "FUL-0409", "Someone changed this since you opened it. Reload and try again.");

// ---- Short picks and backorders -----------------------------------------------------------------------------

/** Lines picked below plan: SO-024183's CP-400 came up two short (only 11 in stock, 9 reserved elsewhere). */
const SHORTS: { shipment: number; line: number; reason: ShortPickReason; ago: number; closed?: number }[] = [
  { shipment: 3, line: 0, reason: "OUT_OF_STOCK", ago: 2.2 },
  { shipment: 8, line: 0, reason: "QUALITY_HOLD", ago: 0.3 },
];

function backorders(): BackorderRow[] {
  return SHORTS.flatMap(({ shipment, line, reason, ago, closed = 0 }) => {
    const s = shipmentOf(shipment);
    const l = s?.lines[line];
    if (!s || !l) return [];
    const remainder = Number(l.qtyPlanned) - Number(l.qtyPicked) - closed;
    return [
      {
        shipmentLineId: l.id,
        shipmentId: s.id,
        shipmentNumber: s.shipmentNumber,
        status: s.status,
        warehouseId: s.warehouseId,
        orderId: s.orderId,
        customerId: s.customerId,
        orderLineId: l.orderLineId,
        productId: l.productId,
        unit: l.unit,
        qtyPlanned: l.qtyPlanned,
        qtyPicked: l.qtyPicked,
        qtyShipped: l.qtyShipped,
        qtyClosed: dec(closed, 0),
        qtyRemainder: dec(Math.max(0, remainder), 0),
        open: remainder > 0 && s.status !== "CANCELLED",
        shortPickedAt: at(-ago),
        reason,
      },
    ];
  });
}

route("GET", `${BASE}/backorders`, ({ query }) => {
  const all = query.get("status") === "all";
  const wh = query.get("warehouseId");
  const from = query.get("from");
  const to = query.get("to");
  const items = backorders()
    .filter((b) => (all || b.open) && (!wh || b.warehouseId === wh) && (!from || b.shortPickedAt.slice(0, 10) >= from) && (!to || b.shortPickedAt.slice(0, 10) <= to))
    .sort((a, b) => b.shortPickedAt.localeCompare(a.shortPickedAt));
  return { items };
});

// ---- Waves --------------------------------------------------------------------------------------------------

const waveShipment = (s: Shipment): WaveShipment => ({ id: s.id, shipmentNumber: s.shipmentNumber, orderId: s.orderId, customerId: s.customerId, status: s.status, carrierCode: s.carrierCode, plannedShipDate: s.plannedShipDate, lineCount: s.lines.length });
const waveLines = (list: Shipment[]): WaveLine[] =>
  list.flatMap((s) =>
    s.lines.map((l) => ({
      shipmentLineId: l.id,
      shipmentId: s.id,
      shipmentNumber: s.shipmentNumber,
      productId: l.productId,
      unit: l.unit,
      zone: l.zone ?? null,
      qtyPlanned: l.qtyPlanned,
      qtyPicked: l.qtyPicked,
      pickConfirmed: l.pickConfirmed,
      shortPickReason: SHORTS.find((x) => uid(K.shipment, x.shipment) === s.id && s.lines[x.line]?.id === l.id)?.reason ?? null,
    })),
  );

interface WaveRow extends Wave {
  shipmentNos: number[];
}

function wave(n: number, groupBy: Wave["groupBy"], groupKey: string, shipmentNos: number[], status: Wave["status"], ago: number, picker: string | null): WaveRow {
  const list = shipmentNos.map(shipmentOf);
  return {
    id: uid(K_WAVE, n),
    waveNumber: `WV-${String(400 + n).padStart(6, "0")}`,
    legalEntityId: LEGAL_ENTITY.id,
    warehouseId: WH_FG.id,
    groupBy,
    groupKey,
    status,
    lineCount: list.reduce((s, x) => s + x.lines.length, 0),
    orderCount: new Set(list.map((x) => x.orderId)).size,
    createdAt: at(-ago),
    version: status === "CLOSED" ? 4 : picker ? 2 : 1,
    picker,
    assignedAt: picker ? at(-ago + 0.05) : null,
    closedAt: status === "CLOSED" ? at(-ago + 0.3) : null,
    shipments: list.map(waveShipment),
    shipmentNos,
  };
}

const waves = table<WaveRow>([
  wave(18, "CARRIER", "ODFL", [1], "OPEN", 0.1, null),
  wave(17, "CARRIER", "XPO", [8], "OPEN", 0.4, "Marcus Bell"),
  wave(15, "CARRIER", "SAIA", [16], "CLOSED", 1.2, "Luis Ortega"),
  wave(12, "CUSTOMER", "C-10055", [3], "CLOSED", 2.6, "Marcus Bell"),
  wave(9, "CARRIER", "SAIA", [6], "CLOSED", 9.4, "Luis Ortega"),
]);

const header = ({ shipments: _s, lines: _l, shipmentNos: _n, ...w }: WaveRow) => w;
const detail = (w: WaveRow): Wave => {
  const list = w.shipmentNos.map(shipmentOf);
  const { shipmentNos: _n, ...rest } = w;
  return { ...rest, shipments: list.map(waveShipment), lines: waveLines(list) };
};

route("GET", `${BASE}/waves`, ({ query }) => {
  const status = query.get("status");
  const picker = query.get("picker");
  const unassigned = query.get("unassigned") === "true";
  const wh = query.get("warehouseId");
  const limit = Number(query.get("limit") ?? 0);
  const rows = waves
    .all()
    .filter((w) => (!status || w.status === status) && (!picker || w.picker === picker) && (!unassigned || !w.picker) && (!wh || w.warehouseId === wh))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
    .map(header);
  return { items: limit > 0 ? rows.slice(0, limit) : rows };
});

route("GET", `${BASE}/waves/{id}`, ({ params }) => {
  const w = waves.get(params.id);
  return w ? detail(w) : reply.problem(404, "FUL-0404", "Wave not found");
});

route("POST", `${BASE}/waves/{id}:assign`, (req) => {
  const w = waves.get(req.params.id);
  if (!w) return reply.problem(404, "FUL-0404", "Wave not found");
  const v = ifMatch(req);
  if (v !== undefined && v !== w.version) return stale();
  if (w.status !== "OPEN") return reply.problem(409, "FUL-0054", "Only an open wave can be assigned");
  const picker = String((req.body as { picker?: string } | undefined)?.picker ?? USER.name).trim();
  return detail(waves.update(w.id, { picker, assignedAt: at(0), version: w.version + 1 })!);
});

route("POST", `${BASE}/waves/{id}:confirm-pick`, ({ params, body }) => {
  const w = waves.get(params.id);
  if (!w) return reply.problem(404, "FUL-0404", "Wave not found");
  const input = (body ?? {}) as { picks?: { shipmentLineId: string; picked: string; reason?: ShortPickReason }[]; pickRest?: boolean };
  let confirmed = 0;
  let short = 0;
  const touched: { id: string; shipmentNumber: string; status: Shipment["status"]; version: number; linesConfirmed: number }[] = [];
  for (const n of w.shipmentNos) {
    const s = shipmentOf(n);
    let count = 0;
    const lines = s.lines.map((l) => {
      if (l.pickConfirmed) return l;
      const pick = input.picks?.find((p) => p.shipmentLineId === l.id);
      if (!pick && !input.pickRest) return l;
      const qty = pick ? pick.picked : l.qtyPlanned;
      if (Number(qty) < Number(l.qtyPlanned)) short += 1;
      count += 1;
      return { ...l, qtyPicked: qty, pickConfirmed: true, ...(pick?.reason ? { shortPickReason: pick.reason } : {}) };
    });
    if (count === 0) continue;
    confirmed += count;
    const all = lines.every((l) => l.pickConfirmed);
    const next = shipments.update(s.id, { lines, status: all && (s.status === "RELEASED" || s.status === "PICKING") ? "PICKED" : s.status === "RELEASED" ? "PICKING" : s.status, version: s.version + 1 })!;
    touched.push({ id: next.id, shipmentNumber: next.shipmentNumber, status: next.status, version: next.version, linesConfirmed: count });
  }
  waves.update(w.id, { version: w.version + 1 });
  return { waveId: w.id, linesConfirmed: confirmed, shortLines: short, shipments: touched };
});

route("POST", `${BASE}/waves/{id}:close`, (req) => {
  const w = waves.get(req.params.id);
  if (!w) return reply.problem(404, "FUL-0404", "Wave not found");
  const v = ifMatch(req);
  if (v !== undefined && v !== w.version) return stale();
  const open = waveLines(w.shipmentNos.map(shipmentOf)).filter((l) => !l.pickConfirmed);
  if (open.length > 0) return reply.problem(409, "FUL-0055", `${open.length} ${open.length === 1 ? "line is" : "lines are"} not picked yet; confirm the picks before closing the wave.`);
  return detail(waves.update(w.id, { status: "CLOSED", closedAt: at(0), version: w.version + 1 })!);
});

// ---- Carrier manifests --------------------------------------------------------------------------------------

const tracking = (s: Shipment) => `${(s.carrierCode ?? "CARRIER").replace(/[^A-Z]/g, "").slice(0, 4)}${s.shipmentNumber.slice(-5)}7${String(s.version).padStart(2, "0")}`;

interface ManifestRow extends Manifest {
  shipmentNos: number[];
}

function manifest(n: number, carrier: string, pickup: number, shipmentNos: number[], status: Manifest["status"] = "CLOSED"): ManifestRow {
  const list = shipmentNos.map(shipmentOf);
  return {
    id: uid(K_MANIFEST, n),
    manifestNumber: `MF-${day(pickup).replace(/-/g, "").slice(2)}-${carrier.replace(/[^A-Z]/g, "").slice(0, 4)}`,
    status,
    legalEntityId: LEGAL_ENTITY.id,
    warehouseId: WH_FG.id,
    carrier,
    pickupDate: day(pickup),
    providerManifestId: `${carrier.replace(/[^A-Z]/g, "")}-${String(880000 + n * 371)}`,
    parcelCount: list.reduce((s, x) => s + Math.max(1, x.containers.length), 0),
    shipmentCount: list.length,
    parcels: list.map((s) => ({ shipmentId: s.id, shipmentNumber: s.shipmentNumber, orderId: s.orderId, trackingNumber: tracking(s), active: status === "CLOSED" })),
    version: status === "CLOSED" ? 1 : 2,
    createdAt: at(pickup - 0.2),
    cancelledAt: status === "CANCELLED" ? at(pickup - 0.1) : null,
    shipmentNos,
  };
}

const manifests = table<ManifestRow>([
  manifest(7, "SAIA", -9, [6]),
  manifest(6, "ODFL", -25, [7]),
  manifest(5, "SAIA", -37, [11]),
  manifest(4, "FEDEX-FRT", -48, [10]),
  manifest(3, "SAIA", -44, [12]),
  manifest(2, "ODFL", -73, [13]),
  manifest(1, "XPO", -88, [14]),
  manifest(8, "XPO", -2, [3], "CANCELLED"),
]);

const listed = ({ shipmentNos: _n, ...m }: ManifestRow): Manifest => ({ ...m, parcels: [] });
const full = ({ shipmentNos: _n, ...m }: ManifestRow): Manifest => m;

/** Labelled parcels on no active manifest: SO-024183's partial shipment went out after its manifest was withdrawn. */
function candidates(): ManifestCandidate[] {
  const active = new Set(manifests.all().filter((m) => m.status === "CLOSED").flatMap((m) => m.shipmentNos));
  return [3, 16, 8]
    .filter((n) => !active.has(n))
    .map(shipmentOf)
    .filter((s) => s && s.containers.length > 0)
    .map((s) => ({
      shipmentId: s.id,
      shipmentNumber: s.shipmentNumber,
      legalEntityId: s.legalEntityId,
      warehouseId: s.warehouseId,
      orderId: s.orderId,
      shipmentStatus: s.status,
      carrier: s.carrierCode ?? "",
      trackingNumber: tracking(s),
      labelledAt: at(-0.3),
      ready: s.status === "SHIPPED" || s.status === "PARTIALLY_SHIPPED",
    }));
}

route("GET", `${BASE}/manifests`, ({ query }) => {
  const status = query.get("status");
  const carrier = query.get("carrier");
  const wh = query.get("warehouseId");
  const items = manifests
    .all()
    .filter((m) => (!status || m.status === status) && (!carrier || m.carrier.toLowerCase() === carrier.toLowerCase()) && (!wh || m.warehouseId === wh))
    .sort((a, b) => b.pickupDate.localeCompare(a.pickupDate))
    .map(listed);
  return { items };
});

route("GET", `${BASE}/manifests/{id}`, ({ params }) => {
  const m = manifests.get(params.id);
  return m ? full(m) : reply.problem(404, "FUL-0404", "Manifest not found");
});

route("GET", `${BASE}/manifest-candidates`, () => ({ items: candidates() }));

route("POST", `${BASE}/manifests`, ({ body }) => {
  const input = body as ManifestInput;
  const ready = candidates().filter((c) => c.ready && c.carrier.toLowerCase() === input.carrier.toLowerCase() && c.warehouseId === input.warehouseId);
  if (ready.length === 0) return reply.problem(409, "FUL-0076", "No shipped parcel of this carrier is waiting in the warehouse");
  const nos = ready.map((c) => [...Array(20).keys()].find((n) => shipments.get(uid(K.shipment, n))?.id === c.shipmentId)!).filter(Boolean);
  const n = 20 + manifests.all().length;
  const made = { ...manifest(n, input.carrier.toUpperCase(), 0, nos), pickupDate: input.pickupDate, providerManifestId: input.providerManifestId || null, createdAt: at(0) };
  manifests.insert(made);
  return reply.created(full(made));
});

route("POST", `${BASE}/manifests/{id}:cancel`, (req) => {
  const m = manifests.get(req.params.id);
  if (!m) return reply.problem(404, "FUL-0404", "Manifest not found");
  const v = ifMatch(req);
  if (v !== undefined && v !== m.version) return stale();
  if (m.status !== "CLOSED") return reply.problem(409, "FUL-0078", "Only a closed manifest can be withdrawn");
  const next = manifests.update(m.id, { status: "CANCELLED", cancelledAt: at(0), version: m.version + 1, parcels: m.parcels.map((p) => ({ ...p, active: false })) })!;
  return full(next);
});
