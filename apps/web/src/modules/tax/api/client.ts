import { z } from "zod";
import { apiFetch, type FetchLike } from "../../../shell/api/http";

export const base = "/api/v1/tax";

export class TaxApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "TaxApiError";
  }
}

const defaultFetch: FetchLike = (input, init) => apiFetch(input, init);

async function problem(res: Response): Promise<TaxApiError> {
  let body: { code?: unknown; serviceCode?: unknown; detail?: unknown; errors?: unknown } = {};
  try {
    body = (await res.json()) as typeof body;
  } catch {
    // A non-JSON error body falls through to the generic message.
  }
  const fields = z.record(z.string(), z.string()).safeParse(body.errors ?? {});
  const code = typeof body.serviceCode === "string" ? body.serviceCode : typeof body.code === "string" ? body.code : "COM-0500";
  return new TaxApiError(res.status, code, typeof body.detail === "string" ? body.detail : `request failed with status ${res.status}`, fields.success ? fields.data : {});
}

async function send(path: string, method: string, body: unknown, fetchImpl: FetchLike): Promise<Response> {
  const res = await fetchImpl(`${base}${path}`, {
    method,
    headers: { Accept: "application/json", ...(body === undefined ? {} : { "Content-Type": "application/json" }) },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  if (!res.ok) throw await problem(res);
  return res;
}

async function read<T>(path: string, schema: z.ZodType<T, unknown>, fetchImpl: FetchLike): Promise<T> {
  return schema.parse(await (await send(path, "GET", undefined, fetchImpl)).json());
}

async function write<T>(path: string, method: "PUT" | "POST", body: unknown, schema: z.ZodType<T, unknown>, fetchImpl: FetchLike): Promise<T> {
  return schema.parse(await (await send(path, method, body, fetchImpl)).json());
}

async function remove(path: string, fetchImpl: FetchLike): Promise<void> {
  await send(path, "DELETE", undefined, fetchImpl);
}

const items = <T extends z.ZodTypeAny>(item: T) => z.object({ items: z.array(item) }).transform((v) => v.items);

// ---- configuration

export const jurisdictionKinds = ["country", "state", "county", "city", "district"] as const;
export type JurisdictionKind = (typeof jurisdictionKinds)[number];

export const jurisdictionSchema = z.object({
  code: z.string(),
  name: z.string(),
  kind: z.enum(jurisdictionKinds),
  country: z.string(),
  parent_code: z.string().default(""),
});
export type Jurisdiction = z.infer<typeof jurisdictionSchema>;

export const rateSchema = z.object({
  valid_from: z.string(),
  valid_to: z.string().nullable().optional(),
  rate_pct: z.string(),
});
export type Rate = z.infer<typeof rateSchema>;

export const mechanisms = ["standard", "reverse_charge", "use_tax"] as const;

export const taxCodeSchema = z.object({
  code: z.string(),
  name: z.string(),
  jurisdiction: z.string(),
  mechanism: z.enum(mechanisms),
  calc_basis: z.string(),
  rounding: z.string(),
  compound_sequence: z.number().int(),
  recoverable_pct: z.string().nullable().optional(),
  rates: z.array(rateSchema.passthrough()),
});
export type TaxCode = z.infer<typeof taxCodeSchema>;

export interface TaxCodeInput {
  name: string;
  jurisdiction: string;
  mechanism: (typeof mechanisms)[number];
  recoverable_pct?: string;
  rates: { valid_from: string; valid_to?: string; rate_pct: string }[];
}

export type GroupKind = "party" | "item";
export const groupSchema = z.object({ kind: z.enum(["party", "item"]), code: z.string(), name: z.string(), tax_codes: z.array(z.string()) });
export type TaxGroup = z.infer<typeof groupSchema>;

export const entitySettingSchema = z.object({ home_country: z.string(), vat_id: z.string() });
export type EntitySetting = z.infer<typeof entitySettingSchema>;

export const boxSchema = z.object({
  box: z.string(),
  label: z.string(),
  kind: z.enum(["base", "tax", "payable"]),
  sort: z.number().int(),
  codes: z.array(z.object({ code: z.string(), direction: z.enum(["output", "input", "use"]) })),
});
export type VatBox = z.infer<typeof boxSchema>;

export const listJurisdictions = (f: FetchLike = defaultFetch) => read("/jurisdictions", items(jurisdictionSchema), f);
export const putJurisdiction = (j: Jurisdiction, f: FetchLike = defaultFetch) =>
  write(`/jurisdictions/${encodeURIComponent(j.code)}`, "PUT", { name: j.name, kind: j.kind, country: j.country, parent_code: j.parent_code }, jurisdictionSchema, f);
export const deleteJurisdiction = (code: string, f: FetchLike = defaultFetch) => remove(`/jurisdictions/${encodeURIComponent(code)}`, f);

export const listTaxCodes = (f: FetchLike = defaultFetch) => read("/codes", items(taxCodeSchema), f);
export const putTaxCode = (code: string, input: TaxCodeInput, f: FetchLike = defaultFetch) =>
  write(`/codes/${encodeURIComponent(code)}`, "PUT", input, taxCodeSchema, f);
export const deleteTaxCode = (code: string, f: FetchLike = defaultFetch) => remove(`/codes/${encodeURIComponent(code)}`, f);

export const listTaxGroups = (kind: GroupKind, f: FetchLike = defaultFetch) => read(`/groups/${kind}`, items(groupSchema), f);
export const putTaxGroup = (g: TaxGroup, f: FetchLike = defaultFetch) =>
  write(`/groups/${g.kind}/${encodeURIComponent(g.code)}`, "PUT", { name: g.name, tax_codes: g.tax_codes }, groupSchema, f);
export const deleteTaxGroup = (kind: GroupKind, code: string, f: FetchLike = defaultFetch) => remove(`/groups/${kind}/${encodeURIComponent(code)}`, f);

/** The company's tax settings, or null while none are saved yet (the service answers COM-0503). */
export async function getEntitySetting(f: FetchLike = defaultFetch): Promise<EntitySetting | null> {
  try {
    return await read("/entity-settings", entitySettingSchema, f);
  } catch (error) {
    if (error instanceof TaxApiError && error.status === 422) return null;
    throw error;
  }
}
export const putEntitySetting = (s: EntitySetting, f: FetchLike = defaultFetch) => write("/entity-settings", "PUT", s, entitySettingSchema, f);

export const listVatBoxes = (f: FetchLike = defaultFetch) => read("/vat-boxes", items(boxSchema), f);
export const putVatBoxes = (boxes: VatBox[], f: FetchLike = defaultFetch) => write("/vat-boxes", "PUT", { boxes }, items(boxSchema), f);

// ---- VAT return and EU sales list

export const vatReturnSchema = z.object({
  period_from: z.string(),
  period_to: z.string(),
  boxes: z.array(z.object({ box: z.string(), label: z.string(), kind: z.string(), amount: z.string() })),
  transactions: z.number().int(),
  missing_base: z.number().int(),
  unmapped: z.number().int(),
});
export type VatReturn = z.infer<typeof vatReturnSchema>;

export const eslSchema = z.object({
  period_from: z.string(),
  period_to: z.string(),
  home_country: z.string(),
  vat_id: z.string(),
  rows: z.array(
    z.object({ country: z.string(), vat_id: z.string(), indicator: z.string(), supply_kind: z.string(), base: z.string(), transactions: z.number().int() }),
  ),
  total: z.string(),
  missing_vat_id: z.number().int(),
  missing_base: z.number().int(),
});
export type Esl = z.infer<typeof eslSchema>;

const period = (from: string, to: string) => `?period_from=${encodeURIComponent(from)}&period_to=${encodeURIComponent(to)}`;
export const getVatReturn = (from: string, to: string, f: FetchLike = defaultFetch) => read(`/vat-return${period(from, to)}`, vatReturnSchema, f);
export const getEsl = (from: string, to: string, f: FetchLike = defaultFetch) => read(`/esl${period(from, to)}`, eslSchema, f);

// ---- Intrastat

export const masterKinds = ["commodity", "transaction_code", "transport_mode", "port", "country", "delivery_terms"] as const;
export type MasterKind = (typeof masterKinds)[number];

export const masterEntrySchema = z.object({
  kind: z.enum(masterKinds),
  code: z.string(),
  description: z.string(),
  attrs: z.record(z.string(), z.string()).default({}),
});
export type MasterEntry = z.infer<typeof masterEntrySchema>;

export const lineSchema = z.object({
  line_no: z.number().int(),
  source_ref: z.string(),
  partner_country: z.string(),
  commodity_code: z.string(),
  transaction_code: z.string(),
  transport_mode: z.string(),
  delivery_terms: z.string(),
  port: z.string(),
  origin_country: z.string(),
  net_mass: z.string(),
  supplementary_qty: z.string(),
  invoice_value: z.string(),
  statistical_value: z.string(),
  compressed_from: z.number().int(),
  errors: z.array(z.string()),
});
export type IntrastatLine = z.infer<typeof lineSchema>;
export type NewIntrastatLine = Omit<IntrastatLine, "line_no" | "compressed_from" | "errors">;

export const journalSchema = z.object({
  id: z.string(),
  flow: z.enum(["arrival", "dispatch"]),
  period_from: z.string(),
  status: z.enum(["draft", "validated", "exported"]),
  correction_of: z.string(),
  created_at: z.string(),
  exported_at: z.string().nullable(),
  line_count: z.number().int(),
  error_count: z.number().int(),
  lines: z.array(lineSchema).default([]),
});
export type IntrastatJournal = z.infer<typeof journalSchema>;

export const listMaster = (kind: MasterKind, f: FetchLike = defaultFetch) => read(`/intrastat/master/${kind}`, items(masterEntrySchema), f);
export const putMaster = (e: MasterEntry, f: FetchLike = defaultFetch) =>
  write(`/intrastat/master/${e.kind}/${encodeURIComponent(e.code)}`, "PUT", { description: e.description, attrs: e.attrs }, masterEntrySchema, f);
export const deleteMaster = (kind: MasterKind, code: string, f: FetchLike = defaultFetch) => remove(`/intrastat/master/${kind}/${encodeURIComponent(code)}`, f);

export const listJournals = (f: FetchLike = defaultFetch) => read("/intrastat/journals", items(journalSchema), f);
export const createJournal = (flow: "arrival" | "dispatch", periodFrom: string, f: FetchLike = defaultFetch) =>
  write("/intrastat/journals", "POST", { flow, period_from: periodFrom }, journalSchema, f);
export const getJournal = (id: string, f: FetchLike = defaultFetch) => read(`/intrastat/journals/${encodeURIComponent(id)}`, journalSchema, f);

const transferResultSchema = z.object({ added: z.number().int(), skipped: z.number().int() });
export const transferLines = (id: string, lines: NewIntrastatLine[], f: FetchLike = defaultFetch) =>
  write(`/intrastat/journals/${encodeURIComponent(id)}/transfer`, "POST", { lines }, transferResultSchema, f);
export const validateJournal = (id: string, f: FetchLike = defaultFetch) => write(`/intrastat/journals/${encodeURIComponent(id)}/validate`, "POST", undefined, journalSchema, f);
export const compressJournal = (id: string, f: FetchLike = defaultFetch) => write(`/intrastat/journals/${encodeURIComponent(id)}/compress`, "POST", undefined, journalSchema, f);
export const correctJournal = (id: string, f: FetchLike = defaultFetch) => write(`/intrastat/journals/${encodeURIComponent(id)}/correct`, "POST", undefined, journalSchema, f);
export const deleteLine = (id: string, lineNo: number, f: FetchLike = defaultFetch) => remove(`/intrastat/journals/${encodeURIComponent(id)}/lines/${lineNo}`, f);

/** The CSV declaration of a validated journal, as text. Exporting locks the journal. */
export async function exportJournal(id: string, f: FetchLike = defaultFetch): Promise<string> {
  const res = await f(`${base}/intrastat/journals/${encodeURIComponent(id)}/export`, { headers: { Accept: "text/csv" } });
  if (!res.ok) throw await problem(res);
  return res.text();
}
