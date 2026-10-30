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
