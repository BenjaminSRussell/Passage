import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeProblem, importDefinition, listDefinitions, newKey, type DefinitionSummary, type Problem } from "./api/client";
import { KIND_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<DefinitionSummary>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "kind", header: "Kind", render: (d) => KIND_LABELS[d.kind] ?? d.kind },
  { key: "latestVersion", header: "Published version", mono: true, align: "end", render: (d) => (d.latestVersion > 0 ? `v${d.latestVersion}` : "—") },
  { key: "hasDraft", header: "Draft", render: (d) => (d.hasDraft ? <Badge tone="info">Open draft</Badge> : null) },
  { key: "updatedAt", header: "Updated", mono: true, render: (d) => <time dateTime={d.updatedAt}>{d.updatedAt}</time> },
];

/** Report definitions (FR-15.2, FR-15.3): every code with its newest published version and open draft. */
export function DefinitionsPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listDefinitions(signal), "definitions");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Report definitions"
        description="Rows, columns and formatting of each statement. Publishing a draft creates an immutable version that runs refer to."
        actions={
          <Link className={buttonClassName("primary")} to="/finreport/definitions/new">
            New definition
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the report definitions" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Report definitions"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(d) => d.code}
          loading={state.loading}
          empty={{ title: "No report definitions yet", description: "Create a definition or import one exported from another environment." }}
          onRowOpen={(d) => navigate(`/finreport/definitions/${encodeURIComponent(d.code)}`)}
        />
      )}
      <ImportCard onImported={(code) => navigate(`/finreport/definitions/${encodeURIComponent(code)}`)} />
    </div>
  );
}

/** JSON import of an exported definition (FR-15.11). The import becomes the code's draft. */
export function ImportCard({ onImported }: { onImported(code: string): void }) {
  const [text, setText] = useState("");
  const [overwrite, setOverwrite] = useState(false);
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch {
      setError("Paste the JSON of an exported definition.");
      return;
    }
    setError(undefined);
    setBusy(true);
    try {
      const saved = await importDefinition(parsed, overwrite, key.current);
      key.current = newKey();
      onImported(saved.code);
    } catch (e) {
      const problem = describeProblem(e);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Import a definition">
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="fr-import-json" label="Definition JSON" hint="The file a definition export produced." error={error}>
          {(c) => (
            <Textarea
              {...c}
              className={styles.mono}
              rows={8}
              value={text}
              invalid={Boolean(error)}
              onChange={(e) => {
                setText(e.target.value);
                key.current = newKey();
              }}
            />
          )}
        </FormField>
        <div className={styles.formActions}>
          <span className={styles.check}>
            <Input
              id="fr-import-overwrite"
              type="checkbox"
              checked={overwrite}
              onChange={(e) => {
                setOverwrite(e.target.checked);
                key.current = newKey();
              }}
            />
            <label htmlFor="fr-import-overwrite">Replace an existing definition with the same code</label>
          </span>
          <Button type="submit" loading={busy}>
            Import
          </Button>
        </div>
      </form>
      {failure ? (
        <Alert tone="danger" title="The definition was not imported" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
    </Card>
  );
}
