// Warehouse service (/api/v1/warehouse) for the demo: dock doors and their appointments, and ASN receiving by pallet.
// Inbound appointments and ASNs carry the purchase orders of purchasing.ts; outbound ones the shipments of the
// order to cash model. A received pallet gets its put-away work in the directed work list of warehouse.ts.
import type { Appointment, AppointmentInput, AsnInput, DockDoor, Receipt, ReceiptLine, ReceiptSummary, Work } from "../../../modules/warehouse/api/client";
import { USER, WH_DAL, WH_FG, WH_RAW, WH_WIP, itemByNumber, type Warehouse } from "../../company";
import { reply, route } from "../../backend";
import { at, day, dec, table, uid } from "../../store";
import { locationId, workTable } from "./warehouse";

const BASE = "/api/v1/warehouse";
const K_DOOR = 426;
const K_APPT = 427;
const K_ASN = 428;

// ---- Doors and appointments ---------------------------------------------------------------------------------

let doorSeq = 0;
const door = (wh: Warehouse, code: string): DockDoor => ({ id: uid(K_DOOR, ++doorSeq), warehouseId: wh.id, code, createdAt: at(-400) });
const doors = table<DockDoor>([
  door(WH_RAW, "R1"),
  door(WH_RAW, "R2"),
  door(WH_RAW, "R3"),
  door(WH_FG, "S1"),
  door(WH_FG, "S2"),
  door(WH_FG, "S3"),
  door(WH_DAL, "D1"),
  door(WH_DAL, "D2"),
  door(WH_WIP, "W1"),
]);
const doorOf = (wh: Warehouse, code: string) => doors.all().find((d) => d.warehouseId === wh.id && d.code === code)!;

/** A UTC instant on day `offset` at hh:mm, on the 15-minute grid. */
const instant = (offset: number, hhmm: string) => `${day(offset)}T${hhmm}:00Z`;

let apptSeq = 0;
function appt(wh: Warehouse, code: string, direction: Appointment["direction"], reference: string, offset: number, start: string, end: string): Appointment {
  const d = doorOf(wh, code);
  return { id: uid(K_APPT, ++apptSeq), warehouseId: wh.id, doorId: d.id, doorCode: d.code, direction, reference, start: instant(offset, start), end: instant(offset, end), createdAt: at(offset - 2) };
}

// Plant hours are 06:00 to 16:00 Central; the times below are UTC.
const appointments = table<Appointment>([
  ...[-1, 0, 1, 2, 3, 4].flatMap((o) => [
    ...(o === 0
      ? [
          appt(WH_RAW, "R1", "INBOUND", "PO-048204 Keystone Seals, 120 seal kits (Estes Express)", 0, "13:00", "14:00"),
          appt(WH_RAW, "R2", "INBOUND", "PO-048211 Riverbend Steel, 350 ft SS316 bar (flatbed)", 0, "14:30", "16:00"),
          appt(WH_RAW, "R1", "INBOUND", "PO-048202 Volta Motor Works, 8 motors 15 HP (XPO)", 0, "17:00", "18:00"),
          appt(WH_RAW, "R3", "OUTBOUND", "RTV-0042 return of dented motors to Volta Motor Works", 0, "19:30", "20:00"),
          appt(WH_RAW, "R2", "INBOUND", "PO-048201 Ironbridge Foundry, castings (Old Dominion)", 0, "20:30", "21:30"),
          appt(WH_FG, "S1", "OUTBOUND", "SHP-004127 SO-024183 Summit Mining Supply (XPO)", 0, "15:00", "16:00"),
          appt(WH_FG, "S2", "OUTBOUND", "TO-000215 Dallas DC replenishment (company truck)", 0, "16:30", "17:30"),
          appt(WH_DAL, "D1", "INBOUND", "TO-000215 from Cedar Rapids, CP-200 and seal kits", 0, "18:00", "19:00"),
        ]
      : []),
    ...(o === 1 ? [appt(WH_RAW, "R1", "INBOUND", "PO-048208 Ironbridge Foundry, 80 volute castings", 1, "14:00", "15:30"), appt(WH_FG, "S1", "OUTBOUND", "SHP-004152 SO-024196 Harlan Agricultural Co-op (Saia)", 1, "15:00", "16:00")] : []),
    ...(o === 2 ? [appt(WH_FG, "S2", "OUTBOUND", "SHP-004127 SO-024188 Summit Mining Supply (XPO)", 2, "19:00", "20:00"), appt(WH_RAW, "R2", "INBOUND", "SA-2026-022 Riverbend Steel, monthly bar release", 2, "13:30", "15:00")] : []),
    ...(o === 3 ? [appt(WH_RAW, "R1", "INBOUND", "SA-2026-016 Keystone Seals, weekly release", 3, "14:00", "14:45")] : []),
    ...(o === 4 ? [appt(WH_FG, "S1", "OUTBOUND", "SHP-004106 SO-024181 Bluewater Municipal Utilities (Old Dominion)", 4, "15:00", "16:30")] : []),
    ...(o === -1 ? [appt(WH_RAW, "R1", "INBOUND", "PO-048204 Keystone Seals, 180 seal kits", -1, "14:00", "15:00"), appt(WH_FG, "S1", "OUTBOUND", "SHP-004112 SO-024183 Summit Mining Supply (XPO)", -1, "19:00", "20:00")] : []),
  ]),
]);

route("GET", `${BASE}/dock-doors`, ({ query }) => {
  const wh = (query.get("warehouseId") ?? "").toLowerCase();
  return { items: doors.all().filter((d) => !wh || d.warehouseId === wh).sort((a, b) => a.code.localeCompare(b.code)) };
});

route("POST", `${BASE}/dock-doors`, ({ body }) => {
  const input = body as { warehouseId: string; code: string };
  const code = input.code.trim().toUpperCase();
  if (doors.all().some((d) => d.warehouseId === input.warehouseId && d.code === code)) return reply.problem(409, "WMS-DUPLICATE", `Door ${code} already exists in this warehouse`);
  return reply.created(doors.insert({ id: uid(K_DOOR, 100 + doors.all().length), warehouseId: input.warehouseId, code, createdAt: at(0) }));
});

route("GET", `${BASE}/dock-appointments`, ({ query }) => {
  const wh = (query.get("warehouseId") ?? "").toLowerCase();
  const doorId = query.get("doorId");
  const from = query.get("from");
  const to = query.get("to");
  const items = appointments
    .all()
    .filter((a) => (!wh || a.warehouseId === wh) && (!doorId || a.doorId === doorId) && (!from || a.end > from) && (!to || a.start < to))
    .sort((a, b) => a.start.localeCompare(b.start) || a.doorCode.localeCompare(b.doorCode));
  return { items };
});

route("POST", `${BASE}/dock-appointments`, ({ body }) => {
  const input = body as AppointmentInput;
  const d = doors.get(input.doorId);
  if (!d) return reply.problem(422, "WMS-VALIDATION", "Choose a door", { doorId: "Choose a door." });
  if (!(input.start < input.end)) return reply.problem(422, "WMS-VALIDATION", "The end must be after the start", { end: "The end must be after the start." });
  const clash = appointments.all().find((a) => a.doorId === d.id && a.start < input.end && a.end > input.start);
  if (clash) return reply.problem(409, "WMS-DOOR-BOOKED", `Door ${d.code} is booked from ${clash.start.slice(11, 16)} to ${clash.end.slice(11, 16)} UTC for ${clash.reference}`);
  const made: Appointment = { id: uid(K_APPT, 100 + appointments.all().length), warehouseId: input.warehouseId, doorId: d.id, doorCode: d.code, direction: input.direction, reference: input.reference, start: input.start, end: input.end, createdAt: at(0) };
  return reply.created(appointments.insert(made));
});

// ---- ASN receiving ------------------------------------------------------------------------------------------

function pallet(lineNo: number, number: string, lp: number, po: string, expected: number, received?: number, ago = 0): ReceiptLine {
  const it = itemByNumber(number);
  const got = received ?? 0;
  const variance = got - expected;
  const done = received !== undefined;
  return {
    lineNo,
    itemId: number,
    lpId: `LP${String(lp).padStart(8, "0")}`,
    poLineRef: po,
    expectedQty: dec(expected, 0),
    weight: dec(expected * (it.kind === "raw" ? 1.2 : 4.5), 1),
    volume: dec(expected * 0.01, 2),
    status: done ? "RECEIVED" : "EXPECTED",
    receivedQty: dec(got, 0),
    varianceQty: done ? dec(variance, 0) : "0",
    variancePct: done ? dec((variance / expected) * 100) : "0.00",
    discrepancy: done && Math.abs(variance / expected) > 0.02,
    reason: done && variance !== 0 ? (variance < 0 ? "Short shipped, vendor notified" : "Over-shipped within tolerance") : undefined,
    receivedBy: done ? "Luis Ortega" : undefined,
    receivedAt: done ? at(-ago) : undefined,
  };
}

function asn(n: number, ref: string, ago: number, lines: ReceiptLine[], wh: Warehouse = WH_RAW): Receipt {
  const received = lines.filter((l) => l.status === "RECEIVED").length;
  return {
    id: uid(K_ASN, n),
    warehouseId: wh.id,
    asnRef: ref,
    tolerancePct: "2.00",
    status: received === 0 ? "OPEN" : received === lines.length ? "RECEIVED" : "PARTIALLY_RECEIVED",
    version: 1 + received,
    createdAt: at(-ago),
    updatedAt: at(-ago + 0.1 * received),
    lines,
  };
}

const asns = table<Receipt>([
  asn(1, "KSG-ASN-558213", 0.4, [pallet(1, "SEAL-MK-125", 30558213, "PO-048204/1", 60), pallet(2, "SEAL-MK-125", 30558214, "PO-048204/1", 60)]),
  asn(2, "RBS-ASN-90417", 0.9, [pallet(1, "RM-SS316-BAR", 40090417, "PO-048211/1", 250, 250, 0.7), pallet(2, "RM-SS316-BAR", 40090418, "PO-048211/1", 100)]),
  asn(3, "VMW-ASN-121044", 0.2, [pallet(1, "MTR-15HP-TEFC", 50121044, "PO-048202/1", 4), pallet(2, "MTR-15HP-TEFC", 50121045, "PO-048202/1", 4)]),
  asn(4, "KSG-ASN-557640", 2.1, [pallet(1, "SEAL-MK-125", 30557640, "PO-048204/1", 90, 90, 2), pallet(2, "SEAL-MK-125", 30557641, "PO-048204/1", 90, 90, 2)]),
  asn(5, "PBC-ASN-33018", 4.2, [pallet(1, "BRG-6206", 60033018, "PO-048205/1", 500, 500, 4), pallet(2, "BRG-6206", 60033019, "PO-048205/1", 500, 488, 4)]),
  asn(6, "VMW-ASN-120771", 10.3, [pallet(1, "MTR-5HP-TEFC", 50120771, "PO-048203/1", 12, 12, 10), pallet(2, "MTR-5HP-TEFC", 50120772, "PO-048203/1", 12, 12, 10)]),
  asn(7, "IBF-ASN-77120", 37, [pallet(1, "CST-IMP-06", 70077120, "PO-048201/1", 100, 100, 36), pallet(2, "CST-VOL-04", 70077121, "PO-048201/2", 60, 58, 36)]),
]);

const asnSummary = ({ lines, version: _v, ...r }: Receipt): ReceiptSummary => ({
  ...r,
  lines: lines.length,
  received: lines.filter((l) => l.status === "RECEIVED").length,
  discrepancies: lines.filter((l) => l.discrepancy).length,
});

route("GET", `${BASE}/receipts`, ({ query }) => {
  const wh = (query.get("warehouseId") ?? "").toLowerCase();
  const status = query.get("status");
  const limit = Number(query.get("limit") ?? 50) || 50;
  const items = asns
    .all()
    .filter((r) => (!wh || r.warehouseId === wh) && (!status || r.status === status))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
    .slice(0, limit)
    .map(asnSummary);
  return { items };
});

route("GET", `${BASE}/receipts/{id}`, ({ params }) => {
  const r = asns.get(params.id);
  return r ? reply.versioned(r, r.version) : reply.problem(404, "WMS-NOT-FOUND", "Receipt not found");
});

route("POST", `${BASE}/receipts:from-asn`, ({ body }) => {
  const input = body as AsnInput;
  if (asns.all().some((r) => r.warehouseId === input.warehouseId && r.asnRef === input.asnRef)) return reply.problem(409, "WMS-DUPLICATE", `ASN ${input.asnRef} is already registered in this warehouse`);
  const n = 50 + asns.all().length;
  const made: Receipt = {
    id: uid(K_ASN, n),
    warehouseId: input.warehouseId,
    asnRef: input.asnRef,
    tolerancePct: input.tolerancePct || "2.00",
    status: "OPEN",
    version: 1,
    createdAt: at(0),
    updatedAt: at(0),
    lines: input.lines.map((l) => ({ lineNo: l.lineNo, itemId: l.itemId, lpId: l.lpId, poLineRef: l.poLineRef, expectedQty: l.expectedQty, weight: l.weight ?? "0", volume: l.volume ?? "0", status: "EXPECTED", receivedQty: "0", varianceQty: "0", variancePct: "0.00", discrepancy: false })),
  };
  return reply.versioned(asns.insert(made), 1, 201);
});

route("POST", `${BASE}/receipts/{id}/lines/{n}:receive`, ({ params, body }) => {
  const r = asns.get(params.id);
  if (!r) return reply.problem(404, "WMS-NOT-FOUND", "Receipt not found");
  const lineNo = Number(params.n);
  const line = r.lines.find((l) => l.lineNo === lineNo);
  if (!line) return reply.problem(404, "WMS-NOT-FOUND", "Pallet not found");
  if (line.status === "RECEIVED") return reply.problem(409, "WMS-ALREADY-RECEIVED", "This pallet is already received");
  const qty = Number((body as { qty?: string } | undefined)?.qty ?? line.expectedQty);
  const expected = Number(line.expectedQty);
  const variance = qty - expected;
  const pct = (variance / expected) * 100;
  const discrepancy = Math.abs(pct) > Number(r.tolerancePct);
  const it = itemByNumber(line.itemId);
  const work: Work = workTable.insert({
    id: uid(310, 500 + workTable.all().length),
    warehouseId: r.warehouseId,
    workType: "PURCHASE_PUTAWAY",
    status: "OPEN",
    priority: 20,
    version: 1,
    createdAt: at(0),
    updatedAt: at(0),
    lines: [{ lineNo: 1, itemId: line.itemId, direction: "PUT", qty: dec(qty, 0), confirmedQty: "0", weight: line.weight, volume: line.volume, lps: 1, toLocationId: locationId(it.number), status: "OPEN" }],
  });
  const lines = r.lines.map((l) =>
    l.lineNo === lineNo
      ? { ...l, status: "RECEIVED" as const, receivedQty: dec(qty, 0), varianceQty: dec(variance, 0), variancePct: dec(pct), discrepancy, workId: work.id, receivedBy: USER.name, receivedAt: at(0), reason: discrepancy ? "Quantity outside the ASN tolerance" : undefined }
      : l,
  );
  const received = lines.filter((l) => l.status === "RECEIVED").length;
  const next = asns.update(r.id, { lines, status: received === lines.length ? "RECEIVED" : "PARTIALLY_RECEIVED", version: r.version + 1, updatedAt: at(0) })!;
  const warnings = discrepancy ? [{ code: "WMS-VARIANCE", detail: `Received ${qty} against ${expected} expected (${dec(pct)} %), outside the ${r.tolerancePct} % tolerance.` }] : [];
  return reply.versioned({ receipt: next, work, traces: [{ directive: "Put away to the item's home bin", blocked: false, trace: [] }], warnings }, next.version);
});
