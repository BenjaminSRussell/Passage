// Purchasing service (/api/v1/procurement) for the demo, beyond requisitions and orders in purchasing.ts: procurement
// catalogs, the changes planning and drop-ship services asked of purchase orders, requisition release runs and
// scheduling agreements with their releases. Vendors, items and purchase orders are the ones purchasing.ts seeds.
import type {
  Catalog,
  CatalogEntry,
  CatalogEntryInput,
  CatalogHit,
  CatalogInput,
  CatalogSummary,
  InboundChange,
  PurchaseOrderLine,
  ReleaseRun,
  ReleaseSkip,
  ScheduleLine,
  ScheduleLineInput,
  ScheduleZone,
  SchedulingAgreement,
  SchedulingAgreementRow,
  SchedulingRelease,
  SchedulingReleaseLine,
} from "../../../modules/purchasing/api/client";
import { ITEMS, LEGAL_ENTITY, PLANT, USER, VENDORS, itemByNumber } from "../../company";
import { reply, route } from "../../backend";
import { at, day, dec, table, uid } from "../../store";
import { newPurchaseLine, newPurchaseOrder, purchaseOrders, requisitions } from "./purchasing";

const BASE = "/api/v1/procurement";
const K_CATALOG = 420;
const K_ENTRY = 421;
const K_INBOUND = 422;
const K_RUN = 423;
const K_SA = 424;
const K_SA_RELEASE = 425;

const V = {
  foundry: VENDORS[0],
  motors: VENDORS[1],
  seals: VENDORS[2],
  fasteners: VENDORS[3],
  steel: VENDORS[4],
  bearings: VENDORS[5],
};
const poId = (n: number) => uid(302, n);
const poNumber = (n: number) => `PO-${String(48200 + n).padStart(6, "0")}`;
const vendorIndex = (id: string) => Math.max(0, VENDORS.findIndex((v) => v.id === id));

// ---- Catalogs -----------------------------------------------------------------------------------------------

let entrySeq = 0;
function entry(vendor: (typeof VENDORS)[number], partNo: string, name: string, category: string, unit: string, price: number, lead: number, extra: Partial<CatalogEntryInput> = {}): CatalogEntry {
  const n = ++entrySeq;
  return { id: uid(K_ENTRY, n), entryNo: 0, vendorId: vendor.id, vendorPartNo: partNo, name, category, unit, unitPrice: dec(price), currency: "USD", leadTimeDays: lead, ...extra };
}
const fromItem = (number: string, vendor: (typeof VENDORS)[number], partNo: string, extra: Partial<CatalogEntryInput> = {}) => {
  const it = itemByNumber(number);
  return entry(vendor, partNo, it.name, number.startsWith("SEAL") ? "Seals" : number.startsWith("BRG") ? "Bearings" : number.startsWith("FST") ? "Fasteners" : number.startsWith("MTR") ? "Motors" : number.startsWith("CST") ? "Castings" : "Raw materials", it.unit, it.cost, it.leadTimeDays, { productId: it.id, preferred: true, ...extra });
};

function catalog(n: number, name: string, status: Catalog["status"], validFrom: number, validTo: number | undefined, entries: CatalogEntry[], description: string): Catalog {
  const numbered = entries.map((e, i) => ({ ...e, entryNo: i + 1 }));
  return {
    id: uid(K_CATALOG, n),
    number: `CAT-${String(n).padStart(4, "0")}`,
    legalEntityId: LEGAL_ENTITY.id,
    name,
    description,
    status,
    validFrom: day(validFrom),
    validTo: validTo === undefined ? undefined : day(validTo),
    orgUnitIds: [],
    version: status === "DRAFT" ? 1 : 3,
    entryCount: numbered.length,
    entries: numbered,
    createdAt: at(validFrom - 14),
    updatedAt: at(Math.min(-1, validFrom)),
  };
}

const catalogs = table<Catalog>([
  catalog(
    11,
    "Shop and MRO supplies 2026",
    "PUBLISHED",
    -310,
    55,
    [
      fromItem("FST-KIT-A", V.fasteners, "AFS-KIT-PA-316"),
      entry(V.fasteners, "AFS-HHCS-0516", "Hex cap screw 5/16-18 x 1, stainless, box of 100", "Fasteners", "box", 21.4, 3),
      entry(V.fasteners, "AFS-WSH-0516", "Flat washer 5/16, stainless, box of 250", "Fasteners", "box", 14.9, 3),
      entry(V.fasteners, "AFS-GLV-CUT5", "Cut-resistant gloves, level A5, pair", "Safety", "pr", 8.75, 2),
      entry(V.fasteners, "AFS-SGL-CLR", "Safety glasses, clear anti-fog", "Safety", "ea", 4.2, 2),
      fromItem("BRG-6206", V.bearings, "PBC-6206-2RS"),
      entry(V.bearings, "PBC-GRS-EP2", "Bearing grease EP2, 14 oz cartridge", "Lubricants", "ea", 9.6, 5),
      fromItem("SEAL-MK-125", V.seals, "KSG-MS-125-SC"),
      entry(V.seals, "KSG-OR-KIT-V", "O-ring kit, Viton, 382 pieces", "Seals", "kit", 112, 7),
      entry(V.steel, "RBS-PLT-025", "Steel plate A36, 1/4 in, 4 x 8 ft", "Raw materials", "ea", 268, 6),
    ],
    "Fasteners, seals, bearings and safety supplies that requesters order without a quote.",
  ),
  catalog(
    12,
    "Motors and drives",
    "PUBLISHED",
    -150,
    215,
    [
      fromItem("MTR-5HP-TEFC", V.motors, "VMW-T5-1800"),
      fromItem("MTR-15HP-TEFC", V.motors, "VMW-T15-1800"),
      entry(V.motors, "VMW-VFD-7.5", "Variable frequency drive, 7.5 HP, 460 V", "Drives", "ea", 1385, 14),
      entry(V.motors, "VMW-VFD-20", "Variable frequency drive, 20 HP, 460 V", "Drives", "ea", 2960, 21),
      entry(V.motors, "VMW-CPL-L100", "Jaw coupling L100 with spider", "Couplings", "ea", 46.5, 7),
    ],
    "Volta Motor Works list prices under the 2026 motor agreement.",
  ),
  catalog(
    13,
    "Ironbridge castings 2027 price file",
    "DRAFT",
    51,
    416,
    [
      fromItem("CST-VOL-04", V.foundry, "IBF-V4-DI", { unitPrice: "176.40" }),
      fromItem("CST-IMP-06", V.foundry, "IBF-I6-BRZ", { unitPrice: "101.00" }),
      entry(V.foundry, "IBF-V6-DI", "Volute casting, ductile iron, 6 in", "Castings", "ea", 244, 35),
    ],
    "Next year's prices from Ironbridge Foundry, waiting for the buyer's review before it is published.",
  ),
  catalog(
    9,
    "Shop and MRO supplies 2025",
    "RETIRED",
    -680,
    -311,
    [fromItem("FST-KIT-A", V.fasteners, "AFS-KIT-PA-316", { unitPrice: "12.10" }), fromItem("BRG-6206", V.bearings, "PBC-6206-2RS", { unitPrice: "7.05" })],
    "Replaced by the 2026 catalog.",
  ),
]);

const catalogSummary = (c: Catalog): CatalogSummary => ({ id: c.id, number: c.number, legalEntityId: c.legalEntityId, name: c.name, status: c.status, validFrom: c.validFrom, validTo: c.validTo, entryCount: c.entryCount, version: c.version, createdAt: c.createdAt });

route("GET", `${BASE}/catalogs`, ({ query }) => {
  const status = query.get("status");
  return { items: catalogs.all().filter((c) => !status || c.status === status).sort((a, b) => b.number.localeCompare(a.number)).map(catalogSummary) };
});

route("POST", `${BASE}/catalogs`, ({ body }) => {
  const input = body as CatalogInput;
  const n = 20 + catalogs.all().length;
  const entries = (input.entries ?? []).map((e, i) => ({ ...e, id: uid(K_ENTRY, 500 + n * 50 + i), entryNo: i + 1 }));
  const made: Catalog = { ...catalog(n, input.name, "DRAFT", 0, undefined, entries, input.description ?? ""), validFrom: input.validFrom, validTo: input.validTo, orgUnitIds: input.orgUnitIds ?? [], createdAt: at(0), updatedAt: at(0) };
  catalogs.insert(made);
  return reply.versioned(made, made.version, 201);
});

route("GET", `${BASE}/catalogs/{id}/entries`, ({ params }) => {
  const c = catalogs.get(params.id);
  return c ? { items: c.entries } : reply.problem(404, "PRC-CAT-NOT-FOUND", "Catalog not found");
});

route("POST", `${BASE}/catalogs/{id}/entries`, ({ params, body }) => {
  const c = catalogs.get(params.id);
  if (!c) return reply.problem(404, "PRC-CAT-NOT-FOUND", "Catalog not found");
  const added = ((body as { entries: CatalogEntryInput[] }).entries ?? []).map((e, i) => ({ ...e, id: uid(K_ENTRY, 900 + c.entries.length * 7 + i), entryNo: c.entries.length + i + 1 }));
  const next = catalogs.update(c.id, { entries: [...c.entries, ...added], entryCount: c.entries.length + added.length, version: c.version + 1, updatedAt: at(0) })!;
  return reply.versioned(next, next.version);
});

route("DELETE", `${BASE}/catalogs/{id}/entries/{entryId}`, ({ params }) => {
  const c = catalogs.get(params.id);
  if (!c) return reply.problem(404, "PRC-CAT-NOT-FOUND", "Catalog not found");
  const entries = c.entries.filter((e) => e.id !== params.entryId);
  const next = catalogs.update(c.id, { entries, entryCount: entries.length, version: c.version + 1, updatedAt: at(0) })!;
  return reply.versioned(next, next.version);
});

route("POST", `${BASE}/catalogs/{id}:{action}`, ({ params }) => {
  const c = catalogs.get(params.id);
  if (!c) return reply.problem(404, "PRC-CAT-NOT-FOUND", "Catalog not found");
  if (params.action === "publish" && c.status !== "DRAFT") return reply.problem(409, "PRC-CAT-002", "Only a draft catalog can be published");
  if (params.action !== "publish" && params.action !== "retire") return reply.problem(404, "PRC-CAT-404", `Unknown action ${params.action}`);
  const next = catalogs.update(c.id, { status: params.action === "publish" ? "PUBLISHED" : "RETIRED", version: c.version + 1, updatedAt: at(0) })!;
  return reply.versioned(next, next.version);
});

route("GET", `${BASE}/catalogs/{id}`, ({ params }) => {
  const c = catalogs.get(params.id);
  return c ? reply.versioned(c, c.version) : reply.problem(404, "PRC-CAT-NOT-FOUND", "Catalog not found");
});

route("GET", `${BASE}/catalog-entries`, ({ query }) => {
  const q = (query.get("q") ?? "").toLowerCase();
  const category = query.get("category");
  const today = day(0);
  const items: CatalogHit[] = catalogs
    .all()
    .filter((c) => c.status === "PUBLISHED" && c.validFrom <= today && (!c.validTo || c.validTo >= today))
    .flatMap((c) => c.entries.map((e) => ({ ...e, catalogId: c.id, catalogNumber: c.number, catalogName: c.name })))
    .filter((e) => (!q || e.name.toLowerCase().includes(q) || (e.vendorPartNo ?? "").toLowerCase().includes(q)) && (!category || e.category === category));
  return { items };
});

route("GET", `${BASE}/catalog-imports`, () => ({ items: [] }));

// ---- Changes from other services ----------------------------------------------------------------------------

function inbound(n: number, ago: number, change: Partial<InboundChange> & Pick<InboundChange, "source" | "action" | "status">): InboundChange {
  return {
    id: uid(K_INBOUND, n),
    legalEntityId: LEGAL_ENTITY.id,
    sourceKey: change.source === "PLANNING_MESSAGE" ? `PM-${String(88100 + n * 7)}` : `DS-${String(3300 + n)}`,
    eventId: `evt-${change.source === "PLANNING_MESSAGE" ? "planning" : "dropship"}-${String(510000 + n * 113)}`,
    reapprovalRequired: false,
    createdAt: at(-ago),
    attempts: 1,
    ...change,
  };
}
const target = (po: number, lineNo: number, number: string) => ({ purchaseOrderId: poId(po), poNumber: poNumber(po), lineNo, productId: itemByNumber(number).id, warehouseId: uid(3, 1) });

const inboundChanges = table<InboundChange>([
  inbound(1, 0.2, {
    source: "PLANNING_MESSAGE",
    action: "reschedule-out",
    status: "APPLIED",
    ...target(2, 1, "MTR-15HP-TEFC"),
    requestedDate: day(16),
    dateBefore: day(9),
    dateAfter: day(16),
    versionNo: 2,
    requestedBy: "Priya Natarajan (planner)",
    note: "CP-400 build for SO-024184 moved a week; the motors are not needed until then.",
  }),
  inbound(2, 0.6, {
    source: "PLANNING_MESSAGE",
    action: "decrease",
    status: "APPLIED",
    ...target(10, 1, "MTR-5HP-TEFC"),
    requestedQty: "16",
    qtyBefore: "20",
    qtyAfter: "16",
    versionNo: 2,
    reapprovalRequired: true,
    requestedBy: "Priya Natarajan (planner)",
    note: "Forecast for CP-200 trimmed after the October review.",
  }),
  inbound(3, 1.4, {
    source: "PLANNING_MESSAGE",
    action: "cancel",
    status: "REFUSED",
    ...target(1, 2, "CST-VOL-04"),
    reasonCode: "PRC-INB-004",
    reason: "The line is confirmed by Ironbridge Foundry and inside its frozen window; a confirmed line can be rescheduled, not cancelled.",
    requestedQty: "0",
    requestedBy: "Priya Natarajan (planner)",
  }),
  inbound(4, 2.1, {
    source: "PLANNING_MESSAGE",
    action: "reschedule-in",
    status: "REFUSED",
    ...target(4, 1, "SEAL-MK-125"),
    reasonCode: "PRC-INB-007",
    reason: "180 of 300 are already received; the open 120 are on the truck that Keystone booked for today.",
    requestedDate: day(1),
    requestedBy: "Priya Natarajan (planner)",
  }),
  inbound(5, 3.5, {
    source: "PLANNING_MESSAGE",
    action: "increase",
    status: "REFUSED",
    ...target(11, 1, "RM-SS316-BAR"),
    reasonCode: "PRC-INB-002",
    reason: "The new quantity is above the order's approval limit and needs a buyer's change.",
    requestedQty: "750",
    requestedBy: "Priya Natarajan (planner)",
    resolution: "ACKNOWLEDGED",
    resolvedBy: USER.name,
    resolvedAt: at(-3.1),
    resolutionNote: "Raised a separate order for the extra 150 ft with Riverbend.",
  }),
  inbound(6, 5.2, { source: "DROPSHIP_CLOSED", action: "close-remainder", status: "APPLIED", ...target(8, 1, "CST-VOL-04"), qtyBefore: "80", qtyAfter: "80", requestedBy: "fulfillment", note: "Drop-ship to Coastal Desalination closed complete." }),
  inbound(7, 9, { source: "DROPSHIP_CANCELLED", action: "close-remainder", status: "IGNORED", ...target(9, 1, "SEAL-MK-125"), reason: "The purchase order was already cancelled; nothing to change.", requestedBy: "fulfillment" }),
  inbound(8, 12.4, {
    source: "PLANNING_MESSAGE",
    action: "reschedule-out",
    status: "APPLIED",
    ...target(1, 1, "CST-IMP-06"),
    requestedDate: day(-6),
    dateBefore: day(-13),
    dateAfter: day(-6),
    versionNo: 2,
    attempts: 2,
    resolution: "RETRIED",
    resolvedBy: "Dana Whitfield",
    resolvedAt: at(-12),
    resolutionNote: "Retried after Ironbridge confirmed the new date.",
    requestedBy: "Priya Natarajan (planner)",
  }),
]);

route("GET", `${BASE}/inbound-changes`, ({ query }) => {
  const source = query.get("source");
  const status = query.get("status");
  const resolution = query.get("resolution");
  const purchaseOrderId = query.get("purchaseOrderId");
  const items = inboundChanges
    .all()
    .filter((c) => (!source || c.source === source) && (!status || c.status === status) && (!purchaseOrderId || c.purchaseOrderId === purchaseOrderId))
    .filter((c) => !resolution || (resolution === "NONE" ? !c.resolution : c.resolution === resolution))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  return { items };
});

route("GET", `${BASE}/inbound-changes/{id}`, ({ params }) => {
  const c = inboundChanges.get(params.id);
  return c ? reply.versioned(c, c.attempts ?? 1) : reply.problem(404, "PRC-INB-NOT-FOUND", "Change not found");
});

route("POST", `${BASE}/inbound-changes/{id}:{action}`, ({ params, body }) => {
  const c = inboundChanges.get(params.id);
  if (!c) return reply.problem(404, "PRC-INB-NOT-FOUND", "Change not found");
  if (c.status !== "REFUSED" || c.resolution) return reply.problem(409, "PRC-INB-009", "Only a refused change nobody settled can be settled");
  const note = (body as { note?: string } | undefined)?.note;
  if (params.action === "acknowledge") {
    return inboundChanges.update(c.id, { resolution: "ACKNOWLEDGED", resolvedBy: USER.name, resolvedAt: at(0), resolutionNote: note });
  }
  if (params.action === "retry") {
    // The reschedule of the seal order goes through once the truck is in; the others are refused again.
    const passes = c.action.startsWith("reschedule");
    return inboundChanges.update(c.id, {
      attempts: (c.attempts ?? 1) + 1,
      resolution: "RETRIED",
      resolvedBy: USER.name,
      resolvedAt: at(0),
      resolutionNote: note,
      ...(passes ? { status: "APPLIED" as const, dateBefore: day(3), dateAfter: c.requestedDate, versionNo: 2 } : {}),
    });
  }
  return reply.problem(404, "PRC-INB-404", `Unknown action ${params.action}`);
});

// ---- Requisition release runs -------------------------------------------------------------------------------

function run(n: number, status: ReleaseRun["status"], ago: number, orders: ReleaseRun["purchaseOrders"], skipped: ReleaseSkip[], considered: number, extra: Partial<ReleaseRun> = {}): ReleaseRun {
  return {
    id: uid(K_RUN, n),
    legalEntityId: LEGAL_ENTITY.id,
    status,
    windowDays: 30,
    maxLines: 200,
    linesConsidered: considered,
    linesReleased: considered - skipped.length,
    linesSkipped: skipped.length,
    purchaseOrders: orders,
    skipped,
    warnings: [],
    requestedBy: USER.name,
    createdAt: at(-ago),
    ...extra,
  };
}
const releasedOrder = (n: number) => {
  const p = purchaseOrders.get(poId(n))!;
  return { purchaseOrderId: p.id, number: p.number, vendorId: p.vendorId, currency: p.currency, total: p.total, lineCount: p.lines.length };
};
const noVendor = (req: number, lineNo: number): ReleaseSkip => ({ requisitionLineId: requisitions.get(uid(300, req))?.lines[lineNo - 1]?.id ?? "", requisitionNumber: `REQ-${String(7100 + req).padStart(6, "0")}`, lineNo, code: "PRC-REL-001", reason: "No vendor: the line needs sourcing before it can be released." });

const runs = table<ReleaseRun>([
  run(
    4,
    "PARTIAL",
    0.5,
    [releasedOrder(6)],
    [
      noVendor(3, 1),
      noVendor(3, 2),
      {
        requisitionLineId: requisitions.get(uid(300, 4))?.lines[1]?.id ?? "",
        requisitionNumber: "REQ-007104",
        lineNo: 2,
        vendorId: V.steel.id,
        code: "PRC-REL-006",
        reason: "Riverbend Steel Service already has PO-048207 in review for this bar; the line waits so the two are not ordered twice.",
      },
    ],
    4,
    { requestedBy: "Dana Whitfield" },
  ),
  run(3, "COMPLETED", 5, [releasedOrder(10)], [], 1),
  run(2, "EMPTY", 8, [], [], 0),
  run(1, "COMPLETED", 18.2, [releasedOrder(2)], [], 1, { requestedBy: "Dana Whitfield" }),
]);

route("GET", `${BASE}/requisition-releases`, ({ query }) => {
  const status = query.get("status");
  return { items: runs.all().filter((r) => !status || r.status === status).sort((a, b) => b.createdAt.localeCompare(a.createdAt)) };
});

route("GET", `${BASE}/requisition-releases/{id}`, ({ params }) => {
  const r = runs.get(params.id);
  return r ? reply.versioned(r, 1) : reply.problem(404, "PRC-REL-NOT-FOUND", "Release run not found");
});

/** Releases every open, sourced line of an approved requisition: one draft purchase order per vendor. */
route("POST", `${BASE}/requisition-releases`, ({ body }) => {
  const input = (body ?? {}) as { windowDays?: number; maxLines?: number };
  const windowDays = input.windowDays ?? 30;
  const horizon = day(windowDays);
  const byVendor = new Map<string, { req: string; lineNo: number; productId: string; qty: number; date: string }[]>();
  const skipped: ReleaseSkip[] = [];
  let considered = 0;
  for (const r of requisitions.all()) {
    if (r.status !== "APPROVED") continue;
    for (const l of r.lines) {
      if (l.status !== "OPEN" || l.requestedDate > horizon) continue;
      considered += 1;
      if (!l.preferredVendorId) {
        skipped.push({ requisitionLineId: l.id, requisitionNumber: r.number, lineNo: l.lineNo, code: "PRC-REL-001", reason: "No vendor: the line needs sourcing before it can be released." });
        continue;
      }
      const list = byVendor.get(l.preferredVendorId) ?? [];
      list.push({ req: r.id, lineNo: l.lineNo, productId: l.productId ?? "", qty: Number(l.qty), date: l.requestedDate });
      byVendor.set(l.preferredVendorId, list);
    }
  }
  const orders: ReleaseRun["purchaseOrders"] = [];
  for (const [vendorId, lines] of byVendor) {
    const n = purchaseOrders.all().length + 1;
    const poLines: PurchaseOrderLine[] = lines.map((l, i) => {
      const it = ITEMS.find((x) => x.id === l.productId)!;
      const days = Math.round((Date.parse(l.date) - Date.parse(day(0))) / 86_400_000);
      return newPurchaseLine(i + 1, it.number, l.qty, days);
    });
    const made = newPurchaseOrder(n, vendorIndex(vendorId), "DRAFT", "DRAFT", 0, poLines);
    purchaseOrders.insert(made);
    orders.push({ purchaseOrderId: made.id, number: made.number, vendorId, currency: "USD", total: made.total, lineCount: poLines.length });
    for (const l of lines) {
      requisitions.update(l.req, (r) => ({ ...r, lines: r.lines.map((x) => (x.lineNo === l.lineNo ? { ...x, status: "CONSOLIDATED", purchaseOrderId: made.id, purchaseOrderLineNo: lines.indexOf(l) + 1 } : x)) }));
    }
  }
  const status: ReleaseRun["status"] = considered === 0 ? "EMPTY" : skipped.length === 0 ? "COMPLETED" : orders.length === 0 ? "FAILED" : "PARTIAL";
  const made = run(10 + runs.all().length, status, 0, orders, skipped, considered, { windowDays, maxLines: input.maxLines ?? 200, requestedBy: USER.name });
  runs.insert(made);
  return reply.versioned(made, 1, 201);
});

// ---- Scheduling agreements ----------------------------------------------------------------------------------

const PROFILE = { frcHorizonDays: 56, jitHorizonDays: 14, firmZoneDays: 10, tradeoffZoneDays: 21, releaseFrequency: "WEEKLY" as const };
const zoneOf = (date: string, profile = PROFILE): ScheduleZone => {
  const days = (Date.parse(date) - Date.parse(day(0))) / 86_400_000;
  return days <= profile.firmZoneDays ? "FIRM" : days <= profile.firmZoneDays + profile.tradeoffZoneDays ? "TRADEOFF" : "FORECAST";
};

interface SaSeed {
  n: number;
  vendor: (typeof VENDORS)[number];
  item: string;
  price: number;
  target: number;
  frequency: "DAILY" | "WEEKLY" | "MONTHLY";
  /** Days from today and quantity per delivery; past deliveries are received in full. */
  schedule: [number, number][];
  validFrom: number;
  validTo: number;
}

const SEEDS: SaSeed[] = [
  { n: 1, vendor: V.bearings, item: "BRG-6206", price: 7.25, target: 12000, frequency: "WEEKLY", schedule: [[-21, 500], [-14, 500], [-7, 500], [0, 500], [7, 600], [14, 600], [21, 600], [28, 500], [42, 500]], validFrom: -200, validTo: 165 },
  { n: 2, vendor: V.seals, item: "SEAL-MK-125", price: 47.9, target: 3600, frequency: "WEEKLY", schedule: [[-14, 120], [-7, 120], [3, 150], [10, 150], [17, 150], [31, 120]], validFrom: -120, validTo: 245 },
  { n: 3, vendor: V.foundry, item: "CST-IMP-06", price: 94.5, target: 1800, frequency: "MONTHLY", schedule: [[-30, 150], [5, 160], [35, 160], [65, 150]], validFrom: -95, validTo: 270 },
  { n: 4, vendor: V.steel, item: "RM-SS316-BAR", price: 17.95, target: 9000, frequency: "MONTHLY", schedule: [[-28, 600], [2, 600], [32, 650]], validFrom: -60, validTo: 305 },
];

function lines(seed: SaSeed): ScheduleLine[] {
  return seed.schedule.map(([offset, qty], i) => {
    const received = offset < 0 ? qty : 0;
    return { lineNo: i + 1, deliveryDate: day(offset), zone: zoneOf(day(offset)), qty: dec(qty, 0), receivedQty: dec(received, 0), openQty: dec(qty - received, 0) };
  });
}

function agreement(seed: SaSeed): SchedulingAgreement {
  const it = itemByNumber(seed.item);
  const ls = lines(seed);
  const scheduled = ls.reduce((s, l) => s + Number(l.qty), 0);
  const received = ls.reduce((s, l) => s + Number(l.receivedQty), 0);
  return {
    id: uid(K_SA, seed.n),
    number: `SA-2026-${String(seed.n * 3 + 10).padStart(3, "0")}`,
    legalEntityId: LEGAL_ENTITY.id,
    vendorId: seed.vendor.id,
    productId: it.id,
    siteId: PLANT.id,
    currency: "USD",
    unit: it.unit,
    unitPrice: dec(seed.price),
    targetQty: dec(seed.target, 0),
    scheduledQty: dec(scheduled, 0),
    receivedQty: dec(received, 0),
    validFrom: day(seed.validFrom),
    validTo: day(seed.validTo),
    profile: { ...PROFILE, releaseFrequency: seed.frequency },
    zonesAsOf: day(0),
    version: 4,
    lines: ls,
    createdAt: at(seed.validFrom - 7),
    updatedAt: at(-1),
  };
}

const agreements = table<SchedulingAgreement>(SEEDS.map(agreement));

function releaseOf(a: SchedulingAgreement, releaseNo: number, kind: "FRC" | "JIT", runDate: string): SchedulingRelease {
  const horizon = kind === "JIT" ? a.profile.jitHorizonDays : a.profile.frcHorizonDays;
  const end = Date.parse(runDate) + horizon * 86_400_000;
  let cum = 0;
  const cumReceived = a.lines.filter((l) => l.deliveryDate <= runDate).reduce((s, l) => s + Number(l.receivedQty), 0);
  const rlines: SchedulingReleaseLine[] = a.lines
    .filter((l) => l.deliveryDate >= runDate)
    .map((l, i) => {
      cum += Number(l.qty);
      return { lineNo: i + 1, scheduleLineNo: l.lineNo, deliveryDate: l.deliveryDate, zone: l.zone, inHorizon: Date.parse(l.deliveryDate) <= end, qty: l.qty, cumulativeReleased: dec(cumReceived + cum, 0), cumulativeReceived: dec(cumReceived, 0) };
    });
  const prefix = a.number.replace("SA-", "SR-");
  return {
    id: uid(K_SA_RELEASE, Number(a.id.slice(3, 8)) * 100 + releaseNo),
    agreementId: a.id,
    releaseNo,
    number: `${prefix}-${String(releaseNo).padStart(3, "0")}`,
    kind,
    runDate,
    agreementVersion: a.version,
    cumulativeReset: false,
    cumulativeReleased: rlines.at(-1)?.cumulativeReleased ?? dec(cumReceived, 0),
    cumulativeReceived: dec(cumReceived, 0),
    lines: rlines,
  };
}

const releases = new Map<string, SchedulingRelease[]>();
for (const a of agreements.all()) {
  const seed = SEEDS.find((s) => uid(K_SA, s.n) === a.id)!;
  const step = seed.frequency === "MONTHLY" ? 30 : 7;
  const list: SchedulingRelease[] = [];
  for (let i = 0; i < 4; i++) list.push(releaseOf(a, i + 1, i % 2 === 1 && seed.frequency === "WEEKLY" ? "JIT" : "FRC", day(-step * (4 - i))));
  releases.set(a.id, list);
}

function row(a: SchedulingAgreement): SchedulingAgreementRow {
  const list = releases.get(a.id) ?? [];
  const last = list.at(-1);
  const step = a.profile.releaseFrequency === "MONTHLY" ? 30 : a.profile.releaseFrequency === "DAILY" ? 1 : 7;
  const next = last ? new Date(Date.parse(last.runDate) + step * 86_400_000).toISOString().slice(0, 10) : day(0);
  return {
    id: a.id,
    number: a.number,
    legalEntityId: a.legalEntityId,
    vendorId: a.vendorId,
    productId: a.productId,
    siteId: a.siteId,
    unit: a.unit,
    validFrom: a.validFrom,
    validTo: a.validTo,
    releaseFrequency: a.profile.releaseFrequency,
    scheduledQty: a.scheduledQty,
    receivedQty: a.receivedQty,
    openQty: dec(Number(a.scheduledQty) - Number(a.receivedQty), 0),
    releaseCount: list.length,
    lastReleaseNumber: last?.number,
    lastReleaseKind: last?.kind,
    lastRunDate: last?.runDate,
    nextReleaseDue: next,
    version: a.version,
  };
}

route("GET", `${BASE}/scheduling-agreements`, ({ query }) => {
  const vendorId = query.get("vendorId");
  return { items: agreements.all().filter((a) => !vendorId || a.vendorId === vendorId).map(row) };
});

route("GET", `${BASE}/scheduling-agreements/{id}/releases/{no}`, ({ params }) => {
  const r = (releases.get(params.id) ?? []).find((x) => x.releaseNo === Number(params.no));
  return r ? reply.versioned(r, 1) : reply.problem(404, "PRC-SA-REL-NOT-FOUND", "Release not found");
});

route("GET", `${BASE}/scheduling-agreements/{id}/releases`, ({ params }) => ({ items: [...(releases.get(params.id) ?? [])].reverse().map(({ lines: _l, ...r }) => r) }));

route("PUT", `${BASE}/scheduling-agreements/{id}/schedule`, ({ params, body }) => {
  const a = agreements.get(params.id);
  if (!a) return reply.problem(404, "PRC-SA-NOT-FOUND", "Scheduling agreement not found");
  const input = (body as { lines: ScheduleLineInput[] }).lines;
  const warnings: string[] = [];
  let nextNo = Math.max(0, ...a.lines.map((l) => l.lineNo)) + 1;
  const ls: ScheduleLine[] = input.map((l) => {
    const old = a.lines.find((x) => x.lineNo === l.lineNo);
    const received = Number(old?.receivedQty ?? 0);
    if (old && Number(l.qty) < Number(old.qty) && old.zone === "TRADEOFF") warnings.push(`Line ${old.lineNo} reduced inside the trade-off zone; the vendor may charge for material already bought.`);
    return { lineNo: l.lineNo ?? nextNo++, deliveryDate: l.deliveryDate, zone: zoneOf(l.deliveryDate), qty: l.qty, receivedQty: dec(received, 0), openQty: dec(Math.max(0, Number(l.qty) - received), 0), reductionReason: l.reason ?? old?.reductionReason };
  });
  const scheduled = ls.reduce((s, l) => s + Number(l.qty), 0);
  const next = agreements.update(a.id, { lines: ls, scheduledQty: dec(scheduled, 0), version: a.version + 1, updatedAt: at(0) })!;
  return reply.versioned({ ...next, warnings }, next.version);
});

route("POST", `${BASE}/scheduling-agreements/{id}:release`, ({ params, body }) => {
  const a = agreements.get(params.id);
  if (!a) return reply.problem(404, "PRC-SA-NOT-FOUND", "Scheduling agreement not found");
  const input = (body ?? {}) as { kind?: "FRC" | "JIT"; runDate?: string; cumulativeReset?: boolean };
  const list = releases.get(a.id) ?? [];
  const made = { ...releaseOf(a, list.length + 1, input.kind ?? "FRC", input.runDate || day(0)), cumulativeReset: input.cumulativeReset ?? false };
  releases.set(a.id, [...list, made]);
  return reply.versioned(made, 1, 201);
});

route("GET", `${BASE}/scheduling-agreements/{id}`, ({ params }) => {
  const a = agreements.get(params.id);
  return a ? reply.versioned(a, a.version) : reply.problem(404, "PRC-SA-NOT-FOUND", "Scheduling agreement not found");
});
