// Engineering service (/api/v1/engineering) for the demo: bill of material versions built from company.ts.
// Products, sites and resources carry the real ids of company.ts; the screens turn them into names.
import type { BomLine, BomVersion, Resource, UsedBy } from "../../../modules/engineering/api/client";
import { reply, route } from "../../backend";
import { BOMS, PLANT, RESOURCES, itemByNumber } from "../../company";
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
