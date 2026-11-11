// Warehouse service (/api/v1/warehouse) for the demo: bin locations and directed work (put-away, pick, replenishment).
import type { Location, Work, WorkLine } from "../../../modules/warehouse/api/client";
import { ITEMS, KIND, WH_DAL, WH_FG, WH_RAW, WH_WIP, itemByNumber, type Warehouse } from "../../company";
import { reply, route } from "../../backend";
import { at, dec, table, uid } from "../../store";

const BASE = "/api/v1/warehouse";
const K_WORK = 310;

type Bin = Location & { warehouseId: string };

function bins(wh: Warehouse, zone: string, prefix: string, count: number, startAt: number): Bin[] {
  return Array.from({ length: count }, (_, i) => ({
    id: uid(KIND.location, startAt + i),
    warehouseId: wh.id,
    name: `${prefix}-${String(i + 1).padStart(2, "0")}`,
    zone,
    pickSequence: (i + 1) * 10,
    status: "ACTIVE" as const,
    capacity: { maxWeight: "1500", maxVolume: "6.0", maxLps: 4 },
    used: { weight: dec(180 + i * 95, 0), volume: dec(0.9 + i * 0.4, 1), lps: i % 3 },
    version: 1,
  }));
}

const LOCATIONS: Bin[] = [
  ...bins(WH_RAW, "Motors and castings", "RM-A", 4, 1),
  ...bins(WH_RAW, "Small parts", "RM-B", 4, 11),
  ...bins(WH_RAW, "Bar stock and coatings", "RM-C", 3, 21),
  ...bins(WH_WIP, "Subassemblies", "WIP-A", 4, 31),
  ...bins(WH_FG, "Finished pumps", "FG-A", 6, 41),
  ...bins(WH_DAL, "Dallas racks", "DAL-A", 4, 51),
];
const loc = (name: string): string => {
  const l = LOCATIONS.find((x) => x.name === name);
  if (!l) throw new Error(`demo location ${name} missing`);
  return l.id;
};
/** Default bin of each item, so receipts and work agree on where stock sits. */
export const BIN_OF: Record<string, string> = {
  "MTR-5HP-TEFC": "RM-A-01",
  "MTR-15HP-TEFC": "RM-A-02",
  "CST-VOL-04": "RM-A-03",
  "CST-IMP-06": "RM-A-04",
  "SEAL-MK-125": "RM-B-01",
  "BRG-6206": "RM-B-02",
  "FST-KIT-A": "RM-B-03",
  "RM-SS316-BAR": "RM-C-01",
  "RM-EPOXY-BLU": "RM-C-02",
  "SA-IMP-06": "WIP-A-01",
  "SA-MTR-05": "WIP-A-02",
  "SA-VOL-04": "WIP-A-03",
  "CP-200": "FG-A-01",
  "CP-400": "FG-A-02",
  "SP-150": "FG-A-03",
  "DP-100": "FG-A-04",
  "BP-300": "FG-A-05",
};
export const locationId = (itemNumber: string): string => loc(BIN_OF[itemNumber] ?? "RM-B-01");

function wline(lineNo: number, number: string, dir: "PUT" | "PICK", qty: number, confirmed: number): WorkLine {
  const it = itemByNumber(number);
  const bin = locationId(number);
  return {
    lineNo,
    itemId: number,
    direction: dir,
    qty: dec(qty, 0),
    confirmedQty: dec(confirmed, 0),
    weight: dec(qty * (it.kind === "finished" ? 38 : 4), 1),
    volume: dec(qty * 0.02, 2),
    lps: 1,
    ...(dir === "PUT" ? { toLocationId: bin } : { fromLocationId: bin }),
    status: confirmed >= qty ? "COMPLETED" : "OPEN",
  };
}

let workSeq = 0;
function work(wh: Warehouse, type: string, status: Work["status"], priority: number, ago: number, lines: WorkLine[], user?: string): Work {
  return {
    id: uid(K_WORK, ++workSeq),
    warehouseId: wh.id,
    workType: type,
    status,
    priority,
    assignedUser: user,
    assignedAt: user ? at(-ago + 0.05) : undefined,
    version: status === "OPEN" ? 1 : 2,
    createdAt: at(-ago),
    updatedAt: at(-ago + 0.05),
    lines,
  };
}

const works = table<Work>([
  work(WH_RAW, "PURCHASE_PUTAWAY", "IN_PROGRESS", 20, 0.3, [wline(1, "SEAL-MK-125", "PUT", 180, 120)], "Luis Ortega"),
  work(WH_RAW, "PURCHASE_PUTAWAY", "OPEN", 20, 0.1, [wline(1, "RM-SS316-BAR", "PUT", 250, 0)]),
  work(WH_RAW, "PRODUCTION_PICK", "ASSIGNED", 10, 0.2, [wline(1, "CST-IMP-06", "PICK", 12, 0), wline(2, "RM-SS316-BAR", "PICK", 18, 0)], "Marcus Bell"),
  work(WH_RAW, "PRODUCTION_PICK", "OPEN", 10, 0.15, [wline(1, "MTR-5HP-TEFC", "PICK", 6, 0), wline(2, "FST-KIT-A", "PICK", 6, 0), wline(3, "BRG-6206", "PICK", 12, 0)]),
  work(WH_RAW, "REPLENISHMENT", "OPEN", 40, 0.5, [wline(1, "BRG-6206", "PICK", 200, 0)]),
  work(WH_RAW, "PURCHASE_PUTAWAY", "COMPLETED", 20, 2.2, [wline(1, "BRG-6206", "PUT", 1000, 1000)], "Luis Ortega"),
  work(WH_RAW, "COUNT", "OPEN", 60, 1, [wline(1, "CST-VOL-04", "PICK", 31, 0)]),
  work(WH_WIP, "PRODUCTION_PICK", "OPEN", 10, 0.2, [wline(1, "SA-IMP-06", "PICK", 8, 0), wline(2, "SA-MTR-05", "PICK", 8, 0)]),
  work(WH_FG, "SALES_PICK", "ASSIGNED", 10, 0.2, [wline(1, "CP-200", "PICK", 12, 0)], "Marcus Bell"),
  work(WH_FG, "SALES_PICK", "OPEN", 20, 0.1, [wline(1, "SP-150", "PICK", 30, 0)]),
  work(WH_FG, "SALES_PICK", "OPEN", 20, 0.05, [wline(1, "CP-400", "PICK", 6, 0), wline(2, "BRG-6206", "PICK", 40, 0)]),
  work(WH_DAL, "TRANSFER_RECEIPT", "OPEN", 30, 0.4, [wline(1, "CP-200", "PUT", 15, 0)]),
]);

/** Directed work, shared with ASN receiving in docks.ts (a received pallet gets its put-away work). */
export const workTable = works;

route("GET", `${BASE}/work`, ({ query }) => {
  const wh = query.get("warehouseId");
  const status = query.get("status");
  const rows = works
    .all()
    .filter((w) => (!wh || w.warehouseId === wh.toLowerCase()) && (!status || w.status === status))
    .sort((a, b) => a.priority - b.priority || a.createdAt.localeCompare(b.createdAt));
  return { items: rows };
});

route("POST", `${BASE}/work:assign`, ({ body }) => {
  const wh = String((body as { warehouseId?: string } | undefined)?.warehouseId ?? "");
  const next = works
    .all()
    .filter((w) => w.warehouseId === wh && w.status === "OPEN")
    .sort((a, b) => a.priority - b.priority || a.createdAt.localeCompare(b.createdAt))[0];
  if (!next) return { work: null };
  return { work: works.update(next.id, { status: "ASSIGNED", assignedUser: "Jordan Avery", assignedAt: at(0), version: next.version + 1, updatedAt: at(0) }) };
});

route("GET", `${BASE}/work/{id}`, ({ params }) => {
  const w = works.get(params.id);
  return w ?? reply.problem(404, "WHS-0404", "Work not found");
});

route("POST", `${BASE}/work/{id}/lines/{n}:confirm`, ({ params, body }) => {
  const w = works.get(params.id);
  if (!w) return reply.problem(404, "WHS-0404", "Work not found");
  const qty = Number((body as { qty?: string } | undefined)?.qty ?? 0);
  const lines = w.lines.map((l): WorkLine => (l.lineNo === Number(params.n) ? { ...l, confirmedQty: dec(qty, 0), status: qty >= Number(l.qty) ? "COMPLETED" : "SHORT" } : l));
  const done = lines.every((l) => l.status !== "OPEN");
  return works.update(w.id, { lines, status: done ? "COMPLETED" : "IN_PROGRESS", version: w.version + 1, updatedAt: at(0) });
});

route("GET", `${BASE}/locations`, ({ query }) => {
  const wh = query.get("warehouseId");
  const items = LOCATIONS.filter((l) => !wh || l.warehouseId === wh.toLowerCase()).map(({ warehouseId: _w, ...l }) => {
    const stock: Record<string, string> = {};
    for (const it of ITEMS) if (BIN_OF[it.number] === l.name) stock[it.number] = dec(it.onHand, 0);
    return { ...l, stock };
  });
  return { items };
});

route("GET", `${BASE}/location-directives`, () => ({ items: [] }));
