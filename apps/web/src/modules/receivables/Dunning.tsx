import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  createDunningRun,
  describeProblem,
  getDunningRun,
  listDunningRuns,
  moveDunningRun,
  newKey,
  RUN_STATUSES,
  type DunningLetter,
  type DunningRun,
  type DunningRunSummary,
  type Exclusion,
  type Problem,
  type RunStatus,
} from "./api/client";
import { dateTime, DAY, EXCLUSION_LABELS, money, RUN_LABELS, RUN_TONES, shortId, today, UUID } from "./format";
import { CommandFailure, Detail, isStale, keepKeyAfter, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { EntityName } from "../../shell/lookup/EntityName";
import { PlainName } from "../sales/OrderNumber";

function runBadge(status: RunStatus) {
  return <Badge tone={RUN_TONES[status] ?? "neutral"}>{RUN_LABELS[status] ?? status}</Badge>;
}

const runColumns: DataTableColumn<DunningRunSummary>[] = [
  { key: "runDate", header: "Run date", mono: true },
  { key: "legalEntityId", header: "Legal entity", render: (row) => <EntityName entity="legalEntity" id={row.legalEntityId} /> },
  { key: "graceDays", header: "Grace days", mono: true, align: "end" },
  { key: "letterCount", header: "Letters", mono: true, align: "end" },
  { key: "issuedAt", header: "Issued", mono: true, render: (row) => (row.issuedAt ? dateTime(row.issuedAt) : "Not yet") },
  { key: "status", header: "Status", render: (row) => runBadge(row.status) },
];

/** Dunning runs (FR-30.7): proposals in draft, reviewed, issued or cancelled. */
export function DunningRunsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<RunStatus | "">("");
  const state = useLoad((signal) => listDunningRuns(status, signal), status);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Dunning runs"
        description="Latest run first. A run proposes letters as a draft; review it, then issue it to raise levels and post fees."
        actions={
          <Link className={buttonClassName("primary")} to="/receivables/dunning-runs/new">
            New dunning run
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="dr-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as RunStatus | "")}>
              <option value="">All statuses</option>
              {RUN_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {RUN_LABELS[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <LoadFailure title="Could not load the dunning runs" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Dunning runs"
          columns={runColumns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No dunning runs", description: "Start a run to see which customers get a letter and at which level." }}
          onRowOpen={(row) => navigate(`/receivables/dunning-runs/${row.id}`)}
        />
      )}
    </div>
  );
}

type Errors = Record<string, string>;

export interface RunDraft {
  legalEntityId: string;
  runDate: string;
  graceDays: string;
}

export function validateRun(v: RunDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Choose a legal entity.";
  if (!DAY.test(v.runDate)) errors.runDate = "Enter the run date.";
  const grace = Number(v.graceDays);
  if (!/^\d{1,2}$/.test(v.graceDays.trim()) || grace > 60) errors.graceDays = "Enter whole days from 0 to 60.";
  return errors;
}

/** Propose a dunning run. Nothing changes on the items until the run is issued. */
export function NewDunningRunPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<RunDraft>({ legalEntityId: "", runDate: today(), graceDays: "5" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<RunDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateRun(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const run = await createDunningRun(
        { legalEntityId: values.legalEntityId.trim(), runDate: values.runDate, graceDays: Number(values.graceDays) },
        key.current,
      );
      navigate(`/receivables/dunning-runs/${run.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (!keepKeyAfter(error)) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New dunning run"
        description="Levels: 1 after the grace days, 2 at 15 days overdue, 3 at 30 (fee 25.00 USD / 20.00 EUR), 4 at 45 (fee 50.00 USD / 40.00 EUR). An item rises one level per run."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="nr-entity" label="Legal entity" error={errors.legalEntityId}>
            {(c) => <EntityLookup entity="legalEntity" {...c} value={values.legalEntityId} onChange={(e) => update({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="nr-date" label="Run date" error={errors.runDate}>
            {(c) => <Input {...c} type="date" value={values.runDate} onChange={(e) => update({ runDate: e.target.value })} invalid={Boolean(errors.runDate)} />}
          </FormField>
          <FormField id="nr-grace" label="Grace days" hint="Days past due before the first reminder." error={errors.graceDays}>
            {(c) => <Input {...c} inputMode="numeric" className={styles.mono} value={values.graceDays} onChange={(e) => update({ graceDays: e.target.value })} invalid={Boolean(errors.graceDays)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <CommandFailure title="Could not propose the run" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Propose run
            </Button>
            <Link className={buttonClassName("ghost")} to="/receivables/dunning-runs">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

const letterColumns: DataTableColumn<DunningLetter>[] = [
  { key: "customerId", header: "Customer", render: (row) => <PlainName short entity="customer" id={row.customerId} /> },
  { key: "level", header: "Level", mono: true, align: "end", render: (row) => (row.priorLevel > 0 ? `${row.priorLevel} → ${row.level}` : String(row.level)) },
  { key: "items", header: "Items", mono: true, render: (row) => row.items.map((i) => `${i.documentNumber} (${i.daysOverdue} d)`).join(", ") },
  { key: "amount", header: "Amount dunned", mono: true, align: "end", render: (row) => money(row.amount, row.currency) },
  { key: "feeAmount", header: "Fee", mono: true, align: "end", render: (row) => money(row.feeAmount, row.currency) },
  { key: "status", header: "Letter", render: (row) => <Badge tone={row.status === "ISSUED" ? "success" : row.status === "SKIPPED" ? "neutral" : "info"}>{row.status === "ISSUED" ? "Issued" : row.status === "SKIPPED" ? "Skipped" : "Draft"}</Badge> },
];

const exclusionColumns: DataTableColumn<Exclusion>[] = [
  { key: "customerId", header: "Customer", render: (row) => <PlainName short entity="customer" id={row.customerId} /> },
  { key: "openItemId", header: "Open item", mono: true, render: (row) => shortId(row.openItemId) },
  { key: "reason", header: "Why it was left out", render: (row) => EXCLUSION_LABELS[row.reason] ?? row.reason },
];

type Move = "review" | "issue" | "cancel";

const MOVED: Record<Move, string> = {
  review: "The run is reviewed. Issue it to send the letters.",
  issue: "The run is issued: item levels are raised and fees posted as open items.",
  cancel: "The run is cancelled. Nothing was raised or posted.",
};

/** One dunning run: the letters it proposes (preview), what it leaves out and why, and review, issue or cancel. */
export function DunningRunDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getDunningRun(id, signal), id);
  const [busy, setBusy] = useState<Move | null>(null);
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [done, setDone] = useState<string | null>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Dunning run" />
        <LoadFailure title="Could not load the dunning run" problem={state.error} retry={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Dunning run" />
        <span className="visually-hidden">Loading the dunning run…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const run: DunningRun = state.data;

  async function move(action: Move) {
    setBusy(action);
    setFailure(null);
    setDone(null);
    try {
      await moveDunningRun(run.id, run.version, action);
      setDone(MOVED[action]);
      state.reload();
    } catch (error) {
      setFailure({
        title: isStale(error) ? "The run changed since you opened it. Reload it and try again." : "Could not update the run",
        problem: describeProblem(error),
      });
    } finally {
      setBusy(null);
    }
  }

  const open = run.status === "DRAFT" || run.status === "REVIEWED";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Dunning run ${run.runDate}`}
        description="Exclusions are checked again at issue: a letter whose items were paid, disputed or promised since the proposal is skipped."
        meta={runBadge(run.status)}
        actions={
          <Link className={buttonClassName("secondary")} to="/receivables/dunning-runs">
            All runs
          </Link>
        }
      />
      <Card title="Run" headingLevel={3}>
        <dl className={styles.details}>
          <Detail term="Run date" mono>{run.runDate}</Detail>
          <Detail term="Grace days" mono>{run.graceDays}</Detail>
          <Detail term="Letters" mono>{run.letters.filter((l) => l.status !== "SKIPPED").length}</Detail>
          <Detail term="Items left out" mono>{run.exclusions.length}</Detail>
          <Detail term="Issued" mono>{run.issuedAt ? dateTime(run.issuedAt) : "Not yet"}</Detail>
          <Detail term="Legal entity" mono>{run.legalEntityId}</Detail>
        </dl>
      </Card>
      {done ? (
        <Alert tone="success" title="Run updated" live="status">
          {done}
        </Alert>
      ) : null}
      {failure ? <CommandFailure title={failure.title} problem={failure.problem} /> : null}
      {open ? (
        <div className={styles.actions}>
          {run.status === "DRAFT" ? (
            <Button variant="primary" loading={busy === "review"} onClick={() => void move("review")}>
              Mark reviewed
            </Button>
          ) : (
            <Button variant="primary" loading={busy === "issue"} onClick={() => void move("issue")}>
              Issue letters
            </Button>
          )}
          <Button variant="danger" loading={busy === "cancel"} onClick={() => void move("cancel")}>
            Cancel run
          </Button>
        </div>
      ) : null}
      <Card title="Letters" headingLevel={3}>
        <DataTable
          label="Letters"
          columns={letterColumns}
          rows={run.letters}
          getRowKey={(row) => row.id}
          empty={{ title: "No letters", description: "No customer has an item that reached a new level, or every one was left out." }}
        />
      </Card>
      <Card title="Left out" headingLevel={3}>
        <DataTable
          label="Items left out"
          columns={exclusionColumns}
          rows={run.exclusions}
          getRowKey={(row) => `${row.openItemId}|${row.reason}`}
          empty={{ title: "Nothing left out", description: "Every overdue item that reached a new level is on a letter." }}
        />
      </Card>
    </div>
  );
}
