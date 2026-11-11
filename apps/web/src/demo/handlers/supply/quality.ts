// Quality service (/api/v1/quality) for the demo, beside the quality orders of mfg/quality.ts: nonconformances with
// their 8D correction, and the source inspection check of purchase receipts. NCR ids are short readable codes
// (the screens print the first eight characters), and they point at the quality orders (QO-24003, QO-24006),
// vendors, customers and receipts the rest of the demo shows.
import type { Correction, CorrectionStep, NewNonconformance, NewReceiptCheck, Nonconformance, ReceiptCheck } from "../../../modules/quality/api/client";
import { CUSTOMERS, ITEMS, USER, VENDORS, itemByNumber } from "../../company";
import { reply, route, type DemoRequest } from "../../backend";
import { at, dec, table, uid } from "../../store";
import { purchaseOrders } from "./purchasing";

const BASE = "/api/v1/quality";

const STEP_NAMES = ["Form the team", "Describe the problem", "Contain the problem", "Find the root cause", "Choose corrective actions", "Implement and validate", "Prevent recurrence", "Close and recognise the team"];

function correction(started: number, currentStep: number, notes: string[], extra: Partial<Correction> = {}): Correction {
  const steps: CorrectionStep[] = STEP_NAMES.map((name, i) => {
    const done = i + 1 < currentStep;
    return {
      step: i + 1,
      name,
      dueAt: at(-started + (i + 1) * 3),
      notes: done ? notes[i] : undefined,
      completedBy: done ? (i % 2 === 0 ? "Priya Natarajan" : "Dana Kowalski") : undefined,
      completedAt: done ? at(-started + (i + 1) * 2.5) : undefined,
    };
  });
  const overdue = steps.filter((s) => !s.completedAt && s.dueAt && s.dueAt < at(0)).map((s) => s.step);
  return {
    id: `8D-${String(Math.round(started * 10)).padStart(4, "0")}`,
    status: "OPEN",
    currentStep,
    steps,
    startedBy: "Priya Natarajan",
    startedAt: at(-started),
    effectivenessDays: 30,
    reopened: 0,
    overdueSteps: overdue,
    effectivenessCheckDue: false,
    ...extra,
  };
}

interface NcrSeed extends Partial<Nonconformance> {
  n: number;
  item: string;
  ago: number;
}

function ncr(s: NcrSeed): Nonconformance {
  const { n, item, ago, ...rest } = s;
  const it = itemByNumber(item);
  return {
    id: `NC-${24000 + n}`,
    source: "INTERNAL",
    itemId: it.id,
    qty: "1",
    uom: it.unit,
    problemType: "DIMENSION",
    severity: "MINOR",
    description: "",
    dims: { site: "CR" },
    status: "NEW",
    createdBy: "Priya Natarajan",
    createdAt: at(-ago),
    updatedAt: at(-ago + 0.3),
    version: 1,
    ...rest,
  };
}

const ncrs = table<Nonconformance>([
  ncr({
    n: 36,
    item: "CP-200",
    ago: 0.3,
    source: "PRODUCTION",
    qty: "1",
    problemType: "PERFORMANCE",
    severity: "MAJOR",
    description: "Sample 5 of batch B24608 delivered 91.8 gpm at rated head against a 94.0 minimum. Impeller clearance suspected.",
    qualityOrderId: "QO-24007",
    productionOrderId: "MO-24608",
    dims: { site: "CR", warehouse: "CR-FG", batch: "B24608" },
  }),
  ncr({
    n: 35,
    item: "CST-IMP-06",
    ago: 0.7,
    source: "VENDOR",
    qty: "6",
    problemType: "POROSITY",
    severity: "MAJOR",
    description: "Six of the first 20 impeller castings in lot IR-9931 show surface porosity on the shroud after machining.",
    qualityOrderId: "QO-24006",
    vendorId: VENDORS[0].id,
    dims: { site: "CR", warehouse: "CR-RM", batch: "IR-9931" },
  }),
  ncr({
    n: 34,
    item: "CP-400",
    ago: 2.4,
    source: "CUSTOMER",
    qty: "1",
    problemType: "LEAK",
    severity: "CRITICAL",
    description: "Summit Mining Supply reports a mechanical seal leak on a CP-400 from SO-024180 after 300 running hours.",
    customerId: CUSTOMERS[2].id,
    status: "APPROVED",
    version: 2,
    updatedAt: at(-2),
  }),
  ncr({
    n: 33,
    item: "BRG-6206",
    ago: 6,
    source: "VENDOR",
    qty: "40",
    problemType: "MATERIAL",
    severity: "MINOR",
    description: "Precision Bearing advisory: lot PB-2588 may be under-filled with grease. 40 bearings held for checking.",
    vendorId: VENDORS[5].id,
    status: "IN_CORRECTION",
    version: 4,
    dims: { site: "CR", warehouse: "CR-RM", batch: "PB-2588" },
    correction: correction(5, 4, ["Quality, purchasing and the Precision Bearing account manager.", "Grease fill 18 % below specification on 3 of 10 sampled bearings.", "Lot PB-2588 placed on hold (40 bearings); no affected units shipped."]),
  }),
  ncr({
    n: 31,
    item: "SA-IMP-06",
    ago: 14,
    source: "PRODUCTION",
    qty: "2",
    problemType: "BALANCE",
    severity: "MAJOR",
    description: "Two impeller assemblies of batch B24575 failed dynamic balance (G6.3) on the balancing machine.",
    qualityOrderId: "QO-24003",
    productionOrderId: "MO-24575",
    status: "IN_CORRECTION",
    version: 9,
    dims: { site: "CR", warehouse: "CR-WIP", batch: "B24575" },
    correction: correction(
      13,
      9,
      [
        "Machining, balancing and quality leads.",
        "Unbalance of 14 g-mm against a 9 g-mm limit on two of eight sampled impellers.",
        "Batch rebalanced; the two failures reworked and passed.",
        "Worn collet on the CNC cell left the bore 0.04 mm off centre.",
        "Replace the collet and add a bore runout check after every tool change.",
        "Collet replaced; 40 impellers since then all within limits.",
        "Runout check added to the machining routing and the preventive maintenance plan.",
        "Team thanked at the plant meeting.",
      ],
      { status: "EFFECTIVENESS_PENDING", effectivenessDueAt: at(9), effectivenessCheckDue: false },
    ),
  }),
  ncr({
    n: 29,
    item: "SEAL-MK-125",
    ago: 9.5,
    source: "VENDOR",
    qty: "10",
    problemType: "DIMENSION",
    severity: "MINOR",
    description: "Ten seal kits from lot KS-7720 had the wrong O-ring size packed.",
    qualityOrderId: "QO-24001",
    vendorId: VENDORS[2].id,
    status: "CLOSED",
    version: 12,
    correction: correction(9, 9, STEP_NAMES.map(() => "Done."), { status: "CLOSED", effectivenessResult: "EFFECTIVE", effectivenessNote: "No mispacks in the next four Keystone deliveries.", effectivenessBy: "Priya Natarajan", effectivenessAt: at(-1), closedAt: at(-1) }),
  }),
  ncr({
    n: 27,
    item: "MTR-5HP-TEFC",
    ago: 11,
    source: "VENDOR",
    qty: "2",
    problemType: "DAMAGE",
    severity: "MINOR",
    description: "Two motors arrived with dented fan covers; carrier damage, not a supplier defect.",
    vendorId: VENDORS[1].id,
    status: "REFUSED",
    statusReason: "Freight damage is claimed against the carrier, not raised against the vendor.",
    version: 2,
  }),
]);

const ifMatch = (req: DemoRequest): number | undefined => {
  const raw = req.headers.get("If-Match");
  return raw ? Number(raw.replace(/"/g, "")) : undefined;
};

route("GET", `${BASE}/nonconformances`, ({ query }) => {
  const status = query.get("status");
  const source = query.get("source");
  const severity = query.get("severity");
  const qo = query.get("qualityOrderId");
  const items = ncrs
    .all()
    .filter((r) => (!status || r.status === status) && (!source || r.source === source) && (!severity || r.severity === severity) && (!qo || r.qualityOrderId === qo))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  return { items };
});

route("GET", `${BASE}/nonconformances/{id}`, ({ params }) => {
  const r = ncrs.get(params.id);
  return r ? reply.versioned(r, r.version) : reply.problem(404, "QM-0404", "Nonconformance not found");
});

route("POST", `${BASE}/nonconformances`, ({ body }) => {
  const input = body as NewNonconformance;
  const it = ITEMS.find((i) => i.id === input.itemId);
  const n = 40 + ncrs.all().length;
  const made: Nonconformance = {
    ...ncr({ n, item: it?.number ?? "CP-200", ago: 0 }),
    ...input,
    itemId: input.itemId ?? it?.id ?? "",
    uom: input.uom || it?.unit || "ea",
    description: input.description ?? "",
    createdBy: USER.name,
    createdAt: at(0),
    updatedAt: at(0),
  };
  ncrs.insert(made);
  return reply.versioned(made, 1, 201);
});

function step(req: DemoRequest, change: (r: Nonconformance) => Nonconformance | ReturnType<typeof reply.problem>) {
  const r = ncrs.get(req.params.id);
  if (!r) return reply.problem(404, "QM-0404", "Nonconformance not found");
  const version = ifMatch(req);
  if (version !== undefined && version !== r.version) return reply.problem(409, "QM-0409", "Someone changed this NCR since you opened it. Reload and try again.");
  const out = change(r);
  if (!("id" in out)) return out;
  const next = ncrs.update(r.id, { ...out, version: r.version + 1, updatedAt: at(0) })!;
  return reply.versioned(next, next.version);
}

route("POST", `${BASE}/nonconformances/{id}:approve`, (req) => step(req, (r) => (r.status === "NEW" ? { ...r, status: "APPROVED" } : reply.problem(409, "QM-0131", "Only a new NCR can be approved"))));

route("POST", `${BASE}/nonconformances/{id}:refuse`, (req) =>
  step(req, (r) => (r.status === "NEW" ? { ...r, status: "REFUSED", statusReason: String((req.body as { reason?: string } | undefined)?.reason ?? "") } : reply.problem(409, "QM-0131", "Only a new NCR can be refused"))),
);

route("POST", `${BASE}/nonconformances/{id}/corrections`, (req) =>
  step(req, (r) => {
    if (r.status !== "APPROVED") return reply.problem(409, "QM-0132", "Approve the NCR before starting its 8D");
    const days = Number((req.body as { effectivenessCheckDays?: number } | undefined)?.effectivenessCheckDays ?? 30);
    return { ...r, status: "IN_CORRECTION", correction: { ...correction(0, 1, []), startedBy: USER.name, effectivenessDays: days, overdueSteps: [] } };
  }),
);

route("POST", `${BASE}/nonconformances/{id}:complete-step`, (req) =>
  step(req, (r) => {
    const c = r.correction;
    if (!c || c.status !== "OPEN") return reply.problem(409, "QM-0133", "The 8D has no open step");
    const input = req.body as { step: number; notes: string };
    const steps = c.steps.map((s) => (s.step === input.step ? { ...s, notes: input.notes, completedBy: USER.name, completedAt: at(0) } : s));
    const last = input.step >= steps.length;
    return {
      ...r,
      correction: {
        ...c,
        steps,
        currentStep: last ? c.currentStep : input.step + 1,
        overdueSteps: c.overdueSteps.filter((n) => n !== input.step),
        ...(last ? { status: "EFFECTIVENESS_PENDING" as const, effectivenessDueAt: at(c.effectivenessDays) } : {}),
      },
    };
  }),
);

route("POST", `${BASE}/nonconformances/{id}:check-effectiveness`, (req) =>
  step(req, (r) => {
    const c = r.correction;
    if (!c || c.status !== "EFFECTIVENESS_PENDING") return reply.problem(409, "QM-0134", "The 8D is not waiting for its effectiveness check");
    const input = req.body as { result: "EFFECTIVE" | "NOT_EFFECTIVE"; note: string };
    const effective = input.result === "EFFECTIVE";
    return {
      ...r,
      status: effective ? "CLOSED" : "IN_CORRECTION",
      correction: {
        ...c,
        status: effective ? "CLOSED" : "OPEN",
        effectivenessResult: input.result,
        effectivenessNote: input.note,
        effectivenessBy: USER.name,
        effectivenessAt: at(0),
        closedAt: effective ? at(0) : undefined,
        reopened: effective ? c.reopened : c.reopened + 1,
        currentStep: effective ? c.currentStep : 4,
      },
    };
  }),
);

// ---- Source inspection at receipt ---------------------------------------------------------------------------

const poLine = (po: number, lineNo: number) => purchaseOrders.get(uid(302, po))?.lines.find((l) => l.lineNo === lineNo)?.id ?? uid(303, po * 10 + lineNo);

function check(n: number, po: number, lineNo: number, batch: string, receipt: string, received: number, accepted: number, before: number, ago: number, by = "Luis Ortega"): ReceiptCheck {
  const covered = Math.max(0, Math.min(received, accepted - before));
  const normal = received - covered;
  const skip = normal === 0;
  return {
    id: uid(429, n),
    poLineId: poLine(po, lineNo),
    batch,
    receiptLineId: receipt,
    receivedQty: dec(received, 0),
    acceptedQty: dec(accepted, 0),
    coveredBefore: dec(before, 0),
    skip,
    coveredQty: dec(covered, 0),
    normalQty: dec(normal, 0),
    ...(skip ? { code: "QM-0128" as const } : {}),
    message: skip
      ? `Covered by source inspection at the vendor: ${covered} of ${accepted} accepted.`
      : covered > 0
        ? `${covered} covered by source inspection; ${normal} need a receiving quality order.`
        : "No accepted source inspection for this line and batch; the whole receipt needs a receiving quality order.",
    checkedBy: by,
    checkedAt: at(-ago),
  };
}

const checks = table<ReceiptCheck>([
  check(1, 4, 1, "KS-8104", "R10401-1", 120, 300, 180, 0.2),
  check(2, 11, 1, "HT-24-8841", "R10402-1", 250, 0, 0, 0.7),
  check(3, 4, 1, "KS-8104", "R10403-1", 180, 300, 0, 2),
  check(4, 5, 1, "PB-2611", "R10404-1", 1000, 600, 0, 4),
  check(5, 3, 1, "VM-5H-0924", "R10405-1", 24, 24, 0, 10, "Marcus Bell"),
  check(6, 1, 1, "IR-9931", "R10398-1", 120, 0, 0, 0.8),
]);

route("GET", `${BASE}/receipt-checks`, ({ query }) => {
  const poLineId = query.get("poLineId");
  const batch = query.get("batch");
  return { items: checks.all().filter((c) => (!poLineId || c.poLineId === poLineId) && (!batch || c.batch === batch)).sort((a, b) => b.checkedAt.localeCompare(a.checkedAt)) };
});

route("POST", `${BASE}/receipt-checks`, ({ body }) => {
  const input = body as NewReceiptCheck;
  const earlier = checks.all().filter((c) => c.poLineId === input.poLineId && c.batch === input.batch);
  const accepted = Number(earlier[0]?.acceptedQty ?? 0);
  const before = earlier.reduce((s, c) => s + Number(c.coveredQty), 0);
  const made = { ...check(100 + checks.all().length, 0, 0, input.batch, input.receiptLineId, Number(input.receivedQty), accepted, before, 0, USER.name), poLineId: input.poLineId };
  return reply.created(checks.insert(made));
});
