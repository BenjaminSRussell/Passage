import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getRollForward,
  listRuns,
  periodEndOf,
  runRecognition,
  type Problem,
  type RecognitionRun,
  type RollForward,
} from "./api/client";
import { CURRENCY, lastMonth, PERIOD, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Errors = Record<string, string>;

export interface RollForwardQuery {
  legalEntityId: string;
  currency: string;
  period: string;
}

/** Client checks that mirror GET /revrec/roll-forward. */
export function validateQuery(q: RollForwardQuery): Errors {
  const errors: Errors = {};
  if (!UUID.test(q.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!CURRENCY.test(q.currency.trim())) errors.currency = "Enter a three-letter currency code such as USD.";
  if (!PERIOD.test(q.period.trim())) errors.period = "Enter the month as YYYY-MM, for example 2027-03.";
  return errors;
}

const runColumns: DataTableColumn<RecognitionRun>[] = [
  { key: "periodEnd", header: "Period end", mono: true },
  { key: "lineCount", header: "Lines recognized", mono: true, align: "end" },
  { key: "createdAt", header: "Run at", mono: true, render: (r) => r.createdAt.replace("T", " ").replace("Z", " UTC") },
];

function RollForwardTiles({ rf }: { rf: RollForward }) {
  const money = (v: string) => `${v} ${rf.currency}`;
  return (
    <StatGroup label={`Deferred revenue ${rf.period}`}>
      <StatTile label="Opening" value={money(rf.opening)} hint={`Deferred before ${rf.start}`} />
      <StatTile label="Additions" value={money(rf.additions)} hint="Deferred obligations of new contracts" />
      <StatTile label="Recognized" value={money(rf.recognized)} hint="Milestones and ratable lines" />
      <StatTile label="Adjustments" value={money(rf.adjustments)} hint="Credit-note reversals" />
      <StatTile label="Closing" value={money(rf.closing)} hint={`Deferred at ${rf.end}`} />
    </StatGroup>
  );
}

/** Deferred revenue roll-forward (FR-29.9) and the period recognition run (FR-29.8). */
export function DeferredRevenuePage() {
  const [form, setForm] = useState<RollForwardQuery>({ legalEntityId: "", currency: "USD", period: lastMonth() });
  const [errors, setErrors] = useState<Errors>({});
  const [query, setQuery] = useState<RollForwardQuery | null>(null);
  const [running, setRunning] = useState(false);
  const [runFailure, setRunFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState("");

  const key = query ? `${query.legalEntityId}|${query.currency}|${query.period}` : "";
  const rollForward = useLoad((signal) => (query ? getRollForward(query.legalEntityId, query.currency, query.period, signal) : Promise.resolve(null)), key);
  const runs = useLoad((signal) => (query ? listRuns(query.legalEntityId, signal) : Promise.resolve([] as RecognitionRun[])), query?.legalEntityId ?? "");

  function update(patch: Partial<RollForwardQuery>) {
    setForm((prev) => ({ ...prev, ...patch }));
  }

  function show(event: FormEvent) {
    event.preventDefault();
    const found = validateQuery(form);
    setErrors(found);
    setRunFailure(null);
    setDone("");
    if (Object.keys(found).length > 0) return;
    setQuery({ legalEntityId: form.legalEntityId.trim(), currency: form.currency.trim(), period: form.period.trim() });
  }

  async function run(q: RollForwardQuery) {
    setRunning(true);
    setRunFailure(null);
    setDone("");
    try {
      const result = await runRecognition(q.legalEntityId, periodEndOf(q.period));
      setDone(`Recognition run through ${result.periodEnd}: ${result.lineCount} ratable ${result.lineCount === 1 ? "line" : "lines"} recognized.`);
      rollForward.reload();
      runs.reload();
    } catch (error) {
      setRunFailure(describeProblem(error));
    } finally {
      setRunning(false);
    }
  }

  function field(id: string, name: keyof RollForwardQuery, label: string, hint?: string) {
    return (
      <FormField id={id} label={label} hint={hint} error={errors[name]}>
        {(c) => <Input {...c} className={styles.mono} value={form[name]} onChange={(e) => update({ [name]: e.target.value })} invalid={Boolean(errors[name])} />}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Deferred revenue"
        description="The month's roll-forward of deferred revenue (opening, additions, recognized, adjustments, closing) and the period recognition run that moves due ratable revenue out of deferral."
        actions={
          <Link className={buttonClassName("ghost")} to="/billing/revenue-contracts">
            Revenue contracts
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={show} noValidate>
          {field("df-entity", "legalEntityId", "Legal entity id")}
          {field("df-currency", "currency", "Currency")}
          {field("df-period", "period", "Month", "YYYY-MM")}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Show roll-forward
            </Button>
          </div>
        </form>
      </Card>
      {!query ? (
        <Card>
          <EmptyState icon="info" title="Choose an entity and month" description="Enter a legal entity, currency and month to see its deferred revenue and recognition runs." />
        </Card>
      ) : (
        <>
          <Alert tone="success" live="status">
            {done}
          </Alert>
          {runFailure ? (
            <Alert tone="danger" title={runFailure.code === "REV-0007" ? "That month has not ended yet" : "The recognition run failed"} live="alert">
              {runFailure.message} ({runFailure.code})
            </Alert>
          ) : null}
          <Card
            title={`Roll-forward ${query.period} (${query.currency})`}
            actions={
              <Button variant="secondary" loading={running} onClick={() => run(query)}>
                {`Run recognition through ${periodEndOf(query.period)}`}
              </Button>
            }
          >
            {rollForward.error ? (
              <Alert tone="danger" title="Could not load the roll-forward" live="alert" action={<Button size="sm" onClick={rollForward.reload}>Try again</Button>}>
                {rollForward.error.message} ({rollForward.error.code})
              </Alert>
            ) : rollForward.data ? (
              <div className={styles.stack}>
                <RollForwardTiles rf={rollForward.data} />
                <p className={styles.note}>
                  Closing = opening + additions − recognized ± adjustments. The tie to general ledger account 2400 arrives with the ledger postings.
                </p>
              </div>
            ) : (
              <div aria-busy="true">
                <span className="visually-hidden">Loading the roll-forward…</span>
                <Skeleton height="tile" />
              </div>
            )}
          </Card>
          <Card title="Recognition runs">
            {runs.error ? (
              <Alert tone="danger" title="Could not load the recognition runs" live="alert" action={<Button size="sm" onClick={runs.reload}>Try again</Button>}>
                {runs.error.message} ({runs.error.code})
              </Alert>
            ) : (
              <DataTable
                label="Recognition runs"
                columns={runColumns}
                rows={runs.data ?? []}
                getRowKey={(r) => r.id}
                loading={runs.loading}
                empty={{ title: "No recognition runs yet", description: "Run recognition after a month closes to recognize its ratable revenue." }}
              />
            )}
          </Card>
        </>
      )}
    </div>
  );
}
