// Technician day (/api/v1/service/technicians/me/day), the action log, captures and the truck's stock for the demo of
// the technician PWA. The day is Jordan Avery's, built from today's date, and the actions change it the way the service
// does: status moves go forward, parts add up, ticks and signatures stick, so a walk through a visit works end to end.
import type { ActionBody, ActionOutcome, DayVisit, TechnicianDay } from "../../modules/technician/domain/types";
import { moveTo } from "../../modules/technician/domain/projection";
import { CUSTOMERS, ITEMS, LEGAL_ENTITY, PLANT, USER, WAREHOUSES } from "../company";
import { reply, route } from "../backend";
import { uid } from "../store";

const SERVICE = "/api/v1/service";
const TECH_ID = uid(450, 1);
const TRUCK = WAREHOUSES[0]!;
const SEAL = ITEMS.find((i) => i.number === "SEAL-MK-125")!;
const BEARING = ITEMS.find((i) => i.number === "BRG-6206")!;

function localIso(hour: number, minute = 0): string {
  const d = new Date();
  d.setUTCHours(hour + 5, minute, 0, 0); // Chicago is UTC-5 or -6; the demo does not need the exact offset
  return d.toISOString();
}

function visit(n: number, customer: number, hour: number, address: string, lat: number, lon: number, description: string, extra: Partial<DayVisit>): DayVisit {
  return {
    id: uid(451, n),
    number: `SWO-0000${40 + n}`,
    status: "DISPATCHED",
    version: 3,
    sequence: n,
    carryOver: false,
    customerPartyId: CUSTOMERS[customer]!.id,
    caseNumber: `CS-0000${10 + n}`,
    caseDescription: description,
    priority: 2,
    address,
    latitude: lat,
    longitude: lon,
    assignedStart: localIso(hour),
    assignedEnd: localIso(hour + 1, 30),
    windowStart: localIso(hour - 1),
    windowEnd: localIso(hour + 3),
    estDurationMinutes: 90,
    requiredSkills: ["PUMP-REPAIR"],
    lines: [],
    checklist: [],
    photos: [],
    signature: { signed: false, exempt: false },
    technicianNote: "",
    ...extra,
  };
}

const visits: DayVisit[] = [
  visit(1, 0, 8, "1200 Harbor Drive, Duluth, MN", 46.7867, -92.1005, "Seal leaking on the 4 in booster pump", {
    serviceObjectLabel: "Serial CP-400-0198",
    lines: [{ id: uid(452, 1), kind: "PART", description: SEAL.name, productId: SEAL.id, unit: "EA", qty: "1", consumedQty: "0" }],
    checklist: [
      { id: uid(453, 1), text: "Lock out and tag the pump", required: true, done: false },
      { id: uid(453, 2), text: "Test run for 10 minutes", required: true, done: false },
      { id: uid(453, 3), text: "Photograph the nameplate", required: false, done: false },
    ],
  }),
  visit(2, 1, 11, "88 County Road 12, Harlan, IA", 41.6, -95.3253, "Pump vibrates at full load", {
    serviceObjectLabel: "Serial CP-200-0771",
    lines: [{ id: uid(452, 2), kind: "PART", description: BEARING.name, productId: BEARING.id, unit: "EA", qty: "2", consumedQty: "0" }],
    checklist: [{ id: uid(453, 4), text: "Check shaft alignment", required: true, done: false }],
  }),
  visit(3, 4, 14, "410 Platte Valley Road, Kearney, NE", 40.6995, -99.0817, "Annual service visit", { signature: { signed: false, exempt: true } }),
];

const seen = new Map<string, ActionOutcome>();
const captures = new Set<string>();

function today(): string {
  return new Date().toISOString().slice(0, 10);
}

function day(date: string): TechnicianDay {
  const list = date === today() ? visits : [];
  return {
    technician: { id: TECH_ID, code: "JAVERY", name: USER.name, timeZone: "America/Chicago", legalEntityId: LEGAL_ENTITY.id, truck: { siteId: PLANT.id, warehouseId: TRUCK.id } },
    date,
    generatedAt: new Date().toISOString(),
    visits: structuredClone(list),
    totals: {
      visits: list.length,
      completed: list.filter((v) => v.status === "COMPLETED").length,
      open: list.filter((v) => v.status !== "COMPLETED").length,
      bookedMinutes: list.reduce((n, v) => n + v.estDurationMinutes, 0),
    },
  };
}

route("GET", `${SERVICE}/technicians/me/day`, ({ query }) => day(query.get("date") || today()));

function apply(v: DayVisit, a: ActionBody): ActionOutcome {
  const base = { id: a.id, kind: a.kind };
  const to = moveTo(v.status, a.kind);
  switch (a.kind) {
    case "TRAVEL_START":
    case "WORK_START":
    case "HOLD":
    case "RESUME":
      if (!to) return { ...base, outcome: "SUPERSEDED", reason: `the work order is already ${v.status}` };
      v.status = to;
      return { ...base, outcome: "APPLIED" };
    case "COMPLETE": {
      if (!v.signature.signed && !v.signature.exempt) return { ...base, outcome: "REJECTED", code: "SVC-WO-001", reason: "the customer's signature is required to complete this work order" };
      if (v.checklist.some((c) => c.required && !c.done)) return { ...base, outcome: "REJECTED", code: "SVC-WO-003", reason: "a required checklist item is open" };
      if (!to) return { ...base, outcome: "SUPERSEDED", reason: `the work order is already ${v.status}` };
      v.status = to;
      v.completedAt = a.deviceTime;
      return { ...base, outcome: "APPLIED" };
    }
    case "PART_USED": {
      const line = v.lines.find((l) => l.id === a.lineId);
      if (!line) return { ...base, outcome: "REJECTED", code: "SVC-SYNC-004", reason: "the line is not a PART line of this work order" };
      const total = Number(line.consumedQty) + Number(a.qty);
      if (total > Number(line.qty)) return { ...base, outcome: "REJECTED", code: "SVC-SYNC-004", reason: `${total} used would exceed the line quantity ${line.qty}` };
      line.consumedQty = String(total);
      return { ...base, outcome: "APPLIED" };
    }
    case "CHECKLIST": {
      const item = v.checklist.find((c) => c.id === a.itemId);
      if (!item) return { ...base, outcome: "REJECTED", code: "SVC-SYNC-007", reason: "the work order's checklist has no such item" };
      item.done = a.done === true;
      return { ...base, outcome: "APPLIED" };
    }
    case "PHOTO": {
      for (const id of a.captureIds ?? []) {
        if (!captures.has(id)) return { ...base, outcome: "REJECTED", code: "SVC-CAP-002", reason: `the photo ${id} has not been uploaded for this work order` };
        v.photos.push({ captureId: id, ref: `capture:${id}`, capturedAt: a.deviceTime });
      }
      return { ...base, outcome: "APPLIED" };
    }
    case "SIGNATURE":
      v.signature = { signed: true, exempt: v.signature.exempt, signedBy: a.signedBy ?? null, signedAt: a.deviceTime, ref: a.signatureRef ?? null };
      return { ...base, outcome: "APPLIED" };
    case "NOTE":
      v.technicianNote = a.text ?? "";
      return { ...base, outcome: "APPLIED" };
  }
}

route("POST", `${SERVICE}/work-orders/{id}/actions`, ({ params, body }) => {
  const v = visits.find((x) => x.id === params.id);
  if (!v) return reply.problem(404, "SVC-WO-004", "service work order not found");
  const actions = ((body as { actions?: ActionBody[] }).actions ?? []).slice().sort((a, b) => a.deviceTime.localeCompare(b.deviceTime));
  const results = actions.map((a) => {
    const before = seen.get(a.id);
    if (before) return { ...before, duplicate: true };
    const out = apply(v, a);
    seen.set(a.id, out);
    return out;
  });
  v.version++;
  return reply.versioned(
    {
      results,
      applied: results.filter((r) => r.outcome === "APPLIED" && !r.duplicate).length,
      duplicates: results.filter((r) => r.duplicate).length,
      superseded: results.filter((r) => r.outcome === "SUPERSEDED").length,
      rejected: results.filter((r) => r.outcome === "REJECTED").length,
      workOrder: structuredClone(v),
    },
    v.version,
  );
});

route("PUT", `${SERVICE}/work-orders/{id}/captures/{captureId}`, ({ params, query }) => {
  if (!visits.some((x) => x.id === params.id)) return reply.problem(404, "SVC-WO-004", "service work order not found");
  const duplicate = captures.has(params.captureId!);
  captures.add(params.captureId!);
  const body = { id: params.captureId, ref: `capture:${params.captureId}`, workOrderId: params.id, technicianId: query.get("technicianId"), kind: query.get("kind"), duplicate };
  return duplicate ? reply.ok(body) : reply.created(body);
});

route("GET", "/api/v1/inventory/truck-stock-levels", () => ({
  items: [
    { productId: SEAL.id, unit: "EA", minQty: "2", maxQty: "6", onHandQty: "4", inboundQty: "0", short: false },
    { productId: BEARING.id, unit: "EA", minQty: "4", maxQty: "10", onHandQty: "3", inboundQty: "0", short: true },
  ],
}));
