import type { PassageModule } from "../modules/registry";
import type { IconName } from "./ui";

/** A top-level sidebar area: a handful of these replace one group per module, so the sidebar fits without scrolling. */
export interface NavArea {
  id: string;
  title: string;
  icon: IconName;
  /** Module ids, in the order they are listed. */
  modules: string[];
}

// Modules that are not listed here land in "More" (see groupByArea), so a new module never disappears from the sidebar.
export const AREAS: NavArea[] = [
  {
    id: "finance",
    title: "Finance",
    icon: "receipt",
    modules: ["ledger", "close", "finreport", "payables", "ap-invoices", "receivables", "treasury", "fx", "tax", "assets", "budgeting", "costacct", "consolidation", "intercompany"],
  },
  { id: "sales", title: "Sales and billing", icon: "cart", modules: ["sales", "pricing", "billing"] },
  {
    id: "supply-chain",
    title: "Supply chain",
    icon: "truck",
    modules: ["purchasing", "vendorgroups", "receiving", "inventory", "costing", "warehouse", "fulfillment", "transport", "quality"],
  },
  {
    id: "manufacturing",
    title: "Manufacturing",
    icon: "factory",
    modules: ["production", "planning", "scheduling", "shopfloor", "processmfg", "lean", "engineering", "productcost", "eam"],
  },
  { id: "master-data", title: "Master data", icon: "database", modules: ["product", "party", "org"] },
  { id: "people", title: "People", icon: "users", modules: ["hr", "payroll", "expense"] },
  { id: "projects", title: "Projects and service", icon: "briefcase", modules: ["projects", "service"] },
  {
    id: "admin",
    title: "Administration",
    icon: "shield",
    modules: ["admin", "compliance", "workflow", "integration", "migration", "migration-factory", "metadata", "mobile-flows", "analytics", "copilot"],
  },
];

const OTHER: Omit<NavArea, "modules"> = { id: "more", title: "More", icon: "grid" };

export interface AreaGroup {
  area: Omit<NavArea, "modules">;
  modules: PassageModule[];
}

/** Groups the visible modules by area, in AREAS order; empty areas are left out and unmapped modules go to "More". */
export function groupByArea(modules: PassageModule[]): AreaGroup[] {
  const byId = new Map(modules.map((m) => [m.id, m]));
  const placed = new Set<string>();
  const groups: AreaGroup[] = [];
  for (const area of AREAS) {
    const members = area.modules.flatMap((id) => {
      const m = byId.get(id);
      if (m) placed.add(id);
      return m ? [m] : [];
    });
    if (members.length > 0) groups.push({ area: { id: area.id, title: area.title, icon: area.icon }, modules: members });
  }
  const rest = modules.filter((m) => !placed.has(m.id));
  if (rest.length > 0) groups.push({ area: OTHER, modules: rest });
  return groups;
}
