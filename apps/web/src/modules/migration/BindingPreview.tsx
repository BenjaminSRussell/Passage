import { useState, type ChangeEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type BindingPreview, type MappingSpec, type PreviewOptions, type PreviewRow, type Problem } from "./api/client";
import { MAX_UPLOAD_BYTES, toBase64 } from "./bindingDomain";
import styles from "./pages.module.css";

const LIMITS = [5, 10, 25, 50, 100];

function show(value: unknown): string {
  if (value === null || value === undefined) return "";
  return typeof value === "object" ? JSON.stringify(value) : String(value);
}

function problems(row: PreviewRow): string {
  const rejects = (row.rejects ?? []).map((r) => `${r.field ? `${r.field}: ` : ""}${r.detail} (${r.code})`);
  const warnings = (row.warnings ?? []).map((w) => `${w.field}: ${w.detail} (warning ${w.code})`);
  return [...rejects, ...warnings].join("; ");
}

const rowColumns: DataTableColumn<PreviewRow>[] = [
  { key: "index", header: "#", align: "end" },
  { key: "sourceKey", header: "Source key", mono: true },
  { key: "result", header: "Result", render: (row) => ((row.rejects ?? []).length > 0 ? "Rejected" : row.target ? "Mapped" : "Read") },
  { key: "target", header: "Target record", mono: true, render: (row) => (row.target ? show(row.target) : "") },
  { key: "problems", header: "Rule errors and warnings", render: problems },
];

/**
 * Runs a preview: extracts a few rows, runs the mapping and rules and shows the target records and the rule errors.
 * Nothing is loaded. A file connection can be previewed from a file chosen here instead of the drop directory.
 */
export function PreviewPanel({
  isFile,
  specs,
  defaultSpecId,
  disabledReason,
  run,
}: {
  isFile: boolean;
  specs: MappingSpec[];
  defaultSpecId?: string;
  /** Why the preview cannot run yet (for example no connection chosen); the button is disabled with this note. */
  disabledReason?: string;
  run(options: PreviewOptions): Promise<BindingPreview>;
}) {
  const [limit, setLimit] = useState("10");
  const [specId, setSpecId] = useState("");
  const [upload, setUpload] = useState<{ name: string; contentBase64: string } | null>(null);
  const [uploadError, setUploadError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<BindingPreview | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);

  async function choose(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    setUpload(null);
    setUploadError(undefined);
    if (!file) return;
    if (file.size > MAX_UPLOAD_BYTES) {
      setUploadError("At most 4 MiB for a preview.");
      return;
    }
    setUpload({ name: file.name, contentBase64: toBase64(new Uint8Array(await file.arrayBuffer())) });
  }

  async function submit() {
    setBusy(true);
    setFailure(null);
    setResult(null);
    try {
      const options: PreviewOptions = { limit: Number(limit) };
      if (specId !== "") options.mappingSpecId = specId;
      if (isFile && upload) options.upload = upload;
      setResult(await run(options));
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  const sourceColumns: DataTableColumn<PreviewRow>[] = (result?.columns ?? []).slice(0, 12).map((c) => ({ key: c, header: c, mono: true, render: (row) => show(row.source[c]) }));
  return (
    <Card title="Preview">
      <p className={styles.note}>Extract a few rows, run the mapping and its rules, and see the target records and errors. Nothing is loaded.</p>
      <div className={styles.toolbar}>
        <FormField id="preview-limit" label="Rows">
          {(control) => (
            <Select {...control} value={limit} onChange={(event) => setLimit(event.target.value)}>
              {LIMITS.map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="preview-spec" label="Mapping spec" optional>
          {(control) => (
            <Select {...control} value={specId} onChange={(event) => setSpecId(event.target.value)}>
              <option value="">{defaultSpecId ? "The binding's spec" : "None: show source rows only"}</option>
              {specs.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.entity} v{s.version}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        {isFile ? (
          <FormField id="preview-upload" label="Try a file" optional hint="Instead of the drop directory. At most 4 MiB." error={uploadError}>
            {(control) => <Input {...control} type="file" accept=".csv,.tsv,.json,.jsonl,.ndjson" invalid={Boolean(uploadError)} onChange={choose} />}
          </FormField>
        ) : null}
        <Button variant="primary" loading={busy} disabled={Boolean(disabledReason)} onClick={submit}>
          Preview rows
        </Button>
      </div>
      {disabledReason ? <p className={styles.note}>{disabledReason}</p> : null}
      {busy ? (
        <p className={styles.note} role="status" aria-busy="true">
          Reading rows from the source...
        </p>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="The preview could not run" live="alert">
          {failure.message} ({failure.code})
          {failure.errors ? ` ${Object.entries(failure.errors).map(([k, v]) => `${k}: ${v}`).join("; ")}` : ""}
        </Alert>
      ) : null}
      {result && !result.ok ? (
        <Alert tone="danger" title="The source could not be read" live="alert">
          {result.failure?.message ?? "Unknown failure"} ({result.failure?.code ?? "MIG-PROBE-FAILED"})
        </Alert>
      ) : null}
      {result?.ok ? (
        <>
          <StatGroup label="Preview counts">
            <StatTile label="Rows read" value={result.rows?.length ?? 0} hint={result.more ? "The source has more." : "The whole source."} />
            {result.mapping ? <StatTile label="Mapped" value={result.mapped ?? 0} /> : null}
            {result.mapping ? <StatTile label="Rejected" value={result.rejected ?? 0} tone="danger" /> : null}
            <StatTile label="Repeated keys" value={result.duplicateKeys?.length ?? 0} tone="warning" />
          </StatGroup>
          {result.mapping ? <p className={styles.note}>Mapping for {result.mapping.entity}, version {result.mapping.version}.</p> : <p className={styles.note}>No mapping ran: choose a mapping spec to see target records and rule errors.</p>}
          {(result.duplicateKeys?.length ?? 0) > 0 ? <p className={styles.note}>These source keys repeat in the sample: {result.duplicateKeys?.join(", ")}.</p> : null}
          <DataTable label="Sampled rows" columns={rowColumns} rows={result.rows ?? []} getRowKey={(row) => String(row.index)} empty={{ title: "The source returned no rows", description: "Check the table, endpoint or file, and the connection's account." }} />
          {sourceColumns.length > 0 ? <DataTable label="Source rows" columns={sourceColumns} rows={result.rows ?? []} getRowKey={(row) => String(row.index)} /> : null}
          {(result.columns?.length ?? 0) > 12 ? <p className={styles.note}>Showing the first 12 of {result.columns?.length} source columns.</p> : null}
        </>
      ) : null}
    </Card>
  );
}
