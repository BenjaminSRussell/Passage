// The fictional company every demo screen shows: Cedar Ridge Pumps, a mid-size maker of industrial pumps.
// Domain handlers map these records into each service's response shape, so names, codes and ids agree
// across sales, purchasing, inventory, planning and production. Add to this file rather than inventing
// master data inside a handler.

import { uid } from "./store";

/** uid() kinds. Keep them unique; handlers may claim kinds from 100 up for their own documents. */
export const KIND = {
  legalEntity: 1,
  site: 2,
  warehouse: 3,
  item: 4,
  customer: 5,
  vendor: 6,
  resource: 7,
  worker: 8,
  location: 9,
  bom: 10,
  route: 11,
} as const;

export const TENANT_ID = "00000000-0000-0000-0000-000000000001";
/** Same id the dev identity headers use, so any page that defaults to it lines up. */
export const LEGAL_ENTITY = { id: "00000000-0000-0000-0000-0000000000e1", code: "CRP-US", name: "Cedar Ridge Pumps, Inc.", currency: "USD", country: "US" };

export const USER = { id: "dev", name: "Jordan Avery", email: "jordan.avery@cedarridgepumps.example" };

export interface Site {
  id: string;
  code: string;
  name: string;
}
export const SITES: Site[] = [
  { id: uid(KIND.site, 1), code: "CR", name: "Cedar Rapids plant" },
  { id: uid(KIND.site, 2), code: "DAL", name: "Dallas distribution center" },
];
export const [PLANT, DC] = SITES;

export interface Warehouse {
  id: string;
  code: string;
  name: string;
  siteId: string;
}
export const WAREHOUSES: Warehouse[] = [
  { id: uid(KIND.warehouse, 1), code: "CR-RM", name: "Raw materials", siteId: PLANT.id },
  { id: uid(KIND.warehouse, 2), code: "CR-WIP", name: "Production floor", siteId: PLANT.id },
  { id: uid(KIND.warehouse, 3), code: "CR-FG", name: "Finished goods", siteId: PLANT.id },
  { id: uid(KIND.warehouse, 4), code: "DAL-FG", name: "Dallas finished goods", siteId: DC.id },
];
export const [WH_RAW, WH_WIP, WH_FG, WH_DAL] = WAREHOUSES;

export type ItemKind = "finished" | "subassembly" | "component" | "raw";
export interface Item {
  id: string;
  number: string;
  name: string;
  kind: ItemKind;
  unit: string;
  /** Standard cost per unit, USD. */
  cost: number;
  /** List price per unit, USD; finished goods and spares only. */
  price?: number;
  /** Days to buy (purchased) or make (manufactured). */
  leadTimeDays: number;
  make: boolean;
  /** Preferred vendor index into VENDORS for purchased items. */
  vendor?: number;
  /** On hand at the plant, in unit. */
  onHand: number;
  safetyStock: number;
}

function item(n: number, number: string, name: string, kind: ItemKind, unit: string, cost: number, leadTimeDays: number, onHand: number, safetyStock: number, extra: Partial<Item> = {}): Item {
  return { id: uid(KIND.item, n), number, name, kind, unit, cost, leadTimeDays, make: kind === "finished" || kind === "subassembly", onHand, safetyStock, ...extra };
}

export const ITEMS: Item[] = [
  item(1, "CP-200", "Centrifugal pump, 2 in, 5 HP", "finished", "ea", 1180, 6, 42, 20, { price: 2450 }),
  item(2, "CP-400", "Centrifugal pump, 4 in, 15 HP", "finished", "ea", 2960, 8, 11, 10, { price: 5890 }),
  item(3, "SP-150", "Submersible well pump, 1.5 HP", "finished", "ea", 640, 5, 87, 40, { price: 1290 }),
  item(4, "DP-100", "Air-operated diaphragm pump, 1 in", "finished", "ea", 815, 7, 6, 15, { price: 1745 }),
  item(5, "BP-300", "Booster pump skid, triplex", "finished", "ea", 9850, 15, 2, 1, { price: 18900 }),
  item(10, "SA-IMP-06", "Impeller assembly, 6 in bronze", "subassembly", "ea", 212, 3, 64, 30),
  item(11, "SA-MTR-05", "Motor and coupling assembly, 5 HP", "subassembly", "ea", 438, 2, 18, 20),
  item(12, "SA-VOL-04", "Volute housing, machined, 4 in", "subassembly", "ea", 365, 4, 9, 12),
  item(20, "MTR-5HP-TEFC", "Motor, 5 HP TEFC, 1800 rpm", "component", "ea", 395, 21, 22, 15, { vendor: 1 }),
  item(21, "MTR-15HP-TEFC", "Motor, 15 HP TEFC, 1800 rpm", "component", "ea", 1120, 28, 4, 6, { vendor: 1 }),
  item(22, "SEAL-MK-125", "Mechanical seal kit, 1.25 in", "component", "ea", 48.5, 10, 140, 80, { vendor: 2, price: 129 }),
  item(23, "BRG-6206", "Ball bearing 6206-2RS", "component", "ea", 7.4, 7, 610, 300, { vendor: 5, price: 24 }),
  item(24, "FST-KIT-A", "Fastener kit, stainless, pump A", "component", "kit", 12.75, 5, 220, 100, { vendor: 3 }),
  item(25, "CST-VOL-04", "Volute casting, ductile iron, 4 in", "component", "ea", 168, 35, 31, 25, { vendor: 0 }),
  item(26, "CST-IMP-06", "Impeller casting, bronze, 6 in", "component", "ea", 96, 30, 12, 40, { vendor: 0 }),
  item(30, "RM-SS316-BAR", "Stainless 316 round bar, 1.25 in", "raw", "ft", 18.2, 12, 940, 400, { vendor: 4 }),
  item(31, "RM-EPOXY-BLU", "Epoxy coating, Cedar blue", "raw", "gal", 64, 9, 38, 20, { vendor: 4 }),
];
export const itemByNumber = (number: string): Item => {
  const found = ITEMS.find((i) => i.number === number);
  if (!found) throw new Error(`demo item ${number} not in company.ts`);
  return found;
};

/** Bill of materials per manufactured item: component number and quantity per unit. */
export const BOMS: Record<string, { component: string; qty: number }[]> = {
  "CP-200": [
    { component: "SA-IMP-06", qty: 1 },
    { component: "SA-MTR-05", qty: 1 },
    { component: "SEAL-MK-125", qty: 1 },
    { component: "BRG-6206", qty: 2 },
    { component: "FST-KIT-A", qty: 1 },
    { component: "RM-EPOXY-BLU", qty: 0.25 },
  ],
  "CP-400": [
    { component: "SA-VOL-04", qty: 1 },
    { component: "MTR-15HP-TEFC", qty: 1 },
    { component: "SA-IMP-06", qty: 1 },
    { component: "SEAL-MK-125", qty: 1 },
    { component: "BRG-6206", qty: 2 },
    { component: "FST-KIT-A", qty: 2 },
    { component: "RM-EPOXY-BLU", qty: 0.4 },
  ],
  "SA-IMP-06": [
    { component: "CST-IMP-06", qty: 1 },
    { component: "RM-SS316-BAR", qty: 1.5 },
  ],
  "SA-MTR-05": [
    { component: "MTR-5HP-TEFC", qty: 1 },
    { component: "FST-KIT-A", qty: 1 },
  ],
  "SA-VOL-04": [{ component: "CST-VOL-04", qty: 1 }],
};

export interface Party {
  id: string;
  number: string;
  name: string;
  city: string;
  state: string;
  /** Credit limit, USD; customers only. */
  creditLimit?: number;
  paymentTerms: string;
}

/** Party ids start with the account number (c0010021 for C-10021), so tables that show short ids stay readable. */
const partyId = (prefix: "c" | "a", number: string) => `${prefix}${number.replace(/\D/g, "").padStart(7, "0")}-0000-4000-8000-000000000000`;

export const CUSTOMERS: Party[] = [
  { id: partyId("c", "C-10021"), number: "C-10021", name: "Bluewater Municipal Utilities", city: "Duluth", state: "MN", creditLimit: 250000, paymentTerms: "Net 30" },
  { id: partyId("c", "C-10048"), number: "C-10048", name: "Harlan Agricultural Co-op", city: "Harlan", state: "IA", creditLimit: 120000, paymentTerms: "Net 45" },
  { id: partyId("c", "C-10055"), number: "C-10055", name: "Summit Mining Supply", city: "Elko", state: "NV", creditLimit: 400000, paymentTerms: "Net 30" },
  { id: partyId("c", "C-10063"), number: "C-10063", name: "Coastal Desalination Partners", city: "Corpus Christi", state: "TX", creditLimit: 600000, paymentTerms: "Net 60" },
  { id: partyId("c", "C-10077"), number: "C-10077", name: "Prairie Irrigation Systems", city: "Kearney", state: "NE", creditLimit: 90000, paymentTerms: "Net 30" },
  { id: partyId("c", "C-10082"), number: "C-10082", name: "Metro Facilities Group", city: "Chicago", state: "IL", creditLimit: 150000, paymentTerms: "Net 30" },
];

export const VENDORS: Party[] = [
  { id: partyId("a", "V-20004"), number: "V-20004", name: "Ironbridge Foundry", city: "Birmingham", state: "AL", paymentTerms: "Net 45" },
  { id: partyId("a", "V-20011"), number: "V-20011", name: "Volta Motor Works", city: "Fort Wayne", state: "IN", paymentTerms: "Net 30" },
  { id: partyId("a", "V-20017"), number: "V-20017", name: "Keystone Seals & Gaskets", city: "Erie", state: "PA", paymentTerms: "Net 30" },
  { id: partyId("a", "V-20023"), number: "V-20023", name: "Allied Fastener Supply", city: "Rockford", state: "IL", paymentTerms: "2% 10 Net 30" },
  { id: partyId("a", "V-20030"), number: "V-20030", name: "Riverbend Steel Service", city: "Davenport", state: "IA", paymentTerms: "Net 30" },
  { id: partyId("a", "V-20036"), number: "V-20036", name: "Precision Bearing Co.", city: "Toledo", state: "OH", paymentTerms: "Net 30" },
];

export interface Resource {
  id: string;
  code: string;
  name: string;
  /** Available hours per day. */
  hoursPerDay: number;
  /** Load over the next two weeks as a fraction of capacity, for capacity and bottleneck screens. */
  load: number;
}
export const RESOURCES: Resource[] = [
  { id: uid(KIND.resource, 1), code: "MCH-01", name: "CNC machining cell", hoursPerDay: 16, load: 0.94 },
  { id: uid(KIND.resource, 2), code: "BAL-01", name: "Impeller balancing", hoursPerDay: 8, load: 1.12 },
  { id: uid(KIND.resource, 3), code: "ASM-01", name: "Assembly line 1", hoursPerDay: 16, load: 0.78 },
  { id: uid(KIND.resource, 4), code: "ASM-02", name: "Assembly line 2 (skids)", hoursPerDay: 8, load: 0.55 },
  { id: uid(KIND.resource, 5), code: "TST-01", name: "Hydrostatic test stand", hoursPerDay: 16, load: 0.86 },
  { id: uid(KIND.resource, 6), code: "PNT-01", name: "Paint booth", hoursPerDay: 8, load: 0.67 },
  { id: uid(KIND.resource, 7), code: "PCK-01", name: "Packing and crating", hoursPerDay: 8, load: 0.48 },
];

/** Routing per manufactured item: resource code and run hours per unit, in sequence. */
export const ROUTES: Record<string, { op: number; resource: string; name: string; setupHours: number; runHours: number }[]> = {
  "CP-200": [
    { op: 10, resource: "ASM-01", name: "Assemble pump", setupHours: 0.5, runHours: 1.2 },
    { op: 20, resource: "TST-01", name: "Hydrostatic and performance test", setupHours: 0.25, runHours: 0.6 },
    { op: 30, resource: "PNT-01", name: "Coat and cure", setupHours: 0.5, runHours: 0.4 },
    { op: 40, resource: "PCK-01", name: "Pack and label", setupHours: 0, runHours: 0.3 },
  ],
  "CP-400": [
    { op: 10, resource: "ASM-01", name: "Assemble pump", setupHours: 0.75, runHours: 2.4 },
    { op: 20, resource: "TST-01", name: "Hydrostatic and performance test", setupHours: 0.5, runHours: 1.1 },
    { op: 30, resource: "PNT-01", name: "Coat and cure", setupHours: 0.5, runHours: 0.7 },
    { op: 40, resource: "PCK-01", name: "Crate", setupHours: 0, runHours: 0.5 },
  ],
  "SA-IMP-06": [
    { op: 10, resource: "MCH-01", name: "Machine bore and hub", setupHours: 1.0, runHours: 0.5 },
    { op: 20, resource: "BAL-01", name: "Dynamic balance", setupHours: 0.25, runHours: 0.35 },
  ],
  "SA-VOL-04": [{ op: 10, resource: "MCH-01", name: "Machine volute faces", setupHours: 1.5, runHours: 0.9 }],
  "SA-MTR-05": [{ op: 10, resource: "ASM-01", name: "Mount motor and coupling", setupHours: 0.25, runHours: 0.4 }],
};

export interface Worker {
  id: string;
  number: string;
  name: string;
  resource: string;
}
export const WORKERS: Worker[] = [
  { id: uid(KIND.worker, 1), number: "W-0112", name: "Dana Kowalski", resource: "MCH-01" },
  { id: uid(KIND.worker, 2), number: "W-0127", name: "Luis Ortega", resource: "ASM-01" },
  { id: uid(KIND.worker, 3), number: "W-0133", name: "Priya Natarajan", resource: "TST-01" },
  { id: uid(KIND.worker, 4), number: "W-0141", name: "Marcus Bell", resource: "ASM-01" },
  { id: uid(KIND.worker, 5), number: "W-0158", name: "Hannah Lindqvist", resource: "PNT-01" },
  { id: uid(KIND.worker, 6), number: "W-0164", name: "Samir Haddad", resource: "BAL-01" },
];

export const resourceByCode = (code: string): Resource => {
  const found = RESOURCES.find((r) => r.code === code);
  if (!found) throw new Error(`demo resource ${code} not in company.ts`);
  return found;
};
