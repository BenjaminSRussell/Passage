import type { LookupDefinition, LookupKind } from "./api/client";

export const LOOKUP_KINDS: { value: LookupKind; label: string; hint: string }[] = [
  { value: "enum", label: "Code list", hint: "Maps a Passage code to the code the legacy ERP uses, for example kg to KGM." },
  { value: "uom_conversion", label: "Unit conversion", hint: "Keys are FROM|TO unit pairs and values are positive factors with at most 12 decimals, for example LB|KG = 0.45359237. Enter both directions: a mapping never divides. A mapping with quantityPath sends the quantity times the factor." },
  { value: "price_list", label: "Price list", hint: "Keys are item keys, or item|currency (add the legal entity id as a key part for entity prices), and values are prices that are not negative with at most 6 decimals, for example SKU-1|EUR = 12.50." },
];

export function kindLabel(kind: string): string {
  return LOOKUP_KINDS.find((k) => k.value === kind)?.label ?? kind;
}

const STARTER_ENTRIES: Record<LookupKind, Record<string, string>> = {
  enum: { kg: "KGM", ea: "EA" },
  uom_conversion: { "KG|LB": "2.20462262185", "LB|KG": "0.45359237" },
  price_list: { "SKU-1|EUR": "12.50" },
};

export function starterEntriesText(kind: LookupKind): string {
  return JSON.stringify(STARTER_ENTRIES[kind], null, 2);
}

/** Entries are a JSON object of text to text: a price or a factor is a decimal string, never a JSON number. */
export function parseEntriesText(text: string): { value?: Record<string, string>; error?: string } {
  if (text.trim() === "") return { error: "Enter the entries as a JSON object of key to value." };
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (e) {
    return { error: `The entries are not valid JSON: ${e instanceof Error ? e.message : "parse error"}.` };
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return { error: "The entries must be a JSON object of key to value." };
  for (const [key, value] of Object.entries(parsed)) {
    if (typeof value !== "string") return { error: `The value of "${key}" must be text, in quotes. Write a price as "12.50", not 12.5.` };
  }
  return { value: parsed as Record<string, string> };
}

export interface LookupForm {
  name: string;
  kind: string;
  description: string;
  entries: string;
}

export const EMPTY_LOOKUP_FORM: LookupForm = { name: "", kind: "enum", description: "", entries: starterEntriesText("enum") };

const NAME_RE = /^[A-Za-z][A-Za-z0-9_.-]{0,63}$/;

export function isLookupKind(value: string): value is LookupKind {
  return LOOKUP_KINDS.some((k) => k.value === value);
}

export function parseLookupForm(form: LookupForm): { value?: { name: string; definition: LookupDefinition }; errors: Partial<Record<keyof LookupForm, string>> } {
  const errors: Partial<Record<keyof LookupForm, string>> = {};
  const name = form.name.trim();
  if (!NAME_RE.test(name)) errors.name = "Start with a letter, then letters, digits, _ . - (at most 64 characters). Outbound mappings refer to the table by this name.";
  if (!isLookupKind(form.kind)) errors.kind = "Choose what the table holds.";
  if (form.description.length > 500) errors.description = "At most 500 characters.";
  const entries = parseEntriesText(form.entries);
  if (entries.error) errors.entries = entries.error;
  if (Object.keys(errors).length > 0 || !entries.value || !isLookupKind(form.kind)) return { errors };
  const definition: LookupDefinition = { kind: form.kind, entries: entries.value };
  if (form.description.trim() !== "") definition.description = form.description.trim();
  return { value: { name, definition }, errors };
}

/** The entries whose key or value contains the text, in key order, at most `limit` of them. */
export function filterEntries(entries: Record<string, string>, text: string, limit: number): { rows: { key: string; value: string }[]; total: number } {
  const needle = text.trim().toLowerCase();
  const all = Object.keys(entries)
    .sort()
    .filter((key) => needle === "" || key.toLowerCase().includes(needle) || (entries[key] ?? "").toLowerCase().includes(needle));
  return { rows: all.slice(0, limit).map((key) => ({ key, value: entries[key] ?? "" })), total: all.length };
}
