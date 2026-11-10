// Tax service (/api/v1/tax) for the demo: US sales tax for the plants and customers of Cedar Ridge Pumps, German VAT
// and EU sales for its EU registration, the VAT return, the EU sales list and the Intrastat journals.
// Typed against the module's own client types so shape drift fails `tsc`.
import type { Esl, IntrastatJournal, IntrastatLine, Jurisdiction, MasterEntry, MasterKind, TaxCode, TaxGroup, VatBox, VatReturn } from "../../../modules/tax/api/client";
import { reply, route } from "../../backend";
import { at, dec, uid } from "../../store";

const BASE = "/api/v1/tax";
const K_JOURNAL = 360;

const notFound = (what: string) => reply.problem(404, "COM-0404", `${what} not found`);
const invalid = (field: string, detail: string) => reply.problem(400, "COM-0001", detail, { [field]: detail });
const inUse = (detail: string) => reply.problem(422, "TAX-0013", detail);

// ---- jurisdictions

const jurisdictions: Jurisdiction[] = [
  { code: "US", name: "United States", kind: "country", country: "US", parent_code: "" },
  { code: "US-IA", name: "Iowa", kind: "state", country: "US", parent_code: "US" },
  { code: "US-IA-LIN", name: "Linn County, Iowa", kind: "county", country: "US", parent_code: "US-IA" },
  { code: "US-IA-CR", name: "Cedar Rapids", kind: "city", country: "US", parent_code: "US-IA-LIN" },
  { code: "US-TX", name: "Texas", kind: "state", country: "US", parent_code: "US" },
  { code: "US-TX-DAL", name: "Dallas", kind: "city", country: "US", parent_code: "US-TX" },
  { code: "US-TX-DART", name: "Dallas Area Rapid Transit district", kind: "district", country: "US", parent_code: "US-TX-DAL" },
  { code: "US-MN", name: "Minnesota", kind: "state", country: "US", parent_code: "US" },
  { code: "DE", name: "Germany", kind: "country", country: "DE", parent_code: "" },
  { code: "FR", name: "France", kind: "country", country: "FR", parent_code: "" },
  { code: "NL", name: "Netherlands", kind: "country", country: "NL", parent_code: "" },
];

// ---- tax codes

const code = (c: string, name: string, jurisdiction: string, rates: [string, string | null, string][], extra: Partial<TaxCode> = {}): TaxCode => ({
  code: c,
  name,
  jurisdiction,
  mechanism: "standard",
  calc_basis: "line",
  rounding: "per_line",
  compound_sequence: 0,
  recoverable_pct: null,
  rates: rates.map(([valid_from, valid_to, rate_pct]) => ({ valid_from, valid_to, rate_pct })),
  ...extra,
});

const GERMAN_STANDARD: [string, string | null, string][] = [
  ["2007-01-01", "2020-06-30", "19.0000"],
  ["2020-07-01", "2020-12-31", "16.0000"],
  ["2021-01-01", null, "19.0000"],
];

const codes: TaxCode[] = [
  code("IA-ST", "Iowa sales tax", "US-IA", [["2008-07-01", null, "6.0000"]]),
  code("IA-LIN-LOST", "Linn County local option tax", "US-IA-LIN", [["2009-01-01", null, "1.0000"]]),
  code("TX-ST", "Texas sales tax", "US-TX", [["1990-10-01", null, "6.2500"]]),
  code("TX-DAL-CITY", "Dallas city sales tax", "US-TX-DAL", [["1990-10-01", null, "1.0000"]]),
  code("TX-DAL-DART", "Dallas transit sales tax", "US-TX-DART", [["1990-10-01", null, "1.0000"]]),
  code("MN-ST", "Minnesota sales tax", "US-MN", [["2009-07-01", null, "6.8750"]]),
  code("US-USE", "Use tax on out-of-state purchases", "US-IA", [["2008-07-01", null, "6.0000"]], { mechanism: "use_tax" }),
  code("DE-VAT19", "German VAT 19%", "DE", GERMAN_STANDARD, { recoverable_pct: "100.0000" }),
  code("DE-VAT7", "German VAT 7%", "DE", [["2007-01-01", "2020-06-30", "7.0000"], ["2020-07-01", "2020-12-31", "5.0000"], ["2021-01-01", null, "7.0000"]], { recoverable_pct: "100.0000" }),
  code("DE-IC", "Intra-community supply, 0%", "DE", [["2007-01-01", null, "0.0000"]], { mechanism: "reverse_charge" }),
  code("FR-VAT20", "French VAT 20%", "FR", [["2014-01-01", null, "20.0000"]]),
  code("NL-VAT21", "Dutch VAT 21%", "NL", [["2012-10-01", null, "21.0000"]]),
];

// ---- tax groups

const US_SALES = ["IA-ST", "IA-LIN-LOST", "TX-ST", "TX-DAL-CITY", "TX-DAL-DART", "MN-ST"];
const EU_SALES = ["DE-VAT19", "DE-VAT7", "DE-IC", "FR-VAT20", "NL-VAT21"];

const groups: TaxGroup[] = [
  { kind: "party", code: "IA-CUSTOMER", name: "Iowa customers", tax_codes: ["IA-ST", "IA-LIN-LOST"] },
  { kind: "party", code: "TX-CUSTOMER", name: "Texas customers (Dallas)", tax_codes: ["TX-ST", "TX-DAL-CITY", "TX-DAL-DART"] },
  { kind: "party", code: "MN-CUSTOMER", name: "Minnesota customers", tax_codes: ["MN-ST"] },
  { kind: "party", code: "US-RESELLER", name: "US resellers with a resale certificate", tax_codes: [] },
  { kind: "party", code: "DE-BUSINESS", name: "German customers", tax_codes: ["DE-VAT19", "DE-VAT7"] },
  { kind: "party", code: "EU-BUSINESS", name: "EU businesses with a VAT number", tax_codes: ["DE-IC"] },
  { kind: "item", code: "PUMPS", name: "Pumps and spare parts", tax_codes: [...US_SALES, ...EU_SALES] },
  { kind: "item", code: "INSTALL", name: "Installation and commissioning", tax_codes: ["IA-ST", "IA-LIN-LOST", "MN-ST", "DE-VAT19"] },
  { kind: "item", code: "EXEMPT", name: "Exempt items", tax_codes: [] },
];

// ---- VAT boxes (German return)

const boxes: VatBox[] = [
  { box: "81", label: "Supplies taxed at 19%", kind: "base", sort: 10, codes: [{ code: "DE-VAT19", direction: "output" }] },
  { box: "86", label: "Supplies taxed at 7%", kind: "base", sort: 20, codes: [{ code: "DE-VAT7", direction: "output" }] },
  { box: "41", label: "Tax-free supplies to other EU countries", kind: "base", sort: 30, codes: [{ code: "DE-IC", direction: "output" }] },
  { box: "81T", label: "Output tax at 19%", kind: "tax", sort: 40, codes: [{ code: "DE-VAT19", direction: "output" }] },
  { box: "86T", label: "Output tax at 7%", kind: "tax", sort: 50, codes: [{ code: "DE-VAT7", direction: "output" }] },
  { box: "66", label: "Input tax from vendor invoices", kind: "tax", sort: 60, codes: [{ code: "DE-VAT19", direction: "input" }, { code: "DE-VAT7", direction: "input" }] },
  { box: "83", label: "Tax to pay (or refund)", kind: "payable", sort: 70, codes: [] },
];

let setting = { home_country: "DE", vat_id: "DE812345678" };

// ---- recorded tax transactions behind the return and the sales list

interface Txn {
  code: string;
  direction: "output" | "input" | "use";
  date: string;
  tax: number;
  base: number | null;
  country?: string;
  vatId?: string;
  kind?: string;
}

const monthDay = (offsetMonths: number, d: number): string => {
  const now = new Date();
  return new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() + offsetMonths, d)).toISOString().slice(0, 10);
};

function monthTxns(m: number): Txn[] {
  const out = (c: string, d: number, base: number, rate: number, extra: Partial<Txn> = {}): Txn => ({ code: c, direction: "output", date: monthDay(m, d), base, tax: Math.round(base * rate * 100) / 100, country: "DE", ...extra });
  const inp = (c: string, d: number, base: number, rate: number): Txn => ({ code: c, direction: "input", date: monthDay(m, d), base, tax: Math.round(base * rate * 100) / 100 });
  return [
    out("DE-VAT19", 3, 48200, 0.19),
    out("DE-VAT19", 8, 31750, 0.19),
    out("DE-VAT19", 12, 52600, 0.19),
    out("DE-VAT19", 19, 27400, 0.19),
    out("DE-VAT19", 24, 24550, 0.19),
    out("DE-VAT7", 10, 7400, 0.07),
    out("DE-VAT7", 22, 5000, 0.07),
    out("DE-IC", 5, 64800, 0, { country: "FR", vatId: "FR40303265045", kind: "goods" }),
    out("DE-IC", 14, 38900, 0, { country: "FR", vatId: "FR40303265045", kind: "goods" }),
    out("DE-IC", 9, 91250, 0, { country: "NL", vatId: "NL004495445B01", kind: "goods" }),
    out("DE-IC", 17, 12600, 0, { country: "NL", vatId: "NL004495445B01", kind: "services" }),
    out("DE-IC", 26, 27300, 0, { country: "PL", vatId: "PL5260250995", kind: "goods" }),
    out("DE-IC", 21, 8800, 0, { country: "IT", vatId: "", kind: "goods" }),
    inp("DE-VAT19", 4, 26900, 0.19),
    inp("DE-VAT19", 15, 18400, 0.19),
    inp("DE-VAT19", 27, 15900, 0.19),
    inp("DE-VAT7", 11, 3100, 0.07),
  ];
}

const transactions: Txn[] = [monthTxns(0), monthTxns(-1), monthTxns(-2)].flat();

function periodOf(query: URLSearchParams): { from: string; to: string } | null {
  const from = query.get("period_from") ?? "";
  const to = query.get("period_to") ?? "";
  return /^\d{4}-\d{2}-\d{2}$/.test(from) && /^\d{4}-\d{2}-\d{2}$/.test(to) && to >= from ? { from, to } : null;
}

function vatReturn(from: string, to: string): VatReturn {
  const inPeriod = transactions.filter((t) => t.date >= from && t.date <= to);
  const feeds = (b: VatBox, t: Txn) => b.codes.some((c) => c.code === t.code && c.direction === t.direction);
  const payable = inPeriod.reduce((s, t) => s + (t.direction === "input" ? -t.tax : t.tax), 0);
  return {
    period_from: from,
    period_to: to,
    boxes: boxes.map((b) => ({
      box: b.box,
      label: b.label,
      kind: b.kind,
      amount: dec(b.kind === "payable" ? payable : inPeriod.filter((t) => feeds(b, t)).reduce((s, t) => s + (b.kind === "base" ? (t.base ?? 0) : t.tax), 0), 2),
    })),
    transactions: inPeriod.length,
    missing_base: 0,
    unmapped: 0,
  };
}

const EU = new Set(["AT", "BE", "BG", "HR", "CY", "CZ", "DK", "EE", "FI", "FR", "DE", "GR", "HU", "IE", "IT", "LV", "LT", "LU", "MT", "NL", "PL", "PT", "RO", "SK", "SI", "ES", "SE"]);

function esl(from: string, to: string): Esl {
  const rows = new Map<string, Esl["rows"][number]>();
  let missingVat = 0;
  let total = 0;
  for (const t of transactions) {
    if (t.direction !== "output" || t.date < from || t.date > to || !t.country || !EU.has(t.country) || t.country === setting.home_country) continue;
    if (!t.vatId) {
      missingVat++;
      continue;
    }
    const kind = t.kind ?? "goods";
    const key = `${t.country}|${t.vatId}|${kind}`;
    const row = rows.get(key) ?? { country: t.country, vat_id: t.vatId, indicator: kind === "services" ? "S" : "L", supply_kind: kind, base: "0", transactions: 0 };
    row.base = dec(Number(row.base) + (t.base ?? 0), 2);
    row.transactions++;
    total += t.base ?? 0;
    rows.set(key, row);
  }
  return {
    period_from: from,
    period_to: to,
    home_country: setting.home_country,
    vat_id: setting.vat_id,
    rows: [...rows.values()].sort((a, b) => a.country.localeCompare(b.country) || a.vat_id.localeCompare(b.vat_id) || a.supply_kind.localeCompare(b.supply_kind)),
    total: dec(total),
    missing_vat_id: missingVat,
    missing_base: 0,
  };
}

// ---- Intrastat master data

const entry = (kind: MasterKind, c: string, description: string, attrs: Record<string, string> = {}): MasterEntry => ({ kind, code: c, description, attrs });
const euCountry = (c: string, name: string, intrastat?: string) => entry("country", c, name, { eu_member: "true", ...(intrastat ? { intrastat_code: intrastat } : {}) });

const master: MasterEntry[] = [
  entry("commodity", "84137021", "Single-stage centrifugal pumps, close-coupled", { supplementary_unit: "p/st" }),
  entry("commodity", "84137081", "Other centrifugal pumps, multi-stage", { supplementary_unit: "p/st" }),
  entry("commodity", "84139100", "Parts of pumps"),
  entry("commodity", "40169300", "Rubber gaskets, washers and other seals"),
  entry("transaction_code", "11", "Outright purchase or sale"),
  entry("transaction_code", "21", "Return of goods"),
  entry("transaction_code", "31", "Goods for processing or repair"),
  entry("transport_mode", "1", "Sea"),
  entry("transport_mode", "2", "Rail"),
  entry("transport_mode", "3", "Road"),
  entry("transport_mode", "4", "Air"),
  entry("port", "DEHAM", "Hamburg", { country: "DE" }),
  entry("port", "NLRTM", "Rotterdam", { country: "NL" }),
  entry("delivery_terms", "EXW", "Ex works"),
  entry("delivery_terms", "FCA", "Free carrier"),
  entry("delivery_terms", "DAP", "Delivered at place"),
  entry("delivery_terms", "DDP", "Delivered duty paid"),
  euCountry("DE", "Germany"),
  euCountry("FR", "France"),
  euCountry("NL", "Netherlands"),
  euCountry("PL", "Poland"),
  euCountry("IT", "Italy"),
  euCountry("GR", "Greece", "EL"),
  entry("country", "US", "United States", { eu_member: "false" }),
  entry("country", "GB", "United Kingdom", { eu_member: "false" }),
];

function problems(flow: IntrastatJournal["flow"], l: IntrastatLine): string[] {
  const find = (kind: MasterKind, c: string) => master.find((m) => m.kind === kind && m.code === c);
  const out: string[] = [];
  const partner = find("country", l.partner_country);
  if (!l.partner_country) out.push("The partner country is missing.");
  else if (!partner) out.push(`Partner country ${l.partner_country} is not in the country master data.`);
  else if (partner.attrs.eu_member !== "true") out.push(`Partner country ${l.partner_country} is not an EU member state, so the trade is not declared in Intrastat.`);
  else if (l.partner_country === setting.home_country) out.push("The partner country is the company's own country; Intrastat covers trade with other member states.");
  const commodity = find("commodity", l.commodity_code);
  if (!l.commodity_code) out.push("The commodity code is missing.");
  else if (!commodity) out.push(`Commodity code ${l.commodity_code} is not in the commodity master data.`);
  else if (commodity.attrs.supplementary_unit && !(Number(l.supplementary_qty) > 0)) out.push(`Commodity ${l.commodity_code} needs a quantity in ${commodity.attrs.supplementary_unit}.`);
  if (!find("transaction_code", l.transaction_code)) out.push(`Transaction code "${l.transaction_code}" is missing or not in the master data.`);
  if (!find("transport_mode", l.transport_mode)) out.push(`Transport mode "${l.transport_mode}" is missing or not in the master data.`);
  if (!find("delivery_terms", l.delivery_terms)) out.push(`Delivery terms "${l.delivery_terms}" are missing or not in the master data.`);
  if (l.port && !find("port", l.port)) out.push(`Port ${l.port} is not in the port master data.`);
  if (!l.origin_country && flow === "arrival") out.push("The country of origin is required on arrivals.");
  else if (l.origin_country && !find("country", l.origin_country)) out.push(`Country of origin ${l.origin_country} is not in the country master data.`);
  if (!(Number(l.net_mass) > 0)) out.push("The net mass must be above zero.");
  if (!(Number(l.invoice_value) > 0)) out.push("The invoice value must be above zero.");
  if (!(Number(l.statistical_value) > 0)) out.push("The statistical value must be above zero.");
  return out;
}

const line = (n: number, partner: string, commodity: string, mass: number, qty: number, value: number, extra: Partial<IntrastatLine> = {}): IntrastatLine => ({
  line_no: n,
  source_ref: `SHP-${1000 + n}`,
  partner_country: partner,
  commodity_code: commodity,
  transaction_code: "11",
  transport_mode: "3",
  delivery_terms: "DAP",
  port: "",
  origin_country: "US",
  net_mass: dec(mass, 3),
  supplementary_qty: dec(qty, 3),
  invoice_value: dec(value),
  statistical_value: dec(value),
  compressed_from: 1,
  errors: [],
  ...extra,
});

interface StoredJournal extends Omit<IntrastatJournal, "line_count" | "error_count" | "lines"> {
  lines: IntrastatLine[];
  transferred: Set<string>;
}

const journals: StoredJournal[] = [
  {
    id: uid(K_JOURNAL, 1),
    flow: "dispatch",
    period_from: monthDay(-1, 1),
    status: "exported",
    correction_of: "",
    created_at: at(-20),
    exported_at: at(-12),
    lines: [line(1, "DE", "84137021", 412.5, 5, 18400), line(2, "NL", "84137081", 1380, 2, 56900), line(3, "FR", "84139100", 96.2, 0, 4210), line(4, "PL", "40169300", 18.4, 0, 960)],
    transferred: new Set(["SHP-1001", "SHP-1002", "SHP-1003", "SHP-1004"]),
  },
  {
    id: uid(K_JOURNAL, 2),
    flow: "dispatch",
    period_from: monthDay(0, 1),
    status: "draft",
    correction_of: "",
    created_at: at(-3),
    exported_at: null,
    lines: [
      line(1, "DE", "84137021", 205, 2, 9100),
      line(2, "DE", "84137021", 308, 3, 13650),
      line(3, "NL", "84139100", 54.8, 0, 2380),
      line(4, "FR", "84139100", 22.1, 0, 940, { transport_mode: "4" }),
      line(5, "IT", "84139900", 12, 0, 410),
    ],
    transferred: new Set(["SHP-1001", "SHP-1002", "SHP-1003", "SHP-1004", "SHP-1005"]),
  },
];

const view = (j: StoredJournal, withLines: boolean): IntrastatJournal => ({
  id: j.id,
  flow: j.flow,
  period_from: j.period_from,
  status: j.status,
  correction_of: j.correction_of,
  created_at: j.created_at,
  exported_at: j.exported_at,
  line_count: j.lines.length,
  error_count: j.lines.filter((l) => l.errors.length > 0).length,
  lines: withLines ? j.lines : [],
});

const open = (j: StoredJournal) => (j.status === "exported" ? reply.problem(400, "COM-0001", "This journal has been exported and can no longer change. Start a correction instead.") : null);

function renumber(j: StoredJournal): void {
  j.lines.forEach((l, i) => (l.line_no = i + 1));
}

// ---- routes: configuration

route("GET", `${BASE}/jurisdictions`, () => ({ items: jurisdictions }));
route("PUT", `${BASE}/jurisdictions/{code}`, ({ params, body }) => {
  const j = body as Partial<Jurisdiction>;
  if (!j.name || !j.kind || !j.country) return invalid("name", "name, kind and country are required");
  if (j.parent_code === params.code) return invalid("parent_code", "a jurisdiction cannot be its own parent");
  if (j.parent_code && !jurisdictions.some((x) => x.code === j.parent_code)) return invalid("parent_code", "the parent jurisdiction does not exist");
  for (let up = j.parent_code ?? ""; up !== ""; up = jurisdictions.find((x) => x.code === up)?.parent_code ?? "") {
    if (up === params.code) return invalid("parent_code", "the parent is below this jurisdiction");
  }
  const saved: Jurisdiction = { code: params.code, name: j.name, kind: j.kind, country: j.country, parent_code: j.parent_code ?? "" };
  const i = jurisdictions.findIndex((x) => x.code === params.code);
  if (i >= 0) jurisdictions[i] = saved;
  else jurisdictions.push(saved);
  return saved;
});
route("DELETE", `${BASE}/jurisdictions/{code}`, ({ params }) => {
  const i = jurisdictions.findIndex((x) => x.code === params.code);
  if (i < 0) return notFound("jurisdiction");
  if (jurisdictions.some((x) => x.parent_code === params.code) || codes.some((c) => c.jurisdiction === params.code)) return inUse(`Jurisdiction ${params.code} still has child jurisdictions or tax codes. Move or remove them first.`);
  jurisdictions.splice(i, 1);
  return reply.noContent();
});

route("GET", `${BASE}/codes`, () => ({ items: codes }));
route("PUT", `${BASE}/codes/{code}`, ({ params, body }) => {
  const c = body as Partial<TaxCode>;
  if (!c.name || !c.jurisdiction) return invalid("name", "name and jurisdiction are required");
  if (!jurisdictions.some((j) => j.code === c.jurisdiction)) return invalid("jurisdiction", `jurisdiction ${c.jurisdiction} does not exist`);
  const rates = (c.rates ?? []).map((r) => ({ valid_from: r.valid_from, valid_to: r.valid_to ?? null, rate_pct: r.rate_pct }));
  const saved = code(params.code, c.name, c.jurisdiction, rates.map((r) => [r.valid_from, r.valid_to, r.rate_pct]), {
    mechanism: c.mechanism ?? "standard",
    recoverable_pct: c.recoverable_pct ?? null,
  });
  const i = codes.findIndex((x) => x.code === params.code);
  if (i >= 0) codes[i] = saved;
  else codes.push(saved);
  return saved;
});
route("DELETE", `${BASE}/codes/{code}`, ({ params }) => {
  const i = codes.findIndex((x) => x.code === params.code);
  if (i < 0) return notFound("tax code");
  if (groups.some((g) => g.tax_codes.includes(params.code)) || boxes.some((b) => b.codes.some((c) => c.code === params.code))) return inUse(`Tax code ${params.code} is used by a tax group or a VAT return box. Remove it from them first.`);
  codes.splice(i, 1);
  return reply.noContent();
});

route("GET", `${BASE}/groups/{kind}`, ({ params }) => (params.kind === "party" || params.kind === "item" ? { items: groups.filter((g) => g.kind === params.kind) } : invalid("kind", "kind must be party or item")));
route("PUT", `${BASE}/groups/{kind}/{code}`, ({ params, body }) => {
  const g = body as Partial<TaxGroup>;
  if (params.kind !== "party" && params.kind !== "item") return invalid("kind", "kind must be party or item");
  if (!g.name) return invalid("name", "name is required");
  const missing = (g.tax_codes ?? []).find((c) => !codes.some((x) => x.code === c));
  if (missing) return invalid("tax_codes", `tax code "${missing}" does not exist`);
  const saved: TaxGroup = { kind: params.kind, code: params.code, name: g.name, tax_codes: g.tax_codes ?? [] };
  const i = groups.findIndex((x) => x.kind === params.kind && x.code === params.code);
  if (i >= 0) groups[i] = saved;
  else groups.push(saved);
  return saved;
});
route("DELETE", `${BASE}/groups/{kind}/{code}`, ({ params }) => {
  const i = groups.findIndex((x) => x.kind === params.kind && x.code === params.code);
  if (i < 0) return notFound("tax group");
  groups.splice(i, 1);
  return reply.noContent();
});

route("GET", `${BASE}/entity-settings`, () => setting);
route("PUT", `${BASE}/entity-settings`, ({ body }) => {
  const s = body as { home_country?: string; vat_id?: string };
  if (!s.home_country || !/^[A-Z]{2}$/.test(s.home_country)) return invalid("home_country", "home_country must be a 2-letter ISO 3166 code in capitals");
  setting = { home_country: s.home_country, vat_id: s.vat_id ?? "" };
  return setting;
});

route("GET", `${BASE}/vat-boxes`, () => ({ items: boxes }));
route("PUT", `${BASE}/vat-boxes`, ({ body }) => {
  const next = (body as { boxes?: VatBox[] }).boxes ?? [];
  const missing = next.flatMap((b) => b.codes).find((c) => !codes.some((x) => x.code === c.code));
  if (missing) return invalid("boxes", `tax code "${missing.code}" does not exist`);
  boxes.splice(0, boxes.length, ...next.map((b) => ({ ...b, codes: b.codes ?? [] })));
  return { items: boxes };
});

route("GET", `${BASE}/vat-return`, ({ query }) => {
  const p = periodOf(query);
  return p ? vatReturn(p.from, p.to) : invalid("period_from", "period_from and period_to are required as YYYY-MM-DD");
});
route("GET", `${BASE}/esl`, ({ query }) => {
  const p = periodOf(query);
  return p ? esl(p.from, p.to) : invalid("period_from", "period_from and period_to are required as YYYY-MM-DD");
});

// ---- routes: Intrastat

const isKind = (k: string): k is MasterKind => ["commodity", "transaction_code", "transport_mode", "port", "country", "delivery_terms"].includes(k);

route("GET", `${BASE}/intrastat/master/{kind}`, ({ params }) => (isKind(params.kind) ? { items: master.filter((m) => m.kind === params.kind).sort((a, b) => a.code.localeCompare(b.code)) } : invalid("kind", "unknown master data kind")));
route("PUT", `${BASE}/intrastat/master/{kind}/{code}`, ({ params, body }) => {
  const e = body as { description?: string; attrs?: Record<string, string> };
  if (!isKind(params.kind)) return invalid("kind", "unknown master data kind");
  if (!e.description) return invalid("description", "description is required");
  const saved = entry(params.kind, params.code, e.description, e.attrs ?? {});
  const i = master.findIndex((m) => m.kind === params.kind && m.code === params.code);
  if (i >= 0) master[i] = saved;
  else master.push(saved);
  return saved;
});
route("DELETE", `${BASE}/intrastat/master/{kind}/{code}`, ({ params }) => {
  const i = master.findIndex((m) => m.kind === params.kind && m.code === params.code);
  if (i < 0) return notFound("master data entry");
  master.splice(i, 1);
  return reply.noContent();
});

route("GET", `${BASE}/intrastat/journals`, () => ({ items: [...journals].sort((a, b) => b.period_from.localeCompare(a.period_from) || b.created_at.localeCompare(a.created_at)).map((j) => view(j, false)) }));
route("POST", `${BASE}/intrastat/journals`, ({ body }) => {
  const r = body as { flow?: string; period_from?: string };
  if (r.flow !== "arrival" && r.flow !== "dispatch") return invalid("flow", 'flow must be "arrival" or "dispatch"');
  if (!r.period_from || !/^\d{4}-\d{2}-01$/.test(r.period_from)) return invalid("period_from", "period_from must be the first day of a month");
  const j: StoredJournal = { id: crypto.randomUUID(), flow: r.flow, period_from: r.period_from, status: "draft", correction_of: "", created_at: at(0), exported_at: null, lines: [], transferred: new Set() };
  journals.push(j);
  return reply.created(view(j, true));
});
route("GET", `${BASE}/intrastat/journals/{id}`, ({ params }) => {
  const j = journals.find((x) => x.id === params.id);
  return j ? view(j, true) : notFound("journal");
});
route("POST", `${BASE}/intrastat/journals/{id}/transfer`, ({ params, body }) => {
  const j = journals.find((x) => x.id === params.id);
  if (!j) return notFound("journal");
  const closed = open(j);
  if (closed) return closed;
  const lines = (body as { lines?: Partial<IntrastatLine>[] }).lines ?? [];
  let added = 0;
  for (const l of lines) {
    if (!l.source_ref) return invalid("lines", "source_ref identifies the shipment or receipt line");
    if (j.transferred.has(l.source_ref)) continue;
    j.transferred.add(l.source_ref);
    j.lines.push({ ...line(j.lines.length + 1, "", "", 0, 0, 0, { origin_country: "", delivery_terms: "", transport_mode: "", transaction_code: "" }), ...l, line_no: j.lines.length + 1, compressed_from: 1, errors: [] } as IntrastatLine);
    added++;
  }
  if (added > 0) j.status = "draft";
  return { added, skipped: lines.length - added };
});
route("POST", `${BASE}/intrastat/journals/{id}/validate`, ({ params }) => {
  const j = journals.find((x) => x.id === params.id);
  if (!j) return notFound("journal");
  const closed = open(j);
  if (closed) return closed;
  for (const l of j.lines) l.errors = problems(j.flow, l);
  j.status = j.lines.length > 0 && j.lines.every((l) => l.errors.length === 0) ? "validated" : "draft";
  return view(j, true);
});
route("POST", `${BASE}/intrastat/journals/{id}/compress`, ({ params }) => {
  const j = journals.find((x) => x.id === params.id);
  if (!j) return notFound("journal");
  const closed = open(j);
  if (closed) return closed;
  const merged = new Map<string, IntrastatLine>();
  for (const l of j.lines) {
    const key = [l.partner_country, l.commodity_code, l.transaction_code, l.transport_mode, l.delivery_terms, l.port, l.origin_country].join("|");
    const m = merged.get(key);
    if (!m) {
      merged.set(key, { ...l, errors: [] });
      continue;
    }
    m.net_mass = dec(Number(m.net_mass) + Number(l.net_mass), 3);
    m.supplementary_qty = dec(Number(m.supplementary_qty) + Number(l.supplementary_qty), 3);
    m.invoice_value = dec(Number(m.invoice_value) + Number(l.invoice_value));
    m.statistical_value = dec(Number(m.statistical_value) + Number(l.statistical_value));
    m.compressed_from += l.compressed_from;
    m.source_ref = "";
  }
  j.lines = [...merged.values()];
  renumber(j);
  j.status = "draft";
  return view(j, true);
});
route("POST", `${BASE}/intrastat/journals/{id}/correct`, ({ params }) => {
  const j = journals.find((x) => x.id === params.id);
  if (!j) return notFound("journal");
  if (j.status !== "exported") return invalid("status", "Only an exported journal needs a correction. Edit this one directly.");
  if (journals.some((x) => x.correction_of === j.id && x.status !== "exported")) return invalid("status", "This journal already has a correction in progress.");
  const copy: StoredJournal = { ...j, id: crypto.randomUUID(), status: "draft", correction_of: j.id, created_at: at(0), exported_at: null, lines: j.lines.map((l) => ({ ...l, source_ref: "", errors: [] })), transferred: new Set() };
  journals.push(copy);
  return reply.created(view(copy, true));
});
route("DELETE", `${BASE}/intrastat/journals/{id}/lines/{lineNo}`, ({ params }) => {
  const j = journals.find((x) => x.id === params.id);
  if (!j) return notFound("journal");
  const closed = open(j);
  if (closed) return closed;
  const i = j.lines.findIndex((l) => String(l.line_no) === params.lineNo);
  if (i < 0) return notFound("line");
  j.lines.splice(i, 1);
  renumber(j);
  j.status = "draft";
  return reply.noContent();
});
route("GET", `${BASE}/intrastat/journals/{id}/export`, ({ params }) => {
  const j = journals.find((x) => x.id === params.id);
  if (!j) return notFound("journal");
  if (j.status === "draft") return invalid("status", "Validate the journal before exporting it; it has unchecked or invalid lines.");
  if (j.status === "validated") {
    j.status = "exported";
    j.exported_at = at(0);
  }
  const code2 = (c: string) => master.find((m) => m.kind === "country" && m.code === c)?.attrs.intrastat_code ?? c;
  const rows = j.lines.map((l) =>
    [l.line_no, j.flow, j.correction_of ? "A" : "N", code2(setting.home_country), code2(l.partner_country), l.commodity_code, l.origin_country ? code2(l.origin_country) : "", l.transaction_code, l.transport_mode, l.delivery_terms, l.port, Number(l.net_mass).toFixed(3), Number(l.supplementary_qty).toFixed(3), Math.round(Number(l.invoice_value)), Math.round(Number(l.statistical_value))].join(","),
  );
  const header = "line,flow,indicator,member_state,partner_member_state,commodity_code,country_of_origin,transaction_code,transport_mode,delivery_terms,port,net_mass_kg,supplementary_qty,invoice_value,statistical_value";
  return reply.text([header, ...rows].join("\n") + "\n", "text/csv; charset=utf-8", `intrastat-${j.flow}-${j.period_from.slice(0, 7)}.csv`);
});
