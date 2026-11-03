import type { ConnectionKind, CreateProjectInput, LegacySourceSystem, MigrationProject, ProjectEdit, RunMode, SourceSystem } from "./api/client";

export const SOURCE_LABELS: Record<LegacySourceSystem, string> = {
  ax2012: "Dynamics AX 2012",
  d365fo: "Dynamics 365 F&O",
  sap_ecc: "SAP ECC",
  sap_s4: "SAP S/4HANA",
};
export const SOURCE_SYSTEMS = Object.keys(SOURCE_LABELS) as LegacySourceSystem[];

/** The label of a source system; a custom system (custom_acme) shows as its own name. */
export function sourceLabel(system: string): string {
  return (SOURCE_LABELS as Record<string, string>)[system] ?? (system.startsWith("custom_") ? `Custom: ${system.slice("custom_".length)}` : system);
}

const CUSTOM_SYSTEM = /^custom_[a-z0-9_]{1,40}$/;

/** True for a legacy system or a well-formed custom_<name>; the service decides the same way. */
export function isSourceSystem(value: string): boolean {
  return value in SOURCE_LABELS || CUSTOM_SYSTEM.test(value);
}

export const SOURCE_SYSTEM_HINT = "Lowercase letters, digits and underscores, up to 40 (for example acme or acme_erp).";
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
  /** The connection profile a migrate project reads from; optional. */
  connectionProfileId?: string;
  cutoverAt: string;
  scheduleCron: string;
  /** IANA time zone the cron is read in; blank is UTC. */
  scheduleTimezone: string;
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
    if (form.sourceSystem === "") errors.sourceSystem = "Choose the source system.";
    else if (!isSourceSystem(form.sourceSystem)) errors.sourceSystem = `Name the custom system. ${SOURCE_SYSTEM_HINT}`;
    else input.sourceSystem = form.sourceSystem as SourceSystem;
    // A migrate project may name the legal entity its data lands in and the connection it reads from.
    const target = form.targetLegalEntityId.trim();
    if (target !== "") {
      if (!UUID_PATTERN.test(target)) errors.targetLegalEntityId = "Enter the target legal entity id (UUID), or leave it empty.";
      else input.targetLegalEntityId = target;
    }
    if (form.connectionProfileId) input.connectionProfileId = form.connectionProfileId;
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
  const zone = form.scheduleTimezone.trim();
  if (zone !== "") {
    if (!validTimezone(zone)) errors.scheduleTimezone = "Enter an IANA time zone such as Europe/Berlin, or leave empty for UTC.";
    else input.scheduleTimezone = zone;
  }
  return Object.keys(errors).length > 0 ? { errors } : { value: input, errors };
}

/** Whether the browser knows name as an IANA time zone (the service checks again against its own database). */
export function validTimezone(name: string): boolean {
  if (name.length > 64 || name.toLowerCase() === "local") return false;
  try {
    new Intl.DateTimeFormat("en", { timeZone: name });
    return true;
  } catch {
    return false;
  }
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

export type ProjectEditForm = Pick<ProjectForm, "name" | "cutoverAt" | "scheduleCron" | "scheduleTimezone">;

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
  const zone = form.scheduleTimezone.trim();
  if (zone !== "" && !validTimezone(zone)) errors.scheduleTimezone = "Enter an IANA time zone such as Europe/Berlin, or leave empty for UTC.";
  else if ((zone || "UTC") !== (current.scheduleTimezone || "UTC")) edit.scheduleTimezone = zone;
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

export const CONNECTION_KIND_LABELS: Record<ConnectionKind, string> = { sql: "SQL database", rest: "REST API", file: "File drop directory" };
export const CONNECTION_KINDS = Object.keys(CONNECTION_KIND_LABELS) as ConnectionKind[];

export interface ConnectionField {
  key: string;
  label: string;
  hint?: string;
  required?: boolean;
  number?: boolean;
  options?: string[];
}

const COMMON_FIELDS: ConnectionField[] = [
  { key: "codepage", label: "Character set", hint: "An IANA name such as utf-8, windows-1252 or iso-8859-1." },
  { key: "timezone", label: "Time zone", hint: "An IANA zone such as Europe/Berlin; dates without a zone are read in it." },
];

/** The non-secret settings of each kind. The password, token or key is never a field here: it is set on its own. */
export const CONNECTION_FIELDS: Record<ConnectionKind, ConnectionField[]> = {
  sql: [
    { key: "driver", label: "Driver", required: true, options: ["postgres", "sqlserver", "mysql", "oracle"], hint: "For Oracle, the database is the service name." },
    { key: "host", label: "Host", required: true, hint: "A host name or IP address. A private address works only when the operator has allowlisted it." },
    { key: "port", label: "Port", number: true, hint: "Default 5432 (postgres), 1433 (sqlserver), 3306 (mysql), 1521 (oracle)." },
    { key: "database", label: "Database", required: true },
    { key: "user", label: "User", required: true, hint: "The password is set separately, after the connection exists." },
    { key: "sslmode", label: "TLS mode", options: ["require", "verify-full", "verify-ca", "disable"], hint: "Default require." },
    ...COMMON_FIELDS,
  ],
  rest: [
    { key: "baseUrl", label: "Base URL", required: true, hint: "https:// with no credentials or query. http:// only for an endpoint without authentication." },
    { key: "authType", label: "Authentication", options: ["none", "basic", "bearer", "header", "oauth2_client"], hint: "The password, token, header value or OAuth2 client secret is set separately." },
    { key: "authUrl", label: "Token URL", hint: "For OAuth2 client credentials: the https token endpoint." },
    { key: "clientId", label: "Client ID", hint: "For OAuth2 client credentials." },
    { key: "scope", label: "Scope", hint: "For OAuth2 client credentials, space separated." },
    { key: "username", label: "User name", hint: "For basic authentication." },
    { key: "headerName", label: "Header name", hint: "For header authentication, for example X-Api-Key." },
    { key: "pagingStyle", label: "Paging style", options: ["none", "offset", "page", "cursor", "link"] },
    { key: "probePath", label: "Test path", hint: "Appended to the base URL for the connection test, for example /health." },
    ...COMMON_FIELDS,
  ],
  file: [
    { key: "dropDir", label: "Drop directory", required: true, hint: "A relative path under the drop directory the operator mounted, for example acme/ax2012." },
    { key: "format", label: "File format", required: true, options: ["csv", "tsv", "json", "jsonl"] },
    { key: "delimiter", label: "Delimiter", hint: "One character, for csv files." },
    { key: "filePattern", label: "File name pattern", hint: "For example Cust*.csv." },
    ...COMMON_FIELDS,
  ],
};

export interface ConnectionForm {
  name: string;
  kind: ConnectionKind;
  sourceSystem: string;
  /** Setting key to text value. */
  config: Record<string, string>;
}

export const EMPTY_CONNECTION_FORM: ConnectionForm = { name: "", kind: "sql", sourceSystem: "", config: {} };

/** Settings to the text a form shows. */
export function configToText(config: Record<string, string | number>): Record<string, string> {
  return Object.fromEntries(Object.entries(config).map(([k, v]) => [k, String(v)]));
}

/** Form text to the config object the service takes: blanks are dropped and a number field becomes a number. */
export function parseConnectionConfig(kind: ConnectionKind, text: Record<string, string>): { config?: Record<string, string | number>; errors: Record<string, string> } {
  const errors: Record<string, string> = {};
  const config: Record<string, string | number> = {};
  for (const field of CONNECTION_FIELDS[kind]) {
    const raw = (text[field.key] ?? "").trim();
    if (raw === "") {
      if (field.required) errors[field.key] = `Enter the ${field.label.toLowerCase()}.`;
      continue;
    }
    if (field.number) {
      const n = Number(raw);
      if (!Number.isInteger(n) || n < 1 || n > 65535) errors[field.key] = "Enter a whole number from 1 to 65535.";
      else config[field.key] = n;
    } else config[field.key] = raw;
  }
  return Object.keys(errors).length > 0 ? { errors } : { config, errors };
}

/** Validates the new connection form the way the service does, so an obvious mistake is caught before the request. */
export function parseConnectionForm(form: ConnectionForm): { value?: { name: string; kind: ConnectionKind; sourceSystem: SourceSystem; config: Record<string, string | number> }; errors: Record<string, string> } {
  const errors: Record<string, string> = {};
  const name = form.name.trim();
  if (name === "") errors.name = "Enter a connection name.";
  else if ([...name].length > 80) errors.name = "At most 80 characters.";
  if (form.sourceSystem === "") errors.sourceSystem = "Choose the source system.";
  else if (!isSourceSystem(form.sourceSystem)) errors.sourceSystem = `Name the custom system. ${SOURCE_SYSTEM_HINT}`;
  const parsed = parseConnectionConfig(form.kind, form.config);
  for (const [k, v] of Object.entries(parsed.errors)) errors[`config.${k}`] = v;
  if (Object.keys(errors).length > 0 || !parsed.config) return { errors };
  return { value: { name, kind: form.kind, sourceSystem: form.sourceSystem as SourceSystem, config: parsed.config }, errors };
}

/** The secret field's check: the service refuses an empty value and one over 4096 bytes. */
export function checkSecret(secret: string): string | undefined {
  if (secret === "") return "Enter the secret.";
  if (new TextEncoder().encode(secret).length > 4096) return "At most 4096 bytes.";
  return undefined;
}

/** The secret a kind takes, named for the person setting it. */
export function secretNoun(kind: ConnectionKind, config: Record<string, string | number>): string | null {
  if (kind === "sql") return "password";
  if (kind === "file") return null;
  switch (config.authType) {
    case "basic":
      return "password";
    case "bearer":
      return "token";
    case "header":
      return "header value";
    case "oauth2_client":
      return "client secret";
    default:
      return null;
  }
}
