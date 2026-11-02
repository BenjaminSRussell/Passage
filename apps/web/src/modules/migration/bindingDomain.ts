import type { BindingInput, BindingSource, ConnectionKind, CursorType, EntityBinding } from "./api/client";

/** One text input of a binding's source, per connection kind. */
export interface SourceField {
  key: string;
  label: string;
  hint?: string;
  required?: boolean;
  /** Rendered as a multi-line box. */
  multiline?: boolean;
  options?: string[];
}

export const PAGING_STYLES = ["none", "page", "offset", "cursor", "link", "odata", "skiptoken"];

export const SOURCE_FIELDS: Record<ConnectionKind, SourceField[]> = {
  sql: [
    { key: "schema", label: "Schema", hint: "Optional. Letters, digits and _ $ # only." },
    { key: "table", label: "Table or view", hint: "Give a table or a query, not both. Oracle names are case-sensitive: type them as the catalog shows." },
    { key: "columns", label: "Columns", hint: "Optional, comma separated. Must include the key and cursor fields." },
    { key: "query", label: "Read-only query", multiline: true, hint: "One SELECT (it may start with WITH). Use :watermark for the last change stamp of a delta. Do not add ORDER BY or a row limit; Passage pages by key. Use a read-only database login." },
  ],
  rest: [
    { key: "path", label: "Endpoint path", required: true, hint: "Under the connection's base URL, for example /api/customers." },
    { key: "recordsPath", label: "Records path", hint: "Where the record array is in the JSON, for example data.items. Leave empty when the body is the array." },
    { key: "pagingStyle", label: "Paging", options: PAGING_STYLES, hint: "How the endpoint pages. Default none: one request." },
    { key: "pageSize", label: "Page size", hint: "1 to 1000." },
    { key: "limitParam", label: "Page size parameter", hint: "For example limit or $top." },
    { key: "pageParam", label: "Page parameter", hint: "For page paging." },
    { key: "offsetParam", label: "Offset parameter", hint: "For offset paging." },
    { key: "cursorParam", label: "Cursor parameter", hint: "For cursor paging: the query parameter that carries the next cursor." },
    { key: "cursorPath", label: "Next cursor path", hint: "For cursor paging: where the next cursor is in the response, for example meta.next." },
    { key: "changedSinceParam", label: "Changed-since parameter", hint: "Optional. The query parameter that carries the last change stamp, so the source filters." },
    { key: "params", label: "Fixed parameters", multiline: true, hint: "Optional, one name=value per line. No credentials: those belong in the connection." },
  ],
  file: [
    { key: "file", label: "File name", hint: "A file in the connection's drop directory. Give a name or a pattern, not both." },
    { key: "filePattern", label: "File name pattern", hint: "For example Cust_*.csv. The newest match by name is read." },
    { key: "recordsPath", label: "Records path", hint: "For json files: where the record array is." },
  ],
};

export const CURSOR_TYPES: CursorType[] = ["timestamp", "int", "rowversion"];
export const CURSOR_LABELS: Record<CursorType, string> = { timestamp: "Timestamp", int: "Integer", rowversion: "Row version" };

export interface BindingForm {
  connectionProfileId: string;
  entity: string;
  keyFields: string;
  cursorField: string;
  cursorType: CursorType | "";
  mappingSpecId: string;
  source: Record<string, string>;
}

export const EMPTY_BINDING_FORM: BindingForm = { connectionProfileId: "", entity: "", keyFields: "", cursorField: "", cursorType: "", mappingSpecId: "", source: {} };

const ENTITY_RE = /^[a-z][a-z0-9_.-]{0,63}$/;

export function splitList(text: string): string[] {
  return text
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

/** Source text fields to the object the service takes; empty fields are left out. */
export function buildSource(kind: ConnectionKind, text: Record<string, string>): { source: BindingSource; errors: Record<string, string> } {
  const errors: Record<string, string> = {};
  const source: BindingSource = {};
  const get = (k: string) => (text[k] ?? "").trim();
  const put = (k: string) => {
    if (get(k) !== "") source[k] = get(k);
  };
  switch (kind) {
    case "sql": {
      put("schema");
      put("table");
      put("query");
      if (get("columns") !== "") source.columns = splitList(get("columns"));
      if (get("table") === "" && get("query") === "") errors.table = "Give a table or a query.";
      if (get("table") !== "" && get("query") !== "") errors.query = "Give a table or a query, not both.";
      break;
    }
    case "rest": {
      put("path");
      put("recordsPath");
      put("changedSinceParam");
      if (get("path") === "") errors.path = "Enter the endpoint path.";
      else if (!get("path").startsWith("/")) errors.path = "Start the path with /.";
      const params: Record<string, string> = {};
      for (const line of (text.params ?? "").split("\n")) {
        const t = line.trim();
        if (t === "") continue;
        const at = t.indexOf("=");
        if (at < 1) errors.params = "Write each parameter as name=value.";
        else params[t.slice(0, at).trim()] = t.slice(at + 1).trim();
      }
      if (Object.keys(params).length > 0) source.params = params;
      const style = get("pagingStyle");
      const paging: Record<string, unknown> = {};
      if (style !== "") paging.style = style;
      for (const k of ["limitParam", "pageParam", "offsetParam", "cursorParam", "cursorPath"]) if (get(k) !== "") paging[k] = get(k);
      if (get("pageSize") !== "") {
        const n = Number(get("pageSize"));
        if (!Number.isInteger(n) || n < 1 || n > 1000) errors.pageSize = "Enter a whole number from 1 to 1000.";
        else paging.pageSize = n;
      }
      if (style === "cursor" && get("cursorPath") === "") errors.cursorPath = "Cursor paging needs the path of the next cursor.";
      if (Object.keys(paging).length > 0) source.paging = paging;
      break;
    }
    case "file": {
      put("file");
      put("filePattern");
      put("recordsPath");
      if ((get("file") === "") === (get("filePattern") === "")) errors.file = "Give a file name or a pattern, not both.";
      break;
    }
  }
  return { source, errors };
}

/** A stored source object back to the text a form shows. */
export function sourceToText(kind: ConnectionKind, source: BindingSource): Record<string, string> {
  const out: Record<string, string> = {};
  for (const f of SOURCE_FIELDS[kind]) {
    const v = source[f.key];
    if (typeof v === "string") out[f.key] = v;
  }
  if (Array.isArray(source.columns)) out.columns = source.columns.join(", ");
  if (kind === "rest") {
    const params = source.params as Record<string, string> | undefined;
    if (params) out.params = Object.entries(params).map(([k, v]) => `${k}=${v}`).join("\n");
    const paging = (source.paging ?? {}) as Record<string, unknown>;
    if (typeof paging.style === "string") out.pagingStyle = paging.style;
    for (const k of ["limitParam", "pageParam", "offsetParam", "cursorParam", "cursorPath"]) if (typeof paging[k] === "string") out[k] = paging[k] as string;
    if (typeof paging.pageSize === "number") out.pageSize = String(paging.pageSize);
  }
  return out;
}

/** Validates a binding form the way the service does, so an obvious mistake is caught before the request. */
export function parseBindingForm(form: BindingForm, kind: ConnectionKind | null): { value?: BindingInput; errors: Record<string, string> } {
  const errors: Record<string, string> = {};
  if (form.connectionProfileId === "" || kind === null) errors.connectionProfileId = "Choose the connection.";
  const entity = form.entity.trim();
  if (!ENTITY_RE.test(entity)) errors.entity = "Lowercase letters, digits and _ . - , starting with a letter, at most 64 characters (for example customer).";
  const keys = splitList(form.keyFields);
  if (keys.length < 1 || keys.length > 5) errors.keyFields = "Give one to five key fields, comma separated.";
  else if (new Set(keys).size !== keys.length) errors.keyFields = "A key field is repeated.";
  const cursorField = form.cursorField.trim();
  if (cursorField !== "" && form.cursorType === "") errors.cursorType = "Choose the cursor type.";
  if (cursorField === "" && form.cursorType !== "") errors.cursorField = "Enter the cursor field.";
  let source: BindingSource = {};
  if (kind !== null) {
    const built = buildSource(kind, form.source);
    source = built.source;
    for (const [k, v] of Object.entries(built.errors)) errors[`source.${k}`] = v;
  }
  if (Object.keys(errors).length > 0) return { errors };
  const value: BindingInput = { connectionProfileId: form.connectionProfileId, entity, source, keyFields: keys };
  if (cursorField !== "" && form.cursorType !== "") {
    value.cursorField = cursorField;
    value.cursorType = form.cursorType;
  }
  if (form.mappingSpecId !== "") value.mappingSpecId = form.mappingSpecId;
  return { value, errors };
}

export function describeSource(kind: ConnectionKind, b: Pick<EntityBinding, "source">): string {
  const s = b.source as Record<string, unknown>;
  if (kind === "sql") return typeof s.query === "string" ? "Query" : [s.schema, s.table].filter((x) => typeof x === "string" && x !== "").join(".");
  if (kind === "rest") return String(s.path ?? "");
  return String(s.file ?? s.filePattern ?? "");
}

/** Base64 of file bytes for an upload preview; the browser reads the file, the service never sees a path. */
export function toBase64(bytes: Uint8Array): string {
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(bin);
}

export const MAX_UPLOAD_BYTES = 4 * 1024 * 1024;
