// Inventory service (/api/v1/inventory) for the demo: the on-hand projection and the workspace tiles.
// On-hand per item comes from company.ts, so it agrees with what planning and sales show. Group values are ids, as the
// service sends them; the page shows the names behind them.
import type { OnHandRow, WorkspaceSummary } from "../../../modules/inventory/api/client";
import { DC, ITEMS, LEGAL_ENTITY, PLANT, SITES, WAREHOUSES, WH_DAL, WH_FG, WH_RAW, WH_WIP, type Item, type Warehouse } from "../../company";
import { route } from "../../backend";
import { at, dec } from "../../store";
import { BIN_OF } from "./warehouse";

const BASE = "/api/v1/inventory";

interface Fact {
  item: Item;
  warehouse: Warehouse;
  qty: number;
  status: "AVAILABLE" | "QUARANTINE" | "BLOCKED";
}

/** Stock added by posted receipts during this session. */
const received = new Map<string, number>();
export function adjustOnHand(itemId: string, delta: number): void {
  received.set(itemId, (received.get(itemId) ?? 0) + delta);
}

/** Finished goods also sit in the Dallas DC, on top of the plant figure in company.ts. */
const DALLAS: Record<string, number> = { "CP-200": 15, "SP-150": 30, "CP-400": 3, "DP-100": 2 };
/** Part of an item's stock that is not usable yet. */
const QUARANTINE: Record<string, number> = { "CST-IMP-06": 3, "SEAL-MK-125": 10, "MTR-5HP-TEFC": 2 };
const RESERVED: Record<string, number> = { "CP-200": 16, "CP-400": 9, "SP-150": 30, "BP-300": 2, "SEAL-MK-125": 24, "BRG-6206": 40, "SA-IMP-06": 6, "MTR-15HP-TEFC": 2 };

function facts(): Fact[] {
  const out: Fact[] = [];
  for (const item of ITEMS) {
    const wh = item.kind === "finished" ? WH_FG : item.kind === "subassembly" ? WH_WIP : WH_RAW;
    const total = item.onHand + (received.get(item.id) ?? 0);
    const q = QUARANTINE[item.number] ?? 0;
    out.push({ item, warehouse: wh, qty: total - q, status: "AVAILABLE" });
    if (q > 0) out.push({ item, warehouse: wh, qty: q, status: "QUARANTINE" });
    if (DALLAS[item.number]) out.push({ item, warehouse: WH_DAL, qty: DALLAS[item.number], status: "AVAILABLE" });
  }
  return out;
}

const matches = (value: string | null, ...candidates: string[]): boolean => !value || candidates.some((c) => c.toLowerCase() === value.trim().toLowerCase());

route("GET", `${BASE}/onhand`, ({ query }) => {
  const groupBy = (query.get("groupBy") ?? "product").split(",").filter(Boolean);
  const rows = facts().filter(
    (f) =>
      matches(query.get("product"), f.item.id, f.item.number) &&
      matches(query.get("site"), f.warehouse.siteId, f.warehouse.siteId === PLANT.id ? PLANT.code : DC.code) &&
      matches(query.get("warehouse"), f.warehouse.id, f.warehouse.code),
  );
  const groups = new Map<string, { group: Record<string, string>; rows: Fact[] }>();
  for (const f of rows) {
    const group: Record<string, string> = {};
    for (const g of groupBy) {
      if (g === "product") group[g] = f.item.id;
      else if (g === "site") group[g] = SITES.find((s) => s.id === f.warehouse.siteId)?.id ?? "";
      else if (g === "warehouse") group[g] = f.warehouse.id;
      else if (g === "location") group[g] = BIN_OF[f.item.number] ?? "";
      else if (g === "inventory_status") group[g] = f.status;
      else if (g === "legal_entity") group[g] = LEGAL_ENTITY.id;
    }
    const key = JSON.stringify(group);
    const entry = groups.get(key) ?? { group, rows: [] };
    entry.rows.push(f);
    groups.set(key, entry);
  }
  const items: OnHandRow[] = [...groups.values()].map(({ group, rows: fs }) => {
    const physical = fs.reduce((s, f) => s + f.qty, 0);
    const blocked = fs.filter((f) => f.status !== "AVAILABLE").reduce((s, f) => s + f.qty, 0);
    const reserved = fs.reduce((s, f) => s + (f.warehouse.id === WH_DAL.id || f.status !== "AVAILABLE" ? 0 : (RESERVED[f.item.number] ?? 0)), 0);
    const value = fs.reduce((s, f) => s + f.qty * f.item.cost, 0);
    return {
      group,
      physicalInvent: dec(physical, 0),
      availPhysical: dec(Math.max(0, physical - blocked - reserved), 0),
      availOrdered: dec(Math.max(0, physical - blocked - reserved), 0),
      postedValue: dec(value),
      reservedPhysical: dec(reserved, 0),
      reservedOrdered: "0",
      blocked: dec(blocked, 0),
      heldPhysical: "0",
      currency: "USD",
      negative: false,
    };
  });
  const label = (g: Partial<Record<string, string>>) => (g.product ? (ITEMS.find((i) => i.id === g.product)?.number ?? "") : (WAREHOUSES.find((w) => w.id === g.warehouse)?.code ?? ""));
  items.sort((a, b) => label(a.group).localeCompare(label(b.group)) || JSON.stringify(a.group).localeCompare(JSON.stringify(b.group)));
  return { items, truncated: false };
});

route("GET", `${BASE}/workspace/summary`, () => {
  const summary: WorkspaceSummary = {
    journals: { draft: 3, awaitingApproval: 2 },
    negativeOnHandRows: 0,
    openCountSheets: 2,
    countSheetsCounted: 1,
    reconciliationDifferences: 0,
    lastReconciliationAt: at(-1.2),
    activeReservations: 14,
    transferOrdersByStatus: { DRAFT: 1, SHIPPED: 1, RECEIVED: 4 } as WorkspaceSummary["transferOrdersByStatus"],
  };
  return summary;
});


