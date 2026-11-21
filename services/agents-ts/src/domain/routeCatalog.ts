import { canCallTool } from "./authz.js";

type FieldKind = "flag" | "amount" | "quantity" | "text";
type Value = boolean | number | string;
type Values = Record<string, Value>;

interface Field {
  name: string;
  kind: FieldKind;
}

export interface CatalogEntry {
  routeKey: string;
  path: string;
  duty: string;
  label: string;
  params: readonly Field[];
  draft: readonly Field[];
  summarize: (params: Values, draft: Values) => string;
}

export type TaskBody =
  | { navigationIntent: { routeKey: string; path: string; params: Values; draft: Values; summary: string }; generated: true }
  | { clarifyingQuestion: string; generated: true }
  | { refusal: { code: string; message: string }; generated: true };

export const TEXT_LIMIT = 80;
export const CLARIFY_FALLBACK = "I did not understand which page you want. Tell me what you want to open or create.";
export const NO_PAGE_ACCESS = "You do not have access to that page.";

export const ROUTE_CATALOG: readonly CatalogEntry[] = [
  {
    routeKey: "receivables.openItems",
    path: "/receivables/open-items",
    duty: "receivables.view",
    label: "open customer invoices, optionally only overdue ones or those above an amount",
    params: [
      { name: "overdue", kind: "flag" },
      { name: "amountGt", kind: "amount" },
    ],
    draft: [],
    summarize: (params) => `Showing open invoices${params["overdue"] === true ? " that are overdue" : ""}${params["amountGt"] === undefined ? "" : ` and above ${params["amountGt"]}`}.`,
  },
  {
    routeKey: "purchasing.purchaseOrder.new",
    path: "/purchasing/purchase-orders/new",
    duty: "procurement.purchase-order.inquire",
    label: "the form to create a purchase order, optionally with the vendor, the product and the quantity named in the request",
    params: [],
    draft: [
      { name: "vendor", kind: "text" },
      { name: "product", kind: "text" },
      { name: "qty", kind: "quantity" },
    ],
    summarize: (_params, draft) => `Opening a new purchase order${draft["product"] === undefined ? "" : ` for ${draft["qty"] === undefined ? "" : `${draft["qty"]} `}${draft["product"]}`}${draft["vendor"] === undefined ? "" : ` from ${draft["vendor"]}`}. Nothing is saved until you save it yourself.`,
  },
];

export function taskInstruction(): string {
  const screens = ROUTE_CATALOG.map((e) => `${e.routeKey} (${e.label}; params: ${e.params.map((f) => `${f.name} ${f.kind}`).join(", ") || "none"}; draft: ${e.draft.map((f) => `${f.name} ${f.kind}`).join(", ") || "none"})`).join("; ");
  return `Resolve the user's request to one Depot screen. If the request is ambiguous and depot.meta.search_tools or depot.help.search is listed as callable, use them to find a screen or its help; call no other tool. Never write a URL or a path. Screens: ${screens}. Answer with only this JSON: {"routeKey":"...","params":{...},"draft":{...}} using only the names listed for that screen, or {"clarifyingQuestion":"..."} when no screen fits or a detail is missing.`;
}

const isRecord = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);

function asNumber(v: unknown): number | undefined {
  const n = typeof v === "number" ? v : typeof v === "string" && v.trim() !== "" ? Number(v) : Number.NaN;
  return Number.isFinite(n) ? n : undefined;
}

function coerce(kind: FieldKind, v: unknown): Value | undefined {
  if (kind === "flag") return v === true || v === "true" ? true : v === false || v === "false" ? false : undefined;
  if (kind === "text") {
    if (typeof v !== "string") return undefined;
    const clean = v.replace(/\p{Cc}/gu, " ").trim().slice(0, TEXT_LIMIT);
    return clean === "" ? undefined : clean;
  }
  const n = asNumber(v);
  if (n === undefined) return undefined;
  return kind === "amount" ? (n >= 0 ? n : undefined) : n > 0 ? n : undefined;
}

function pick(fields: readonly Field[], source: unknown): Values {
  const out: Values = {};
  if (!isRecord(source)) return out;
  for (const f of fields) {
    const value = coerce(f.kind, source[f.name]);
    if (value !== undefined) out[f.name] = value;
  }
  return out;
}

function parse(answer: string): Record<string, unknown> | undefined {
  try {
    const raw: unknown = JSON.parse(answer);
    return isRecord(raw) ? raw : undefined;
  } catch {
    return undefined;
  }
}

const clarify = (question: unknown): TaskBody => ({ clarifyingQuestion: (typeof question === "string" && question.replace(/\p{Cc}/gu, " ").trim().slice(0, 200)) || CLARIFY_FALLBACK, generated: true });

export function taskBody(reply: { answer: string; refusal: { code: string; message: string } | null }, duties: ReadonlySet<string>): TaskBody {
  if (reply.refusal !== null) return { refusal: reply.refusal, generated: true };
  const parsed = parse(reply.answer);
  if (parsed === undefined || parsed["clarifyingQuestion"] !== undefined) return clarify(parsed?.["clarifyingQuestion"]);
  const entry = ROUTE_CATALOG.find((e) => e.routeKey === parsed["routeKey"]);
  if (entry === undefined) return clarify(undefined);
  if (!canCallTool(duties, entry.duty)) return { refusal: { code: "AGT-0203", message: NO_PAGE_ACCESS }, generated: true };
  const params = pick(entry.params, parsed["params"]);
  const draft = pick(entry.draft, parsed["draft"]);
  return { navigationIntent: { routeKey: entry.routeKey, path: entry.path, params, draft, summary: entry.summarize(params, draft) }, generated: true };
}
