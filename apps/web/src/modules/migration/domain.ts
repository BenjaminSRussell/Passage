import type { RunMode, SourceSystem } from "./api/client";

export const SOURCE_LABELS: Record<SourceSystem, string> = {
  ax2012: "Dynamics AX 2012",
  d365fo: "Dynamics 365 F&O",
  sap_ecc: "SAP ECC",
  sap_s4: "SAP S/4HANA",
};
export const SOURCE_SYSTEMS = Object.keys(SOURCE_LABELS) as SourceSystem[];
export const MODE_LABELS: Record<RunMode, string> = { rehearsal: "Rehearsal", cutover: "Cutover", delta: "Delta" };
export const MODES = Object.keys(MODE_LABELS) as RunMode[];
export const RUN_STATUSES = ["succeeded", "partial", "failed"] as const;

export function formatDateTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toISOString().replace("T", " ").slice(0, 16) + " UTC";
}

/** Parses pasted mapping JSON; the service owns every other rule. */
export function parseMappingText(text: string): { mapping?: unknown; error?: string } {
  if (text.trim() === "") return { error: "Paste the mapping JSON." };
  try {
    const value: unknown = JSON.parse(text);
    if (typeof value !== "object" || value === null || Array.isArray(value)) return { error: "The mapping must be a JSON object." };
    return { mapping: value };
  } catch (error) {
    return { error: `Not valid JSON: ${error instanceof Error ? error.message : "parse error"}` };
  }
}

/** Parses the pasted configuration package JSON and its signature; the service verifies both. */
export function parsePackageInput(text: string, signature: string): { pkg?: unknown; errors: { package?: string; signature?: string } } {
  const errors: { package?: string; signature?: string } = {};
  let pkg: unknown;
  if (text.trim() === "") errors.package = "Paste the package JSON.";
  else {
    try {
      const value: unknown = JSON.parse(text);
      if (typeof value !== "object" || value === null || Array.isArray(value)) errors.package = "The package must be a JSON object.";
      else pkg = value;
    } catch (error) {
      errors.package = `Not valid JSON: ${error instanceof Error ? error.message : "parse error"}`;
    }
  }
  if (signature.trim() === "") errors.signature = "Paste the base64 signature.";
  return { pkg: errors.package ? undefined : pkg, errors };
}

/** Parses the template form: a name, an entity, a whole-number version and a JSON object of target to value. */
export function parseTemplateForm(input: { name: string; entity: string; version: string; defaults: string }): {
  value?: { name: string; entity: string; version: number; defaults: Record<string, string> };
  errors: { name?: string; entity?: string; version?: string; defaults?: string };
} {
  const errors: { name?: string; entity?: string; version?: string; defaults?: string } = {};
  const name = input.name.trim();
  const entity = input.entity.trim();
  const version = Number(input.version);
  if (name === "") errors.name = "Enter a template name.";
  else if (name.length > 80) errors.name = "At most 80 characters.";
  if (entity === "") errors.entity = "Enter the entity the template is for.";
  if (!Number.isInteger(version) || version < 1) errors.version = "Enter a whole number of 1 or more.";
  let defaults: Record<string, string> = {};
  if (input.defaults.trim() === "") errors.defaults = "Enter the default values as JSON, for example {\"paymentTerms\": \"NET30\"}.";
  else {
    try {
      const value: unknown = JSON.parse(input.defaults);
      if (typeof value !== "object" || value === null || Array.isArray(value)) errors.defaults = "The defaults must be a JSON object of target to value.";
      else if (Object.keys(value).length === 0) errors.defaults = "Add at least one default.";
      else if (!Object.values(value).every((v) => typeof v === "string")) errors.defaults = "Every value must be a string, such as \"500\" or \"NET30\".";
      else defaults = value as Record<string, string>;
    } catch (error) {
      errors.defaults = `Not valid JSON: ${error instanceof Error ? error.message : "parse error"}`;
    }
  }
  if (Object.keys(errors).length > 0) return { errors };
  return { value: { name, entity, version, defaults }, errors };
}
