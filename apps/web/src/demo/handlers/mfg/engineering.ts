// Engineering service (/api/v1/engineering) for the demo: bill of material versions built from company.ts.
// Products, sites and resources carry the real ids of company.ts; the screens turn them into names.
import type { BomLine, BomVersion, Resource, RouteVersion, RouteVersionOperation, UsedBy } from "../../../modules/engineering/api/client";
import { reply, route } from "../../backend";
import { BOMS, PLANT, RESOURCES, ROUTES, itemByNumber } from "../../company";
import { day, dec, table, uid } from "../../store";

const BASE = "/api/v1/engineering";
const KIND_BOM = 240;

function lines(product: string, tweak: (l: { component: string; qty: number }) => { component: string; qty: number } = (l) => l): BomLine[] {
  return BOMS[product].map(tweak).map((l, i) => {
    const raw = itemByNumber(l.component).kind === "raw";
    return {
      lineNo: (i + 1) * 10,
      componentProductId: itemByNumber(l.component).id,
      quantity: dec(l.qty, 4),
      lineType: "item",
      scrapConst: "0.0000",
      scrapVarPct: raw ? "2.00" : "0.00",
      roundUp: "no",
    };
  });
}

let seq = 0;
function version(product: string, status: string, from: number, to: number | undefined, rest: Partial<BomVersion> = {}): BomVersion {
  seq += 1;
  return {
    id: uid(KIND_BOM, seq),
    productId: itemByNumber(product).id,
    siteId: PLANT.id,
    effectiveFrom: day(from),
    effectiveTo: to === undefined ? undefined : day(to),
    fromQty: "1",
    status,
    usage: "production",
    category: "material",
    version: 1,
    lines: lines(product),
    ...rest,
  };
}

const boms = table<BomVersion>([
  version("CP-200", "active", -210, undefined),
  version("CP-400", "active", -180, undefined),
  version("SA-IMP-06", "active", -300, undefined),
  version("SA-MTR-05", "active", -260, undefined),
  version("SA-VOL-04", "active", -240, undefined),
  version("CP-200", "superseded", -520, -211),
  version("CP-400", "draft", 14, undefined, {
    version: 2,
    lines: lines("CP-400", (l) => (l.component === "FST-KIT-A" ? { ...l, qty: 3 } : l)),
  }),
  version("SA-IMP-06", "superseded", -620, -301, { lines: lines("SA-IMP-06").slice(0, 1) }),
]);

route("GET", `${BASE}/bom-versions`, (req) => {
  let rows = boms.all();
  const eq = (key: "productId" | "siteId" | "status" | "usage" | "category") => {
    const v = req.query.get(key);
    if (v) rows = rows.filter((r) => (r[key] ?? (key === "usage" ? "production" : key === "category" ? "material" : "")) === v);
  };
  eq("productId");
  eq("siteId");
  eq("status");
  eq("usage");
  eq("category");
  const asOf = req.query.get("asOf");
  if (asOf) rows = rows.filter((r) => r.effectiveFrom <= asOf && (!r.effectiveTo || r.effectiveTo >= asOf));
  const rank: Record<string, number> = { draft: 0, active: 1, superseded: 2 };
  rows.sort((a, b) => (rank[a.status] ?? 3) - (rank[b.status] ?? 3) || a.productId.localeCompare(b.productId));
  // The list shows headers; lines come with the version.
  return { items: rows.map(({ lines: _lines, ...header }) => (void _lines, header)) };
});

route("GET", `${BASE}/boms:where-used`, (req) => {
  const wanted = req.query.get("component") ?? "";
  const component = (() => {
    try {
      return itemByNumber(wanted).id;
    } catch {
      return wanted;
    }
  })();
  const items: UsedBy[] = boms
    .all()
    .filter((b) => b.status === "active" && (b.lines ?? []).some((l) => l.componentProductId === component))
    .map((b) => ({ productId: b.productId, versionId: b.id, siteId: b.siteId, level: 1 }));
  return { items };
});

route("GET", `${BASE}/bom-versions/{id}`, (req) => boms.get(req.params.id) ?? reply.problem(404, "ENG-BOM-NOTFOUND", "This BOM version does not exist."));

route("POST", `${BASE}/bom-versions/{id}:activate`, (req) => {
  const b = boms.get(req.params.id);
  if (!b) return reply.problem(404, "ENG-BOM-NOTFOUND", "This BOM version does not exist.");
  // Activating a draft supersedes the active version of the same product and site.
  for (const other of boms.all()) {
    if (other.productId === b.productId && other.status === "active") boms.update(other.id, { status: "superseded", effectiveTo: day(-1) });
  }
  return reply.ok(boms.update(b.id, { status: "active", effectiveFrom: day(0) }));
});

route("POST", `${BASE}/bom-versions/{id}:cancel`, (req) => {
  const b = boms.get(req.params.id);
  return b ? reply.ok(boms.update(b.id, { status: "cancelled" })) : reply.problem(404, "ENG-BOM-NOTFOUND", "This BOM version does not exist.");
});

// The resource master, which production forms and screens read to show work center names.
const resourceRows: Resource[] = RESOURCES.map((r) => ({
  id: r.id,
  code: r.code,
  name: r.name,
  type: r.code === "ASM-02" ? "machine" : "work-center",
  siteId: PLANT.id,
  efficiencyPct: r.code === "BAL-01" ? "92" : "100",
  capacityPerDayH: String(r.hoursPerDay),
  exclusive: true,
}));
route("GET", `${BASE}/resources`, (req) => {
  const q = (req.query.get("q") ?? "").toLowerCase();
  const rows = resourceRows.filter((r) => !q || r.name.toLowerCase().includes(q) || r.code.toLowerCase().includes(q));
  return { items: rows.slice(0, Number(req.query.get("limit") ?? 100)) };
});
route("GET", `${BASE}/resources/{id}`, (req) => resourceRows.find((r) => r.id === req.params.id || r.code === req.params.id) ?? reply.problem(404, "ENG-RES-NOTFOUND", "This resource does not exist."));

// Route versions built from the routings of company.ts. The route screens print product, site and operation as they
// come, so these carry the item number, the site code and readable operation codes (the lookups resolve both).
const OPERATION_CODES: Record<string, string> = {
  "Assemble pump": "ASSEMBLE-PUMP",
  "Hydrostatic and performance test": "HYDRO-TEST",
  "Coat and cure": "COAT-CURE",
  "Pack and label": "PACK-LABEL",
  Crate: "CRATE",
  "Machine bore and hub": "MACHINE-BORE",
  "Dynamic balance": "DYN-BALANCE",
  "Machine volute faces": "MACHINE-FACES",
  "Mount motor and coupling": "MOUNT-MOTOR",
};

function routeOps(product: string, tweak: (ops: RouteVersionOperation[]) => RouteVersionOperation[] = (o) => o): RouteVersionOperation[] {
  const steps = ROUTES[product];
  const ops = steps.map((s, i) => ({
    no: s.op,
    operationId: OPERATION_CODES[s.name] ?? s.name.toUpperCase().replace(/\W+/g, "-"),
    nextNo: steps[i + 1]?.op,
    linkType: steps[i + 1] ? "hard" : undefined,
    priority: "primary",
    setupH: dec(s.setupHours, 2),
    runH: dec(s.runHours, 2),
    processQty: "1",
    queueBeforeH: "2.00",
    queueAfterH: "0.00",
    transitH: steps[i + 1] ? "1.00" : "0.00",
    efficiencyPct: s.resource === "BAL-01" ? "92" : "100",
    scrapPercent: s.resource === "MCH-01" ? "1.5" : "0",
    requirements: [{ kind: "resource", refId: s.resource }, ...(s.resource === "TST-01" ? [{ kind: "capability", refId: "HYDRO-500PSI", level: 2 }] : [])],
  }));
  return tweak(ops);
}

function routeVersion(product: string, versionNo: number, status: string, from: number, to?: number, rest: Partial<RouteVersion> = {}): RouteVersion {
  const operations = rest.operations ?? routeOps(product);
  return {
    id: `RT-${product}-${versionNo}`,
    productId: product,
    siteId: PLANT.code,
    routeCode: `R-${product}`,
    versionNo,
    effectiveFrom: day(from),
    effectiveTo: to === undefined ? undefined : day(to),
    status,
    operationCount: operations.length,
    version: status === "draft" ? 2 : 1,
    ...rest,
    operations,
  };
}

const routeVersions = table<RouteVersion>([
  routeVersion("CP-200", 3, "active", -150),
  routeVersion("CP-200", 2, "superseded", -480, -151, { operations: routeOps("CP-200", (ops) => ops.map((o) => (o.no === 10 ? { ...o, runH: "1.45" } : o))) }),
  routeVersion("CP-400", 2, "active", -180),
  // Engineering is adding a seal leak check between assembly and the hydro test after the failures on MO-24611.
  routeVersion("CP-400", 3, "draft", 14, undefined, {
    copiedFromVersionId: "RT-CP-400-2",
    operations: routeOps("CP-400", (ops) => {
      const check: RouteVersionOperation = { no: 15, operationId: "SEAL-LEAK-CHECK", nextNo: 20, linkType: "hard", priority: "primary", setupH: "0.10", runH: "0.15", processQty: "1", queueBeforeH: "0.00", queueAfterH: "0.00", transitH: "0.00", efficiencyPct: "100", scrapPercent: "0", requirements: [{ kind: "resource", refId: "TST-01" }] };
      return [...ops.map((o) => (o.no === 10 ? { ...o, nextNo: 15 } : o)), check];
    }),
  }),
  routeVersion("SA-IMP-06", 4, "active", -95),
  routeVersion("SA-IMP-06", 3, "superseded", -400, -96, { operations: routeOps("SA-IMP-06", (ops) => ops.map((o) => (o.no === 20 ? { ...o, runH: "0.45" } : o))) }),
  routeVersion("SA-VOL-04", 1, "active", -520),
  routeVersion("SA-MTR-05", 2, "active", -260),
]);
const header = ({ operations: _ops, ...h }: RouteVersion) => (void _ops, h);

route("GET", `${BASE}/route-versions`, (req) => {
  const q = req.query;
  const wanted = (v: string | null, ...candidates: string[]) => !v || candidates.some((c) => c.toLowerCase() === v.trim().toLowerCase());
  let rows = routeVersions.all().filter((r) => {
    const item = (() => {
      try {
        return itemByNumber(r.productId);
      } catch {
        return undefined;
      }
    })();
    return wanted(q.get("productId"), r.productId, item?.id ?? "", item?.name ?? "") && wanted(q.get("siteId"), r.siteId, PLANT.id) && wanted(q.get("status"), r.status);
  });
  const asOf = q.get("asOf");
  if (asOf) rows = rows.filter((r) => r.effectiveFrom <= asOf && (!r.effectiveTo || r.effectiveTo >= asOf));
  const rank: Record<string, number> = { draft: 0, active: 1, superseded: 2, cancelled: 3 };
  rows.sort((a, b) => a.productId.localeCompare(b.productId) || (rank[a.status] ?? 4) - (rank[b.status] ?? 4) || b.versionNo - a.versionNo);
  return { items: rows.map(header) };
});
route("GET", `${BASE}/route-versions/{id}`, (req) => routeVersions.get(req.params.id) ?? reply.problem(404, "ENG-ROUTE-NOTFOUND", "This route version does not exist."));
route("POST", `${BASE}/route-versions/{id}:activate`, (req) => {
  const r = routeVersions.get(req.params.id);
  if (!r) return reply.problem(404, "ENG-ROUTE-NOTFOUND", "This route version does not exist.");
  if (r.status !== "draft") return reply.problem(409, "ENG-ROUTE-STATE", "Only a draft route version can be activated.");
  for (const other of routeVersions.all()) {
    if (other.productId === r.productId && other.status === "active") routeVersions.update(other.id, { status: "superseded", effectiveTo: day(-1), version: (other.version ?? 1) + 1 });
  }
  return reply.ok(routeVersions.update(r.id, { status: "active", effectiveFrom: day(0), version: (r.version ?? 1) + 1 }));
});
route("POST", `${BASE}/route-versions/{id}:cancel`, (req) => {
  const r = routeVersions.get(req.params.id);
  if (!r) return reply.problem(404, "ENG-ROUTE-NOTFOUND", "This route version does not exist.");
  return reply.ok(routeVersions.update(r.id, { status: "cancelled", version: (r.version ?? 1) + 1 }));
});
route("POST", `${BASE}/routes:lead-time`, (req) => {
  const body = req.body as { operations: { setup?: string; run?: string; processQty: string; queueBefore?: string; queueAfter?: string; transit?: string; efficiencyPct?: string }[]; quantity: string; hoursPerDay: string };
  const qty = Number(body.quantity) || 1;
  const hours = (body.operations ?? []).reduce((n, o) => {
    const eff = (Number(o.efficiencyPct ?? 100) || 100) / 100;
    return n + Number(o.setup ?? 0) + (Number(o.run ?? 0) * qty) / (Number(o.processQty) || 1) / eff + Number(o.queueBefore ?? 0) + Number(o.queueAfter ?? 0) + Number(o.transit ?? 0);
  }, 0);
  return { hours: dec(hours, 2), calendarDays: Math.ceil(hours / (Number(body.hoursPerDay) || 8)) };
});
