import { AgentError } from "../errors.js";

/**
 * PHI guard (#606). Fields the entity metadata tags `phi: true` must never reach a model, whatever the provider.
 * The guard knows field names (from the generated `<Entity>PHIFields()` lists, passed in through
 * AGENTS_PHI_FIELDS or AGENTS_PHI_FIELDS_FILE) and refuses text that carries a non-empty value under one of them,
 * as a JSON key (`"diagnosis_code": "J45"`) or as `name: value` / `name=value` text. snake_case and camelCase
 * spellings both match. It looks at names, not values: a model cannot be given PHI by renaming the key, because
 * the context is built from tool results that keep the metadata field names.
 */
export interface PhiGuard {
  /** Throws AGT-0215 naming the fields (never their values) when any text carries a tagged field. */
  assert(...texts: readonly string[]): void;
  /** The field names the guard watches, as configured. */
  readonly fields: readonly string[];
}

const camel = (s: string): string => s.replace(/_([a-z0-9])/g, (_, c: string) => c.toUpperCase());
const escapeRe = (s: string): string => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

export function createPhiGuard(fields: readonly string[]): PhiGuard {
  const names = [...new Set(fields.map((f) => f.trim()).filter((f) => f !== ""))];
  const variants = new Map<string, string[]>(names.map((n) => [n, [...new Set([n, camel(n)])]]));
  // A non-empty value: not null, an empty string, an empty array or an empty object.
  const nonEmpty = String.raw`\s*(?!null\b|""|\[\s*\]|\{\s*\}|$)\S`;
  const patterns = [...variants].map(([name, alts]) => {
    const group = alts.map(escapeRe).join("|");
    return { name, re: new RegExp(String.raw`(?:"(?:${group})"\s*:${nonEmpty})|(?:\b(?:${group})\s*[:=]${nonEmpty})`, "i") };
  });
  return {
    fields: names,
    assert(...texts: readonly string[]): void {
      const hit = new Set<string>();
      for (const text of texts) for (const p of patterns) if (p.re.test(text)) hit.add(p.name);
      if (hit.size > 0) {
        throw new AgentError("AGT-0215", `PHI-tagged field(s) ${[...hit].sort().join(", ")} cannot be sent to a model`);
      }
    },
  };
}

/** Parses AGENTS_PHI_FIELDS (comma separated) and the JSON of AGENTS_PHI_FIELDS_FILE (an array, or an object of entity to fields). */
export function parsePhiFields(list: string | undefined, fileJson: string | undefined): string[] {
  const out: string[] = (list ?? "").split(",").map((s) => s.trim()).filter((s) => s !== "");
  if (fileJson !== undefined && fileJson.trim() !== "") {
    const parsed: unknown = JSON.parse(fileJson);
    const take = (v: unknown): void => {
      if (typeof v === "string") out.push(v);
      else if (Array.isArray(v)) v.forEach(take);
      else if (typeof v === "object" && v !== null) Object.values(v).forEach(take);
      else throw new Error("AGENTS_PHI_FIELDS_FILE must hold a JSON array of field names or an object of entity to field names");
    };
    take(parsed);
  }
  return [...new Set(out)];
}
