// The migration routes behind the setup guide's import step (/api/v1/integration): one file import each for
// vendors, customers and products, with a mapping, so a visitor can download the template and check a file.
// The preview reads the file the visitor chose, in the browser, and rejects rows without a number or a name the
// way the mapping's required rule does. Nothing is loaded.
import type { BindingPreview, ConnectionProfile, EntityBinding, MappingSpec, PreviewRow } from "../../modules/migration/api/client";
import { reply, route } from "../backend";
import { at, uid } from "../store";

const BASE = "/api/v1/integration";
const KIND_CONNECTION = 480;
const KIND_BINDING = 481;
const KIND_SPEC = 482;
const SAMPLE_ROWS = 100;

const kinds = [
  { entity: "vendor", file: "vendors.csv", columns: ["number", "name", "country", "currency", "payment_terms", "email"], sample: ["V-2001", "Harbor Castings Ltd", "US", "USD", "NET30", "ap@harborcastings.example"] },
  { entity: "customer", file: "customers.csv", columns: ["number", "name", "country", "currency", "payment_terms", "email"], sample: ["C-3001", "Lakeshore Water Works", "US", "USD", "NET30", "orders@lakeshoreww.example"] },
  { entity: "product", file: "products.csv", columns: ["number", "name", "unit", "type", "description"], sample: ["P-9001", "Impeller, bronze, 6 in", "EA", "item", "Cast bronze impeller for the 6 in pump"] },
];
const REQUIRED = ["number", "name"];

const connection = {
  id: uid(KIND_CONNECTION, 1),
  name: "Spreadsheet uploads",
  kind: "file",
  sourceSystem: "custom_spreadsheets",
  config: { directory: "imports" },
  status: "active",
  version: 1,
  createdBy: "dev",
  createdAt: at(-30),
  updatedAt: at(-30),
} as unknown as ConnectionProfile;

const specs: MappingSpec[] = kinds.map((k, i) => ({
  id: uid(KIND_SPEC, i + 1),
  entity: k.entity,
  version: 1,
  checksum: `demo-${k.entity}`,
  fieldCount: k.columns.length,
  testCount: 0,
  naturalKey: ["number"],
  createdAt: at(-30),
  mapping: { entity: k.entity, version: 1, naturalKey: ["number"], fields: k.columns.map((c) => ({ source: c, target: c, type: "string", required: REQUIRED.includes(c) })) },
}));

const bindings = kinds.map(
  (k, i) =>
    ({
      id: uid(KIND_BINDING, i + 1),
      connectionProfileId: connection.id,
      entity: k.entity,
      source: { file: k.file },
      keyFields: ["number"],
      mappingSpecId: specs[i].id,
      status: "active",
      version: 1,
      createdBy: "dev",
      createdAt: at(-30),
      updatedAt: at(-30),
    }) as EntityBinding,
);

route("GET", `${BASE}/connection-profiles`, ({ query }) => ({ items: !query.get("kind") || query.get("kind") === "file" ? [connection] : [] }));
route("GET", `${BASE}/connection-profiles/{id}`, ({ params }) => (params.id === connection.id ? connection : reply.problem(404, "AUTH-0404", "connection not found")));
route("GET", `${BASE}/entity-bindings`, ({ query }) => ({ items: bindings.filter((b) => !query.get("entity") || b.entity === query.get("entity")) }));
route("GET", `${BASE}/entity-bindings/{id}`, ({ params }) => bindings.find((b) => b.id === params.id) ?? reply.problem(404, "AUTH-0404", "binding not found"));
route("GET", `${BASE}/mapping-specs`, ({ query }) => ({ items: specs.filter((s) => !query.get("entity") || s.entity === query.get("entity")).map((s) => ({ ...s, mapping: undefined })) }));
route("GET", `${BASE}/mapping-specs/{id}`, ({ params }) => specs.find((s) => s.id === params.id) ?? reply.problem(404, "AUTH-0404", "mapping spec not found"));
// The pages the step links to: no imports have run yet, and there are no projects or data templates.
for (const list of ["migration-runs", "projects", "data-templates"]) route("GET", `${BASE}/${list}`, () => ({ items: [] }));

/** Splits CSV text into rows of cells; quoted cells may hold commas, quotes and line breaks. */
function parseCsv(text: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let cell = "";
  let quoted = false;
  const body = text.replace(/^﻿/, "");
  for (let i = 0; i < body.length; i += 1) {
    const ch = body[i];
    if (quoted) {
      if (ch === '"' && body[i + 1] === '"') {
        cell += '"';
        i += 1;
      } else if (ch === '"') quoted = false;
      else cell += ch;
    } else if (ch === '"') quoted = true;
    else if (ch === ",") {
      row.push(cell);
      cell = "";
    } else if (ch === "\n" || ch === "\r") {
      if (ch === "\r" && body[i + 1] === "\n") i += 1;
      row.push(cell);
      cell = "";
      if (row.some((c) => c !== "")) rows.push(row);
      row = [];
    } else cell += ch;
  }
  row.push(cell);
  if (row.some((c) => c !== "")) rows.push(row);
  return rows;
}

function decode(contentBase64: string): string {
  return new TextDecoder().decode(Uint8Array.from(atob(contentBase64), (c) => c.charCodeAt(0)));
}

route("POST", `${BASE}/entity-bindings/{id}:preview`, ({ params, body }) => {
  const index = bindings.findIndex((b) => b.id === params.id);
  if (index < 0) return reply.problem(404, "AUTH-0404", "binding not found");
  const kind = kinds[index];
  const options = (body ?? {}) as { limit?: number; upload?: { name: string; contentBase64: string } };
  const table = options.upload ? parseCsv(decode(options.upload.contentBase64)) : [kind.columns, kind.sample];
  const [header = [], ...records] = table;
  const columns = header.map((h) => h.trim());
  const limit = Math.min(Math.max(options.limit ?? 10, 1), SAMPLE_ROWS);
  const rows: PreviewRow[] = records.slice(0, limit).map((record, i) => {
    const source = Object.fromEntries(columns.map((c, j) => [c, (record[j] ?? "").trim()]));
    const rejects = REQUIRED.filter((c) => !source[c]).map((c) => ({ field: c, code: c === "number" ? "MIG-ROW-NO-KEY" : "MIG-MAP-REQUIRED", detail: columns.includes(c) ? `${c} is empty; every row needs one` : `the file has no ${c} column` }));
    const target = Object.fromEntries(kind.columns.filter((c) => source[c]).map((c) => [c, source[c]]));
    return { index: i + 1, sourceKey: source.number || undefined, source, ...(rejects.length > 0 ? { rejects } : { target }) };
  });
  const keys = rows.flatMap((r) => (r.sourceKey ? [r.sourceKey] : []));
  const rejected = rows.filter((r) => (r.rejects ?? []).length > 0).length;
  const preview: BindingPreview = {
    ok: true,
    durationMs: 18,
    mapping: { entity: kind.entity, version: 1, mappingSpecId: specs[index].id },
    rows,
    columns,
    more: records.length > limit,
    mapped: rows.length - rejected,
    rejected,
    duplicateKeys: [...new Set(keys.filter((k, i) => keys.indexOf(k) !== i))],
  };
  return preview;
});
