// Migration factory (/api/v1/migration) for the demo: two reconciled cutover runs of Cedar Ridge Pumps, the on-hand stock and the
// bank balances of the legacy AX company, and their opening load into the inventory ledger and treasury. Typed against the
// module's client so shape drift fails `tsc`.
import type { OpeningLoadDetail, OpeningLoadRequest, PackRunDetail } from "../../modules/migration-factory/api/client";
import { LEGAL_ENTITY } from "../company";
import { reply, route } from "../backend";
import { at, uid } from "../store";

const BASE = "/api/v1/migration";
const K_RUN = 1910;

const total = (name: string, key: Record<string, string>, source: string, target = source) => ({ name, key, source, target, delta: "0", status: "MATCH" as const });

const runs: PackRunDetail[] = [
  {
    runId: uid(K_RUN, 1),
    pack: "onhand",
    sourceSystem: "AX2012",
    sourceCompany: "CRP",
    cutoffTs: at(-3),
    inputHash: "sha256:onhand-crp-cutover",
    status: "RECONCILED",
    recordsIn: 3,
    recordsLoaded: 3,
    recordsQuarantined: 0,
    alreadyPresent: 0,
    version: 1,
    createdAt: at(-3),
    controlTotals: [
      total("onhand.quantity", { item: "PMP-200", unit: "EA" }, "42"),
      total("onhand.quantity", { item: "IMP-075", unit: "EA" }, "310"),
      total("onhand.value", { currency: "USD" }, "214380.00"),
    ],
    quarantined: [],
  },
  {
    runId: uid(K_RUN, 2),
    pack: "bank_balances",
    sourceSystem: "AX2012",
    sourceCompany: "CRP",
    cutoffTs: at(-3),
    inputHash: "sha256:bank-crp-cutover",
    status: "RECONCILED",
    recordsIn: 2,
    recordsLoaded: 2,
    recordsQuarantined: 0,
    alreadyPresent: 0,
    version: 1,
    createdAt: at(-3),
    controlTotals: [total("bank_balances.balance", { currency: "USD" }, "1285400.00"), total("bank_balances.account_count", {}, "2")],
    quarantined: [],
  },
];

const loads = new Map<string, OpeningLoadDetail>([
  [
    uid(K_RUN, 2),
    {
      runId: uid(K_RUN, 2),
      target: "treasury",
      legalEntityId: LEGAL_ENTITY.id,
      status: "RECONCILED",
      clean: true,
      staged: 2,
      loaded: 2,
      rejected: 0,
      pending: 0,
      attempts: 1,
      startedAt: at(-2),
      finishedAt: at(-2),
      version: 2,
      reconciliation: [total("target.bank_balances.item_count", {}, "2"), total("target.bank_balances.balance", { currency: "USD" }, "128540000")],
      rejects: [],
    },
  ],
]);

route("GET", `${BASE}/runs/{id}`, ({ params }) => runs.find((r) => r.runId === params.id) ?? reply.problem(404, "MIG-0404", "No pack run with that id"));

route("GET", `${BASE}/runs/{id}/opening-load`, ({ params }) => loads.get(params.id) ?? reply.problem(409, "MIG-1563", "the run has not been loaded: post to opening-load first"));

route("POST", `${BASE}/runs/{id}/opening-load`, ({ params, body }) => {
  const run = runs.find((r) => r.runId === params.id);
  if (!run || (run.pack !== "onhand" && run.pack !== "bank_balances")) return reply.problem(409, "MIG-1564", "only a RECONCILED onhand or bank_balances run is loaded");
  const req = body as OpeningLoadRequest;
  const onhand = run.pack === "onhand";
  // The demo's legacy items are codes, so each needs a product; one left unmapped is rejected, as the factory does.
  const legacy = onhand ? ["PMP-200", "IMP-075", "SEAL-KIT"] : ["OPER-USD", "PAYROLL-USD"];
  const map = (onhand ? req.products : req.bankAccounts) ?? {};
  const rejected = legacy.filter((code) => !map[code]);
  const loaded = legacy.length - rejected.length;
  const detail: OpeningLoadDetail = {
    runId: run.runId,
    target: onhand ? "inventory" : "treasury",
    legalEntityId: req.legalEntityId,
    status: rejected.length === 0 ? "RECONCILED" : "MISMATCH",
    clean: rejected.length === 0,
    staged: legacy.length,
    loaded,
    rejected: rejected.length,
    pending: 0,
    attempts: (loads.get(run.runId)?.attempts ?? 0) + 1,
    startedAt: at(0),
    finishedAt: at(0),
    version: (loads.get(run.runId)?.version ?? 0) + 1,
    reconciliation: onhand
      ? [total("target.onhand.line_count", {}, String(legacy.length), String(loaded)), total("target.onhand.value", { currency: "USD" }, "214380.00", rejected.length === 0 ? "214380.00" : "198120.00")]
      : [total("target.bank_balances.item_count", {}, String(legacy.length), String(loaded))],
    rejects: rejected.map((code) => ({ legacyId: code, code: "MIG-REF-UNRESOLVED", detail: `legacy ${onhand ? "item" : "bank account"} ${code} has no Passage ${onhand ? "product" : "bank account"}` })),
  };
  detail.reconciliation = detail.reconciliation.map((t) => (t.source === t.target ? t : { ...t, delta: String(Number(t.source) - Number(t.target)), status: "MISMATCH" as const }));
  loads.set(run.runId, detail);
  const { reconciliation: _r, rejects: _x, ...head } = detail;
  return head;
});
