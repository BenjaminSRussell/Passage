import { useRef, useState, type FormEvent } from "react";
import { useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, Card, ConfirmDialog, DataTable, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import {
  deprecateCatalogEntry,
  DUTY_HUB_CATALOG_WRITE,
  getCatalogEntry,
  getCatalogSpec,
  MIN_DEPRECATION_MONTHS,
  retireCatalogEntry,
  type CatalogDetail,
  type CatalogOperation,
} from "./api/hubClient";
import { LIFECYCLE_TONES } from "./CatalogPage";
import { dash, formatDateTime, UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemAlert } from "./parts";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const DAY = /^\d{4}-\d{2}-\d{2}$/;

/**
 * The first day (UTC, YYYY-MM-DD) a sunset may be set to. The hub needs the sunset at or after
 * `now` plus `months` calendar months (clamped to a shorter month's end), and the form sends the
 * day as midnight UTC, so that day only qualifies when `now` is itself midnight; otherwise it is
 * the next day.
 */
export function earliestSunset(now: Date, months: number = MIN_DEPRECATION_MONTHS): string {
  const d = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1));
  d.setUTCMonth(d.getUTCMonth() + months);
  const last = new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + 1, 0)).getUTCDate();
  d.setUTCDate(Math.min(now.getUTCDate(), last));
  if (now.getTime() > Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate())) {
    d.setUTCDate(d.getUTCDate() + 1);
  }
  return d.toISOString().slice(0, 10);
}

function operationColumns(kind: CatalogDetail["kind"]): DataTableColumn<CatalogOperation>[] {
  return kind === "api"
    ? [
        { key: "method", header: "Method", mono: true },
        { key: "path", header: "Path", mono: true },
        { key: "id", header: "Operation", mono: true, render: (row) => row.id ?? dash },
        { key: "summary", header: "Summary", render: (row) => row.summary ?? dash },
        { key: "duty", header: "Duty", mono: true, render: (row) => row.duty ?? dash },
      ]
    : [
        { key: "path", header: "Event type", mono: true },
        { key: "summary", header: "Summary", render: (row) => row.summary ?? dash },
      ];
}

function Term({ term, children, mono }: { term: string; children: React.ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/** Deprecate: a sunset date at least 12 months ahead and, optionally, the entry that replaces this one. */
function DeprecateCard({ entry, onSaved }: { entry: CatalogDetail; onSaved(): void }) {
  const [sunset, setSunset] = useState("");
  const [replacedBy, setReplacedBy] = useState("");
  const [errors, setErrors] = useState<{ sunset?: string; replacedBy?: string }>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const saving = useRef(false);
  const earliest = earliestSunset(new Date());

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (saving.current) return;
    const replacement = replacedBy.trim().toLowerCase();
    const found = {
      ...(!DAY.test(sunset) || sunset < earliest ? { sunset: `Choose a date on or after ${earliest}: a deprecation needs at least ${MIN_DEPRECATION_MONTHS} months of notice.` } : {}),
      ...(replacement !== "" && !UUID.test(replacement) ? { replacedBy: "Enter the id of the replacing catalog entry (a UUID) or leave it empty." } : {}),
    };
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    saving.current = true;
    setBusy(true);
    try {
      await deprecateCatalogEntry(entry.id, { version: entry.version, sunsetAt: `${sunset}T00:00:00Z`, ...(replacement !== "" ? { replacedBy: replacement } : {}) });
      onSaved();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem.code === "HUB-0409" ? { ...problem, message: "The entry changed since you opened it. Reload and try again." } : problem);
    } finally {
      saving.current = false;
      setBusy(false);
    }
  }

  return (
    <Card title="Deprecate">
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="catalog-sunset" label="Sunset date" hint={`The earliest allowed date is ${earliest}. Integrators are notified of this date.`} error={errors.sunset}>
          {(control) => <Input {...control} type="date" min={earliest} value={sunset} onChange={(event) => setSunset(event.target.value)} invalid={Boolean(errors.sunset)} />}
        </FormField>
        <FormField id="catalog-replaced-by" label="Replaced by" optional hint="The id of an active entry of the same kind." error={errors.replacedBy}>
          {(control) => <Input {...control} spellCheck={false} value={replacedBy} onChange={(event) => setReplacedBy(event.target.value)} invalid={Boolean(errors.replacedBy)} />}
        </FormField>
        {failure ? (
          <div className={styles.wide}>
            <ProblemAlert problem={failure} title="Could not deprecate the entry" />
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="danger" disabled={busy} loading={busy}>
            Deprecate
          </Button>
        </div>
      </form>
    </Card>
  );
}

function SpecCard({ id }: { id: string }) {
  const [text, setText] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);

  async function show() {
    setBusy(true);
    setFailure(null);
    try {
      setText(JSON.stringify(await getCatalogSpec(id), null, 2));
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Document">
      <div className={styles.stack}>
        {text === null ? (
          <div>
            <Button onClick={show} loading={busy}>
              Show document
            </Button>
          </div>
        ) : (
          <pre className={styles.payload} tabIndex={0} aria-label="Published document">
            {text}
          </pre>
        )}
        {failure ? <ProblemAlert problem={failure} title="Could not load the document" /> : null}
      </div>
    </Card>
  );
}

/** One catalog entry: metadata, operations, documentation gaps, the document, deprecate and retire (FR-35.8). */
export function CatalogEntryPage() {
  const { id = "" } = useParams();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_HUB_CATALOG_WRITE);
  const state = useLoad((signal) => getCatalogEntry(id, signal), `hub-catalog-entry:${id}`);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const retiring = useRef(false);

  async function retire(version: number) {
    if (retiring.current) return;
    retiring.current = true;
    setConfirming(false);
    setBusy(true);
    setFailure(null);
    try {
      await retireCatalogEntry(id, version);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      retiring.current = false;
      setBusy(false);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Catalog entry" />
        <Alert tone="danger" title="Could not load the catalog entry" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  const entry = state.data;
  if (!entry) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Catalog entry" />
        <Skeleton />
      </div>
    );
  }
  const pastSunset = entry.lifecycle === "deprecated" && entry.sunsetAt !== null && new Date(entry.sunsetAt).getTime() <= Date.now();
  const tone = LIFECYCLE_TONES[entry.lifecycle];
  return (
    <div className={styles.stack}>
      <PageHeader
        title={entry.title}
        description={entry.summary || (entry.kind === "api" ? "A published OpenAPI document." : "A published AsyncAPI document.")}
        actions={
          canWrite && pastSunset ? (
            <Button variant="danger" onClick={() => setConfirming(true)} disabled={busy}>
              Retire entry
            </Button>
          ) : undefined
        }
      />
      {entry.lifecycle === "deprecated" ? (
        <Alert tone="warning" title="Deprecated" live="status">
          This version ends on {formatDateTime(entry.sunsetAt)}. {entry.replacedBy ? `Its replacement is ${entry.replacedBy}.` : "No replacement is named."}
        </Alert>
      ) : null}
      {entry.lifecycle === "retired" ? (
        <Alert tone="info" title="Retired" live="status">
          Retired on {formatDateTime(entry.retiredAt)}. The document stays readable.
        </Alert>
      ) : null}
      {failure ? <ProblemAlert problem={failure} title="Could not retire the entry" /> : null}
      <Card title="Contract">
        <dl className={styles.details}>
          <Term term="Lifecycle">
            <Badge tone={tone.tone}>{tone.label}</Badge>
          </Term>
          <Term term="Kind">{entry.kind === "api" ? "API (OpenAPI)" : "Events (AsyncAPI)"}</Term>
          <Term term="Name and version" mono>
            {entry.name} {entry.specVersion}
          </Term>
          <Term term="Owner service" mono>
            {entry.ownerService}
          </Term>
          <Term term="Operations">{entry.operationCount}</Term>
          <Term term="Published">{formatDateTime(entry.createdAt)}</Term>
          {entry.deprecatedAt ? <Term term="Deprecated">{formatDateTime(entry.deprecatedAt)}</Term> : null}
          {entry.sunsetAt ? <Term term="Sunset">{formatDateTime(entry.sunsetAt)}</Term> : null}
        </dl>
      </Card>
      {entry.issues.length > 0 ? (
        <Alert tone="warning" title={`${entry.issues.length} documentation gap${entry.issues.length === 1 ? "" : "s"}`} live="status">
          <ul className={styles.plainList} aria-label="Documentation gaps">
            {entry.issues.map((issue) => (
              <li key={issue}>{issue}</li>
            ))}
          </ul>
        </Alert>
      ) : null}
      <Card title={entry.kind === "api" ? "Operations" : "Events"}>
        <DataTable
          label={entry.kind === "api" ? "Operations" : "Events"}
          columns={operationColumns(entry.kind)}
          rows={entry.operations}
          getRowKey={(row) => `${row.method} ${row.path} ${row.id ?? ""}`}
          empty={{ title: "No operations" }}
        />
      </Card>
      <SpecCard id={entry.id} />
      {canWrite && entry.lifecycle === "active" ? <DeprecateCard key={entry.version} entry={entry} onSaved={state.reload} /> : null}
      {confirming ? (
        <ConfirmDialog
          title="Retire this entry?"
          description={`${entry.name} ${entry.specVersion} has passed its sunset date. Retiring marks it as no longer offered; the document stays readable. This cannot be undone.`}
          confirmLabel="Retire entry"
          cancelLabel="Keep entry"
          onConfirm={() => retire(entry.version)}
          onCancel={() => setConfirming(false)}
        />
      ) : null}
    </div>
  );
}
