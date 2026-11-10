import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Skeleton, StatGroup, StatTile, Textarea, type DataTableColumn } from "../../shell/ui";
import { createCountSession, describeProblem, getCountSession, listCountSessions, newKey, type CountLine, type CountSession, type NewCountSession, type Problem } from "./api/client";
import { StatusBadge } from "./badges";
import { DATE, dash, today, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** The service accepts at most 5000 scans per session, each 1 to 64 characters. */
export const MAX_SCANS = 5000;

const listColumns: DataTableColumn<CountSession>[] = [
  { key: "countedDate", header: "Counted", mono: true },
  { key: "location", header: "Location" },
  { key: "expectedCount", header: "Expected", align: "end" },
  { key: "foundCount", header: "Found", align: "end" },
  { key: "missingCount", header: "Missing", align: "end" },
  { key: "unknownCount", header: "Unknown", align: "end" },
];

/** Physical inventory count sessions (FR-37.11), newest first. */
export function CountSessionsPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listCountSessions(undefined, signal), "count-sessions");
  const [more, setMore] = useState<CountSession[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);

  useEffect(() => {
    setMore([]);
    setMoreError(null);
    setCursor(state.data?.nextCursor);
  }, [state.data]);

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listCountSessions(cursor);
      setMore((rows) => [...rows, ...page.items]);
      setCursor(page.nextCursor);
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Count sessions"
        description="Physical inventory: scanned barcodes reconciled against the open assets of a legal entity. Open a session to see what was found, missing or not in the register."
        actions={
          <Link className={buttonClassName("primary")} to="/assets/count-sessions/new">
            New count
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load count sessions" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Count sessions"
          columns={listColumns}
          rows={[...(state.data?.items ?? []), ...more]}
          getRowKey={(c) => c.id}
          loading={state.loading}
          empty={{ title: "No count sessions yet", description: "Record a count to reconcile the register with what is on site." }}
          onRowOpen={(c) => navigate(`/assets/count-sessions/${c.id}`)}
        />
      )}
      {moreError ? (
        <Alert tone="danger" title="Could not load more sessions" live="alert">
          <ProblemMessage problem={moreError} />
        </Alert>
      ) : null}
      {cursor && !state.error ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

/** One barcode per line (commas also separate); blank lines are dropped and repeats kept. */
export function parseBarcodes(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((code) => code.trim())
    .filter((code) => code !== "");
}

export interface CountValues {
  legalEntityId: string;
  location: string;
  countedDate: string;
  barcodes: string;
}

type CountErrors = Partial<Record<keyof CountValues, string>>;

/** Checks that mirror the service (internal/api createCountSession). */
export function validateCount(values: CountValues): { errors: CountErrors; input?: NewCountSession } {
  const errors: CountErrors = {};
  const location = values.location.trim();
  const barcodes = parseBarcodes(values.barcodes);
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (location.length < 1 || location.length > 80) errors.location = "Enter where you counted, up to 80 characters.";
  if (!DATE.test(values.countedDate)) errors.countedDate = "Choose the count date.";
  if (barcodes.length > MAX_SCANS) errors.barcodes = `Enter at most ${MAX_SCANS} scans per session.`;
  else {
    const long = barcodes.find((b) => b.length > 64);
    if (long) errors.barcodes = `"${long.slice(0, 20)}…" is longer than 64 characters.`;
  }
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { legalEntityId: values.legalEntityId.trim(), location, countedDate: values.countedDate, barcodes } };
}

/** Record a count session: paste or scan barcodes, one per line (FR-37.11). */
export function NewCountSessionPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<CountValues>(() => ({ legalEntityId: "", location: "", countedDate: today(), barcodes: "" }));
  const [errors, setErrors] = useState<CountErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const scans = parseBarcodes(values.barcodes).length;

  function update(patch: Partial<CountValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateCount(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const session = await createCountSession(input, key.current);
      navigate(`/assets/count-sessions/${session.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as CountErrors);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (name: Exclude<keyof CountValues, "barcodes">, label: string, hint?: string, extra: { type?: string; mono?: boolean } = {}) => (
    <FormField id={`count-${name}`} label={label} hint={hint} error={errors[name]}>
      {(control) => (
        <Input
          {...control}
          type={extra.type ?? "text"}
          className={extra.mono ? styles.mono : undefined}
          value={values[name]}
          onChange={(e) => update({ [name]: e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New count session"
        description="Saving reconciles the scans against every open asset of the legal entity that has a barcode. The register is not changed: missing and unknown items are listed for follow-up."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New count session">
          {text("legalEntityId", "Legal entity", "UUID of the legal entity being counted.", { mono: true })}
          {text("location", "Location", "Where the count took place, such as Warehouse 1.")}
          {text("countedDate", "Count date", undefined, { type: "date" })}
          <div className={styles.variant}>
            <FormField id="count-barcodes" label="Scanned barcodes" hint={`One per line. ${scans} scanned; repeats count once.`} error={errors.barcodes}>
              {(control) => <Textarea {...control} mono rows={8} value={values.barcodes} onChange={(e) => update({ barcodes: e.target.value })} invalid={Boolean(errors.barcodes)} />}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not record the count" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Reconcile count
            </Button>
            <Link className={buttonClassName("ghost")} to="/assets/count-sessions">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

const lineColumns: DataTableColumn<CountLine>[] = [
  { key: "outcome", header: "Outcome", render: (l) => <StatusBadge status={l.outcome} /> },
  { key: "barcode", header: "Barcode", mono: true },
  { key: "assetNumber", header: "Asset", mono: true, render: (l) => l.assetNumber ?? dash },
  { key: "assetName", header: "Name", render: (l) => l.assetName ?? "Not in the register" },
];

/** A count session: expected, found, missing and unknown, with the AST-0525 warnings. */
export function CountSessionDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getCountSession(id, signal), id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Count session" />
        <Alert tone="danger" title="Could not load the count session" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
        <Link className={buttonClassName("ghost")} to="/assets/count-sessions">
          Back to count sessions
        </Link>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Count session" />
        <span className="visually-hidden">Loading the count session…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const session = state.data;
  const warnings = session.warnings ?? [];

  return (
    <div className={styles.stack}>
      <PageHeader title={`Count at ${session.location}`} description={`Counted on ${session.countedDate} for legal entity ${session.legalEntityId}.`} />
      <StatGroup label="Count result">
        <StatTile label="Expected" value={session.expectedCount} />
        <StatTile label="Found" value={session.foundCount} />
        <StatTile label="Missing" value={session.missingCount} tone="danger" />
        <StatTile label="Unknown" value={session.unknownCount} tone="warning" />
      </StatGroup>
      {session.duplicateCount > 0 ? <p className={styles.note}>{session.duplicateCount} repeated scans were counted once.</p> : null}
      {warnings.length > 0 ? (
        <Alert tone="warning" title={`${warnings.length} barcodes are not in the register`}>
          {warnings.join(" ")}
        </Alert>
      ) : null}
      <Card title="Lines">
        <DataTable
          label="Count lines"
          columns={lineColumns}
          rows={session.lines ?? []}
          getRowKey={(l) => l.barcode}
          empty={{ title: "Nothing to reconcile", description: "The entity has no open asset with a barcode and nothing was scanned." }}
          onRowOpen={(l) => (l.assetId ? navigate(`/assets/${l.assetId}`) : undefined)}
        />
      </Card>
      <div>
        <Link className={buttonClassName("ghost")} to="/assets/count-sessions">
          Back to count sessions
        </Link>
      </div>
    </div>
  );
}
