import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  importDefinition,
  installStandardDefinitions,
  listDefinitions,
  listStandardDefinitions,
  newKey,
  type DefinitionSummary,
  type InstallResult,
  type Problem,
  type StandardDefinition,
} from "./api/client";
import { KIND_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<DefinitionSummary>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "kind", header: "Kind", render: (d) => KIND_LABELS[d.kind] ?? d.kind },
  { key: "status", header: "Status", render: (d) => <Badge tone={STATUS_TONES[d.status]}>{STATUS_LABELS[d.status]}</Badge> },
  { key: "latestVersion", header: "Published version", mono: true, align: "end", render: (d) => (d.latestVersion > 0 ? `v${d.latestVersion}` : "—") },
  { key: "hasDraft", header: "Draft", render: (d) => (d.hasDraft ? <Badge tone="info">Open draft</Badge> : null) },
  { key: "updatedAt", header: "Updated", mono: true, render: (d) => <time dateTime={d.updatedAt}>{d.updatedAt}</time> },
];

const STATUS_LABELS: Record<DefinitionSummary["status"], string> = { draft: "Draft only", published: "Published", retired: "Retired" };
const STATUS_TONES = { draft: "neutral", published: "success", retired: "warning" } as const;

/** Report definitions (FR-15.2, FR-15.3): every code with its status, newest published version and open draft. */
export function DefinitionsPage() {
  const navigate = useNavigate();
  const [reloads, setReloads] = useState(0);
  const state = useLoad((signal) => listDefinitions(signal), `definitions-${reloads}`);
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
          <ProblemMessage problem={state.error} />
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
      <StandardCard onInstalled={() => setReloads((n) => n + 1)} />
      <ImportCard onImported={(code) => navigate(`/finreport/definitions/${encodeURIComponent(code)}`)} />
    </div>
  );
}

const standardColumns: DataTableColumn<StandardDefinition>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "kind", header: "Kind", render: (d) => KIND_LABELS[d.kind] ?? d.kind },
  { key: "rows", header: "Rows", mono: true, align: "end" },
  { key: "columns", header: "Columns", mono: true, align: "end" },
];

/**
 * The statutory definitions the service ships (FR-15.5, FR-15.14) and the
 * install action. Install publishes version 1 of each code the tenant does not
 * use yet and skips the others, so clicking it twice is harmless.
 */
export function StandardCard({ onInstalled }: { onInstalled(): void }) {
  const state = useLoad((signal) => listStandardDefinitions(signal), "standard-definitions");
  const [results, setResults] = useState<InstallResult[] | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function install() {
    setBusy(true);
    setFailure(null);
    try {
      setResults(await installStandardDefinitions());
      onInstalled();
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  const installed = results?.filter((r) => r.action === "installed") ?? [];
  return (
    <Card
      title="Standard statements"
      actions={
        <Button size="sm" onClick={install} loading={busy} disabled={state.loading || state.error !== null}>
          Install standard definitions
        </Button>
      }
    >
      <div className={styles.stack}>
        <p className={styles.note}>
          Trial balance, balance sheet and income statements (also by function of expense) over the default account ranges. Installing publishes
          version 1 of each code you do not use yet; codes you already have are left as they are.
        </p>
        {state.error ? (
          <Alert tone="danger" title="Could not load the standard definitions" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            <ProblemMessage problem={state.error} />
          </Alert>
        ) : (
          <DataTable
            label="Standard definitions"
            columns={standardColumns}
            rows={state.data ?? []}
            getRowKey={(d) => d.code}
            loading={state.loading}
            empty={{ title: "No standard definitions", description: "This service version ships none." }}
          />
        )}
        {results ? (
          <Alert tone="success" title={installed.length > 0 ? `Installed ${installed.map((r) => r.code).join(", ")}` : "Nothing to install"} live="status">
            <ul className={styles.issues}>
              {results.map((r) => (
                <li key={r.code}>
                  <span className={styles.mono}>{r.code}</span>: {r.action === "installed" ? `published as version ${r.version ?? 1}` : `skipped, ${r.reason ?? "already in use"}`}
                </li>
              ))}
            </ul>
          </Alert>
        ) : null}
        {failure ? (
          <Alert tone="danger" title="The standard definitions were not installed" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
      </div>
    </Card>
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
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
    </Card>
  );
}
