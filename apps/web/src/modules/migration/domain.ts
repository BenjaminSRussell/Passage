import type { CreateProjectInput, MigrationProject, ProjectEdit, RunMode, SourceSystem } from "./api/client";

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

export const PROJECT_TYPE_LABELS = { migrate: "Migrate from a legacy system", copy: "Copy between legal entities", config_package: "Configuration package" } as const;

export interface ProjectForm {
  name: string;
  projectType: "migrate" | "copy" | "config_package";
  sourceSystem: string;
  sourceLegalEntityId: string;
  targetLegalEntityId: string;
  cutoverAt: string;
  scheduleCron: string;
}

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Validates the project form the way the service does, so an obvious mistake is caught before the request. */
export function parseProjectForm(form: ProjectForm): { value?: CreateProjectInput; errors: Partial<Record<keyof ProjectForm, string>> } {
  const errors: Partial<Record<keyof ProjectForm, string>> = {};
  const name = form.name.trim();
  if (name === "") errors.name = "Enter a project name.";
  else if ([...name].length > 80) errors.name = "At most 80 characters.";
  const input: CreateProjectInput = { name, projectType: form.projectType };
  if (form.projectType === "migrate") {
    if (form.sourceSystem === "") errors.sourceSystem = "Choose the legacy source system.";
    else input.sourceSystem = form.sourceSystem as SourceSystem;
  }
  if (form.projectType === "copy") {
    const source = form.sourceLegalEntityId.trim();
    const target = form.targetLegalEntityId.trim();
    if (!UUID_PATTERN.test(source)) errors.sourceLegalEntityId = "Enter the source legal entity id (UUID).";
    if (!UUID_PATTERN.test(target)) errors.targetLegalEntityId = "Enter the target legal entity id (UUID).";
    else if (source.toLowerCase() === target.toLowerCase()) errors.targetLegalEntityId = "The target must differ from the source.";
    input.sourceLegalEntityId = source;
    input.targetLegalEntityId = target;
  }
  const cutover = parseCutover(form.cutoverAt);
  if (cutover === null) errors.cutoverAt = "Enter a valid date and time.";
  else if (cutover !== "") input.cutoverAt = cutover;
  const cron = form.scheduleCron.trim();
  if (cron !== "") {
    if (cron.split(/\s+/).length !== 5) errors.scheduleCron = "Enter five fields: minute hour day month weekday.";
    else input.scheduleCron = cron;
  }
  return Object.keys(errors).length > 0 ? { errors } : { value: input, errors };
}

/** A datetime-local value read as UTC: the ISO timestamp, "" when blank, or null when it is not a date. */
function parseCutover(value: string): string | null {
  if (value === "") return "";
  const at = new Date(`${value.length === 16 ? `${value}:00` : value}Z`);
  return Number.isNaN(at.getTime()) ? null : at.toISOString();
}

/** The datetime-local value (UTC) for a stored cutover. */
export function cutoverInput(value?: string): string {
  if (!value) return "";
  const at = new Date(value);
  if (Number.isNaN(at.getTime())) return "";
  // Seconds are kept when set so an untouched cutover round-trips unchanged.
  return at.toISOString().slice(0, at.getUTCSeconds() === 0 ? 16 : 19);
}

export type ProjectEditForm = Pick<ProjectForm, "name" | "cutoverAt" | "scheduleCron">;

/** Checks the editable fields and returns only the ones that changed; an emptied cutover or schedule clears it. */
export function parseProjectEdit(form: ProjectEditForm, current: MigrationProject): { value?: ProjectEdit; errors: Partial<Record<keyof ProjectEditForm, string>> } {
  const errors: Partial<Record<keyof ProjectEditForm, string>> = {};
  const edit: ProjectEdit = {};
  const name = form.name.trim();
  if (name === "") errors.name = "Enter a project name.";
  else if ([...name].length > 80) errors.name = "At most 80 characters.";
  else if (name !== current.name) edit.name = name;
  const cutover = parseCutover(form.cutoverAt);
  if (cutover === null) errors.cutoverAt = "Enter a valid date and time.";
  else if (cutover !== (current.cutoverAt ? new Date(current.cutoverAt).toISOString() : "")) edit.cutoverAt = cutover;
  const cron = form.scheduleCron.trim();
  if (cron !== "" && cron.split(/\s+/).length !== 5) errors.scheduleCron = "Enter five fields: minute hour day month weekday.";
  else if (cron !== (current.scheduleCron ?? "")) edit.scheduleCron = cron;
  return Object.keys(errors).length > 0 ? { errors } : { value: edit, errors };
}

/**
 * Checks rewrite rules before they are sent: a blank prefix, an unchanged value, or a rule an earlier rule for the
 * same field always wins over (its prefix starts with the earlier prefix, so it could never apply).
 */
export function checkRewriteRules(rules: { field: string; from: string; to: string }[]): (string | undefined)[] {
  return rules.map((r, i) => {
    const field = r.field.trim();
    if (field === "") return "Enter a field, or @key for the record key.";
    if (r.from === "") return "Enter the prefix to replace.";
    if (r.from === r.to) return "The new prefix must differ from the old one.";
    const earlier = rules.slice(0, i).findIndex((e) => e.field.trim() === field && e.from !== "" && r.from.startsWith(e.from));
    return earlier >= 0 ? `Rule ${earlier + 1} already matches this prefix, so this rule would never apply.` : undefined;
  });
}
