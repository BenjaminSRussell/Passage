// FX service (/api/v1/fx) for the demo: rate types, the stored rate history per pair (with one correction that
// superseded an earlier value), lookups that fall back to the inverse or a triangulated rate, conversion, manual
// rate entry and the provider import. Daily reference rates are EUR-based like the ECB's; the corporate monthly
// rates Cedar Ridge Pumps closes its books with are USD-based.
import type { ConvertResponse, HistoryEntry, ImportRun, PublishResponse, RateType, Resolution } from "../../modules/fx/api/client";
import { reply, route } from "../backend";
import { at, day, uid } from "../store";

const BASE = "/api/v1/fx";
const K_TYPE = 392;
const K_RATE = 393;

const types = new Map<string, RateType>(
  [
    { id: uid(K_TYPE, 1), code: "ECB", name: "ECB reference rate (daily)", allowInverse: true, provider: "ecb" },
    { id: uid(K_TYPE, 2), code: "CORP-M", name: "Corporate month-end rate", allowInverse: true },
    { id: uid(K_TYPE, 3), code: "BUDGET", name: "Budget rate 2026", allowInverse: false },
  ].map((t) => [t.code, t]),
);

interface StoredRate extends HistoryEntry {
  rateType: string;
  from: string;
  to: string;
}
let rateSeq = 0;
const rates: StoredRate[] = [];
const fixed = (n: number, to: string) => n.toFixed(to === "JPY" || to === "MXN" ? 4 : 6).replace(/0+$/, "").replace(/\.$/, "");

function store(rateType: string, from: string, to: string, validFrom: string, rate: string, source: string, createdBy: string, createdAt = at(0)): StoredRate {
  const versions = rates.filter((r) => r.rateType === rateType && r.from === from && r.to === to && r.validFrom === validFrom);
  const entry: StoredRate = { id: uid(K_RATE, ++rateSeq), version: versions.length + 1, validFrom, rate, source, createdAt, createdBy, rateType, from, to, ...(source === "ecb" ? { providerTimestamp: `${validFrom}T14:15:00Z`, payloadHash: `sha256:${(rateSeq * 2654435761 >>> 0).toString(16).padStart(8, "0")}` } : {}) };
  for (const old of versions.filter((v) => !v.supersededBy)) old.supersededBy = entry.id;
  rates.push(entry);
  return entry;
}

// Daily ECB rates for the last three weeks of business days, a gentle walk around today's level.
const ECB_BASE: Record<string, number> = { USD: 1.0842, GBP: 0.8431, CAD: 1.5128, MXN: 19.6215, JPY: 163.42, CHF: 0.9417 };
for (let back = 21; back >= 0; back--) {
  const date = day(-back);
  const weekday = new Date(`${date}T12:00:00Z`).getUTCDay();
  if (weekday === 0 || weekday === 6) continue;
  Object.entries(ECB_BASE).forEach(([to, base], i) => {
    const drift = Math.sin((back + i * 3) / 3.1) * 0.0045 + Math.cos(back / 5.3 + i) * 0.0021;
    store("ECB", "EUR", to, date, fixed(base * (1 + drift), to), "ecb", "fx-import", `${date}T16:05:00.000Z`);
  });
}
// Month-end corporate rates for the last six closes; September's CAD rate was corrected after a keying error.
const CORP_BASE: Record<string, number> = { EUR: 0.9218, CAD: 1.3954, MXN: 18.0975, GBP: 0.7781 };
for (let back = 6; back >= 1; back--) {
  const now = new Date(`${day(0)}T00:00:00Z`);
  const monthEnd = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - back + 1, 0)).toISOString().slice(0, 10);
  Object.entries(CORP_BASE).forEach(([to, base], i) => {
    const rate = fixed(base * (1 + Math.sin(back * 1.7 + i) * 0.012), to);
    const entered = new Date(Date.parse(`${monthEnd}T18:00:00Z`) + 86_400_000).toISOString();
    if (back === 2 && to === "CAD") {
      store("CORP-M", "USD", to, monthEnd, fixed(base * 1.0391, to), "manual", "megan.holt", entered);
      store("CORP-M", "USD", to, monthEnd, rate, "manual", "rachel.kim", new Date(Date.parse(entered) + 2 * 86_400_000).toISOString());
    } else {
      store("CORP-M", "USD", to, monthEnd, rate, "manual", "megan.holt", entered);
    }
  });
}
// Budget rates set once for the year.
for (const [to, rate] of Object.entries({ EUR: "0.93", CAD: "1.38", MXN: "18.5" })) store("BUDGET", "USD", to, `${day(0).slice(0, 4)}-01-01`, rate, "manual", "rachel.kim", `${Number(day(0).slice(0, 4)) - 1}-12-12T15:00:00.000Z`);

const problem = (status: number, code: string, detail: string, errors?: Record<string, string>) => reply.problem(status, code, detail, errors);
const live = (type: string, from: string, to: string, date: string) =>
  rates.filter((r) => r.rateType === type && r.from === from && r.to === to && !r.supersededBy && r.validFrom <= date).sort((a, b) => b.validFrom.localeCompare(a.validFrom))[0];

function resolve(type: string, from: string, to: string, date: string, via?: string): Resolution | undefined {
  if (from === to) return { rateType: type, from, to, date, path: "direct", rate: "1", hops: [] };
  const direct = live(type, from, to, date);
  if (direct) return { rateType: type, from, to, date, path: "direct", rate: direct.rate, hops: [{ from, to, rate: direct.rate, validFrom: direct.validFrom }] };
  const allowInverse = types.get(type)?.allowInverse ?? false;
  const inverse = allowInverse ? live(type, to, from, date) : undefined;
  if (inverse) return { rateType: type, from, to, date, path: "inverse", rate: fixed(1 / Number(inverse.rate), to), hops: [{ from: to, to: from, rate: inverse.rate, validFrom: inverse.validFrom }] };
  const pivot = via || (type === "ECB" ? "EUR" : "USD");
  const leg = (a: string, b: string) => {
    const d = live(type, a, b, date);
    if (d) return { rate: Number(d.rate), hop: { from: a, to: b, rate: d.rate, validFrom: d.validFrom } };
    const inv = allowInverse ? live(type, b, a, date) : undefined;
    return inv ? { rate: 1 / Number(inv.rate), hop: { from: b, to: a, rate: inv.rate, validFrom: inv.validFrom } } : undefined;
  };
  const first = leg(from, pivot);
  const second = leg(pivot, to);
  if (!first || !second) return undefined;
  return { rateType: type, from, to, date, path: "triangulated", rate: fixed(first.rate * second.rate, to), hops: [first.hop, second.hop] };
}

route("GET", `${BASE}/rate-types`, () => [...types.values()]);

route("POST", `${BASE}/rate-types`, ({ body }) => {
  const input = body as { code?: string; name?: string; allowInverse?: boolean; provider?: string };
  const code = String(input?.code ?? "").trim().toUpperCase();
  if (!/^[A-Z0-9-]{2,20}$/.test(code)) return problem(400, "FX-0001", "Use 2 to 20 capital letters, digits or hyphens for the code.", { code: "Use 2 to 20 capital letters, digits or hyphens." });
  if (types.has(code)) return problem(409, "FX-0002", `Rate type ${code} already exists.`, { code: "This code is taken." });
  const t: RateType = { id: uid(K_TYPE, types.size + 1), code, name: String(input.name ?? code), allowInverse: Boolean(input.allowInverse), ...(input.provider ? { provider: input.provider } : {}) };
  types.set(code, t);
  return reply.created(t);
});

route("PUT", `${BASE}/rate-types/{code}`, ({ params, body }) => {
  const t = types.get(params.code);
  if (!t) return problem(404, "FX-0404", `Rate type ${params.code} not found.`);
  const input = body as { name?: string; allowInverse?: boolean; provider?: string };
  const next: RateType = { id: t.id, code: t.code, name: input.name ?? t.name, allowInverse: input.allowInverse ?? t.allowInverse, ...(input.provider ? { provider: input.provider } : {}) };
  types.set(t.code, next);
  return next;
});

route("GET", `${BASE}/rates/history`, ({ query }) => {
  const type = query.get("type") ?? "";
  if (!types.has(type)) return problem(404, "FX-0404", `Rate type ${type} not found.`);
  const entries: HistoryEntry[] = rates
    .filter((r) => r.rateType === type && r.from === query.get("from") && r.to === query.get("to"))
    .sort((a, b) => b.validFrom.localeCompare(a.validFrom) || b.version - a.version)
    .map(({ rateType: _t, from: _f, to: _to, ...entry }) => entry);
  return { entries };
});

route("GET", `${BASE}/rates`, ({ query }) => {
  const type = query.get("type") ?? "";
  if (!types.has(type)) return problem(404, "FX-0404", `Rate type ${type} not found.`);
  const from = query.get("from") ?? "";
  const to = query.get("to") ?? "";
  const date = query.get("date") || day(0);
  const found = resolve(type, from, to, date, query.get("via") ?? undefined);
  return found ?? problem(404, "FX-0101", `No ${type} rate from ${from} to ${to} on ${date}, directly, inverted or through a reference currency.`);
});

route("POST", `${BASE}/rates`, ({ body }) => {
  const list = ((body as { rates?: { rateType: string; from: string; to: string; validFrom: string; rate: string }[] })?.rates ?? []);
  const out: PublishResponse["rates"] = [];
  for (const r of list) {
    if (!types.has(r.rateType)) return problem(404, "FX-0404", `Rate type ${r.rateType} not found.`);
    if (!(Number(r.rate) > 0)) return problem(400, "FX-0003", "The rate must be above zero.", { rate: "Enter a rate above zero." });
    const current = rates.find((x) => x.rateType === r.rateType && x.from === r.from && x.to === r.to && x.validFrom === r.validFrom && !x.supersededBy);
    if (current && Number(current.rate) === Number(r.rate)) {
      out.push({ id: current.id, version: current.version, superseded: false, unchanged: true });
      continue;
    }
    const entry = store(r.rateType, r.from, r.to, r.validFrom, r.rate, "manual", "jordan.avery");
    out.push({ id: entry.id, version: entry.version, superseded: current !== undefined, unchanged: false });
  }
  return reply.created({ rates: out });
});

const MINOR: Record<string, number> = { JPY: 0 };
route("POST", `${BASE}/convert`, ({ body }) => {
  const input = body as { rateType: string; to: string; date?: string; via?: string; amounts: { value: string; currency: string }[] };
  if (!types.has(input?.rateType)) return problem(404, "FX-0404", `Rate type ${input?.rateType} not found.`);
  const date = input.date || day(0);
  const results: ConvertResponse["results"] = [];
  for (const a of input.amounts ?? []) {
    const found = resolve(input.rateType, a.currency, input.to, date, input.via);
    if (!found) return problem(404, "FX-0101", `No ${input.rateType} rate from ${a.currency} to ${input.to} on ${date}.`);
    const places = MINOR[input.to] ?? 2;
    results.push({ amount: a, converted: { value: (Number(a.value) * Number(found.rate)).toFixed(places), currency: input.to }, path: found.path, rate: found.rate });
  }
  return { rateType: input.rateType, date, to: input.to, results } satisfies ConvertResponse;
});

route("POST", `${BASE}/providers/{provider}:import`, ({ params, body }) => {
  const rateType = (body as { rateType?: string })?.rateType ?? "ECB";
  // The ECB publishes around 16:00 CET on business days; before that, or on a weekend, the latest rates stand.
  let date = day(0);
  while ([0, 6].includes(new Date(`${date}T12:00:00Z`).getUTCDay())) date = new Date(Date.parse(date) - 86_400_000).toISOString().slice(0, 10);
  const already = rates.some((r) => r.rateType === rateType && r.validFrom === date && r.source === params.provider);
  const run: ImportRun = { runId: `IMP-${date.replace(/-/g, "")}-${params.provider.toUpperCase()}`, provider: params.provider, rateType, date, attempts: 1, imported: already ? 0 : 0, unchanged: already ? Object.keys(ECB_BASE).length : 0, alreadyDone: already, degraded: false, lastSuccessAt: at(-0.1) };
  if (!already) {
    for (const [to, base] of Object.entries(ECB_BASE)) store(rateType, "EUR", to, date, fixed(base, to), params.provider, "fx-import");
    run.imported = Object.keys(ECB_BASE).length;
  }
  return run;
});
