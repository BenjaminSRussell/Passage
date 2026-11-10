// Consolidation service (/api/v1/consol) for the demo: the Cedar Ridge Pumps group of three legal entities in two
// currencies, its account mapping, the collection of their ledger balances and the consolidated trial balance.
// Typed against the module's own client types so shape drift fails `tsc`.
import type { AccountMapRow, Collection, Group, GroupDetail, Holding, TrialBalance, TrialBalanceRow, UnmappedLine, Unit } from "../../modules/consolidation/api/groups";
import { LEGAL_ENTITY, LEGAL_ENTITY_DE, LEGAL_ENTITY_ES } from "../company";
import { reply, route } from "../backend";
import { at, newId, uid } from "../store";

const BASE = "/api/v1/consol";
const K_GROUP = 400;
const K_UNIT = 401;
const K_HOLDING = 402;
const K_COLLECTION = 403;

const GROUP_ID = uid(K_GROUP, 1);
const U_US = uid(K_UNIT, 1);
const U_DE = uid(K_UNIT, 2);
const U_ES = uid(K_UNIT, 3);

// Money is kept in cents so sums stay exact; the services send decimal strings.
const cents = (s: string): number => Math.round(Number(s) * 100);
const dec = (c: number): string => (c / 100).toFixed(2);

const groups: Group[] = [{ id: GROUP_ID, code: "CRP", name: "Cedar Ridge Pumps group", currency: "USD", createdBy: "dev", createdAt: at(-120) }];
const units: Unit[] = [
  { id: U_US, legalEntityId: LEGAL_ENTITY.id, ledgerId: "CRP-US-GL", currency: "USD", currentPercent: "100.0000", ownership: [{ id: uid(K_HOLDING, 1), effectiveFrom: "2020-01-01", percent: "100.0000" }] },
  { id: U_DE, legalEntityId: LEGAL_ENTITY_DE.id, ledgerId: "CRP-DE-GL", currency: "EUR", currentPercent: "80.0000", ownership: [{ id: uid(K_HOLDING, 2), effectiveFrom: "2022-04-01", percent: "80.0000" }] },
  {
    id: U_ES,
    legalEntityId: LEGAL_ENTITY_ES.id,
    ledgerId: "CRP-ES-GL",
    currency: "EUR",
    currentPercent: "60.0000",
    ownership: [
      { id: uid(K_HOLDING, 3), effectiveFrom: "2023-07-01", effectiveTo: "2026-01-01", percent: "40.0000" },
      { id: uid(K_HOLDING, 4), effectiveFrom: "2026-01-01", percent: "60.0000" },
    ],
  },
];
let accountMap: AccountMapRow[] = [
  { mainAccount: "1000", consolidationAccount: "CASH", accountType: "balance_sheet" },
  { mainAccount: "1200", consolidationAccount: "RECEIVABLES", accountType: "balance_sheet" },
  { mainAccount: "2000", consolidationAccount: "PAYABLES", accountType: "balance_sheet" },
  { mainAccount: "3000", consolidationAccount: "EQUITY", accountType: "balance_sheet" },
  { mainAccount: "4000", consolidationAccount: "REVENUE", accountType: "income" },
  { mainAccount: "5000", consolidationAccount: "COST-OF-SALES", accountType: "income" },
];

// What each unit's ledger holds, debit minus credit in the unit's currency; every ledger balances.
const LEDGERS: Record<string, [string, string][]> = {
  [U_US]: [["1000", "480900.00"], ["1200", "214600.00"], ["2000", "-161300.00"], ["3000", "-400000.00"], ["4000", "-1260000.00"], ["5000", "1124550.00"], ["9100", "1250.00"]],
  [U_DE]: [["1000", "96400.00"], ["1200", "58200.00"], ["2000", "-42000.00"], ["3000", "-80000.00"], ["4000", "-310000.00"], ["5000", "277400.00"]],
  [U_ES]: [["1000", "41000.00"], ["1200", "22500.00"], ["2000", "-18200.00"], ["3000", "-50000.00"], ["4000", "-120000.00"], ["5000", "124700.00"]],
};

interface Line {
  unit: string;
  mainAccount: string;
  consolidationAccount?: string;
  accountType?: "balance_sheet" | "income";
  unitCurrency: string;
  unitAmount: string;
  rate: string;
  groupAmount: string;
}
interface Stored extends Collection {
  lines: Line[];
}
const collections: Stored[] = [];

const period = (offsetMonths: number): string => {
  const d = new Date();
  d.setUTCDate(1);
  d.setUTCMonth(d.getUTCMonth() + offsetMonths);
  return d.toISOString().slice(0, 7);
};

function collect(per: string, rates: Record<string, { average: string; closing: string }>): Stored {
  const lines: Line[] = [];
  for (const u of units) {
    for (const [account, amount] of LEDGERS[u.id]) {
      const m = accountMap.find((a) => a.mainAccount === account);
      const r = u.currency === "USD" ? { average: "1", closing: "1" } : rates[u.currency];
      const rate = m?.accountType === "income" ? r.average : r.closing;
      lines.push({
        unit: u.id,
        mainAccount: account,
        consolidationAccount: m?.consolidationAccount,
        accountType: m?.accountType,
        unitCurrency: u.currency,
        unitAmount: amount,
        rate,
        groupAmount: dec(Math.round(cents(amount) * Number(rate))),
      });
    }
  }
  const version = collections.filter((c) => c.period === per).length + 1;
  const stored: Stored = { id: version === 1 && collections.length === 0 ? uid(K_COLLECTION, 1) : newId(), groupId: GROUP_ID, period: per, version, currency: "USD", createdBy: "dev", createdAt: at(0), lineCount: lines.length, unmappedCount: lines.filter((l) => !l.consolidationAccount).length, lines };
  collections.unshift(stored);
  return stored;
}
collect(period(-1), { EUR: { average: "1.0840", closing: "1.0950" } });

const header = (c: Stored): Collection => ({ id: c.id, groupId: c.groupId, period: c.period, version: c.version, currency: c.currency, createdBy: c.createdBy, createdAt: c.createdAt });

function trialBalance(c: Stored): TrialBalance {
  const rows = new Map<string, { type: "balance_sheet" | "income"; total: number; units: Record<string, number> }>();
  const unmapped: UnmappedLine[] = [];
  const diffs: Record<string, number> = {};
  let total = 0;
  for (const l of c.lines) {
    const g = cents(l.groupAmount);
    total += g;
    diffs[l.unit] = (diffs[l.unit] ?? 0) + g;
    if (!l.consolidationAccount || !l.accountType) {
      unmapped.push({ unit: l.unit, mainAccount: l.mainAccount, unitCurrency: l.unitCurrency, unitAmount: l.unitAmount, rate: l.rate, groupAmount: l.groupAmount });
      continue;
    }
    const row = rows.get(l.consolidationAccount) ?? { type: l.accountType, total: 0, units: {} };
    row.total += g;
    row.units[l.unit] = (row.units[l.unit] ?? 0) + g;
    rows.set(l.consolidationAccount, row);
  }
  const out: TrialBalanceRow[] = [...rows.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([account, r]) => ({ account, accountType: r.type, total: dec(r.total), units: Object.fromEntries(Object.entries(r.units).map(([u, v]) => [u, dec(v)])) }));
  return { ...header(c), rows: out, unmapped, total: dec(total), unitDifferences: Object.fromEntries(Object.entries(diffs).map(([u, v]) => [u, dec(v)])) };
}

const detail = (g: Group): GroupDetail => ({ ...g, units: structuredClone(units) });

route("GET", `${BASE}/groups`, () => ({ items: groups.map((g) => ({ ...g, unitCount: g.id === GROUP_ID ? units.length : 0 })) }));
route("POST", `${BASE}/groups`, ({ body }) => {
  const b = body as { code: string; name: string; currency: string };
  if (groups.some((g) => g.code === b.code)) return reply.problem(409, "CNS-0925", `A consolidation group with code ${b.code} already exists`);
  const g: Group = { id: newId(), code: b.code, name: b.name, currency: b.currency, createdBy: "dev", createdAt: at(0) };
  groups.push(g);
  return reply.created(g, { Location: `${BASE}/groups/${g.id}` });
});
route("GET", `${BASE}/groups/{id}/account-map`, ({ params }) => (groups.some((g) => g.id === params.id) ? { accounts: structuredClone(accountMap) } : reply.problem(404, "CNS-0924", "No consolidation group with that id")));
route("PUT", `${BASE}/groups/{id}/account-map`, ({ body }) => {
  accountMap = (body as { accounts: AccountMapRow[] }).accounts;
  return { accounts: structuredClone(accountMap) };
});
route("GET", `${BASE}/groups/{id}/collections`, ({ params }) => ({ items: collections.filter((c) => c.groupId === params.id).map(header) }));
route("POST", `${BASE}/groups/{id}/collections`, ({ params, body }) => {
  if (params.id !== GROUP_ID) return reply.problem(422, "CNS-0927", "The demo group has the units to collect; this one has none");
  const b = body as { period: string; rates: { currency: string; average: string; closing: string }[] };
  const rates = Object.fromEntries(b.rates.map((r) => [r.currency, r]));
  if (!rates.EUR) return reply.problem(422, "CNS-0927", "Unit CRP-DE is in EUR and needs a positive average and closing rate to USD");
  const c = collect(b.period, rates);
  return reply.created({ ...header(c), lineCount: c.lineCount, unmappedCount: c.unmappedCount }, { Location: `${BASE}/collections/${c.id}` });
});
route("POST", `${BASE}/groups/{id}/units`, ({ params, body }) => {
  const b = body as { legalEntityId: string; ledgerId: string; currency: string };
  if (params.id !== GROUP_ID) return reply.problem(404, "CNS-0924", "No consolidation group with that id");
  if (units.some((u) => u.legalEntityId === b.legalEntityId)) return reply.problem(409, "CNS-0926", "That legal entity is already a unit of the group");
  const u: Unit = { id: newId(), legalEntityId: b.legalEntityId, ledgerId: b.ledgerId, currency: b.currency, currentPercent: "0.0000", ownership: [] };
  units.push(u);
  LEDGERS[u.id] = [];
  return reply.created(u);
});
route("POST", `${BASE}/groups/{id}/units/{unitId}/ownership`, ({ params, body }) => {
  const b = body as { holderUnitId?: string; effectiveFrom: string; percent: string };
  const u = units.find((x) => x.id === params.unitId);
  if (!u) return reply.problem(404, "CNS-0924", "No consolidation group or unit with that id");
  const others = u.ownership.filter((h) => h.holderUnitId !== (b.holderUnitId || undefined));
  const held = others.filter((h) => !h.effectiveTo).reduce((sum, h) => sum + Number(h.percent), 0) + Number(b.percent);
  if (held > 100) return reply.problem(422, "CNS-0904", `Ownership totals ${held.toFixed(2)}% on ${b.effectiveFrom}`);
  for (const h of u.ownership) if (h.holderUnitId === (b.holderUnitId || undefined) && !h.effectiveTo && h.effectiveFrom < b.effectiveFrom) h.effectiveTo = b.effectiveFrom;
  const holding: Holding = { id: newId(), holderUnitId: b.holderUnitId || undefined, effectiveFrom: b.effectiveFrom, percent: Number(b.percent).toFixed(4) };
  u.ownership.push(holding);
  u.currentPercent = held.toFixed(4);
  return reply.created(structuredClone(u));
});
route("GET", `${BASE}/groups/{id}`, ({ params }) => {
  const g = groups.find((x) => x.id === params.id);
  return g ? detail(g) : reply.problem(404, "CNS-0924", "No consolidation group with that id");
});
route("GET", `${BASE}/collections/{id}/trial-balance`, ({ params }) => {
  const c = collections.find((x) => x.id === params.id);
  return c ? trialBalance(c) : reply.problem(404, "CNS-0928", "No collection with that id");
});
route("GET", `${BASE}/collections/{id}`, ({ params }) => {
  const c = collections.find((x) => x.id === params.id);
  return c ? { ...header(c), lineCount: c.lineCount, unmappedCount: c.unmappedCount, lines: c.lines } : reply.problem(404, "CNS-0928", "No collection with that id");
});
