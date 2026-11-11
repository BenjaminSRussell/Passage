import { useState } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listGLJournals, retryGLJournal, sweepGLJournals, type GLJournal, type JournalStatus, type JournalSweepResult, type Problem } from "./api/client";
import { dateTime, DAY, money } from "./format";
import { CommandFailure, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { EntityName } from "../../shell/lookup/EntityName";

const PAGE = 200;

export const SOURCE_LABELS: Record<string, string> = {
  payment_receipt: "Cash received",
  payment_apply: "Payment applied",
  dunning_fee: "Dunning fee",
  interest: "Interest",
  down_payment: "Down payment or deposit",
  deduction: "Deduction written off",
  opening_items: "Opening balance",
};

type Standing = "Posted" | "Waiting" | "Parked";

/** What a person needs to know: posted, waiting for the next pass, or parked because the ledger refused it. */
export function standing(journal: GLJournal): Standing {
  if (journal.status === "POSTED") return "Posted";
  return journal.parked ? "Parked" : "Waiting";
}

const TONES: Record<Standing, BadgeTone> = { Posted: "success", Waiting: "info", Parked: "danger" };

const columns: DataTableColumn<GLJournal>[] = [
  { key: "postingDate", header: "Posting date", mono: true },
  { key: "sourceType", header: "What happened", render: (row) => SOURCE_LABELS[row.sourceType] ?? row.sourceType },
  { key: "legalEntityId", header: "Legal entity", render: (row) => <EntityName entity="legalEntity" id={row.legalEntityId} /> },
  { key: "debit", header: "Amount", mono: true, align: "end", render: (row) => money(row.debit, row.currency) },
  { key: "status", header: "Status", render: (row) => <Badge tone={TONES[standing(row)]}>{standing(row)}</Badge> },
  { key: "attempts", header: "Tries", mono: true, align: "end" },
];

/**
 * Ledger journals (30-2): every cash receipt, application, fee, interest charge and write-off receivables owes the general ledger,
 * stored with the change that caused it and posted by a worker. A journal the ledger refused is parked until someone retries it,
 * if need be under a new posting date.
 */
export function GLJournalsPage() {
  const [status, setStatus] = useState<JournalStatus | "">("");
  const [parkedOnly, setParkedOnly] = useState(false);
  const [selected, setSelected] = useState<GLJournal | null>(null);
  const state = useLoad((signal) => listGLJournals({ status, parked: parkedOnly, limit: PAGE }, signal), `${status}|${parkedOnly}`);
  const [sweeping, setSweeping] = useState(false);
  const [sweep, setSweep] = useState<JournalSweepResult | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);

  async function postDue() {
    setSweeping(true);
    setFailure(null);
    setSweep(null);
    try {
      setSweep(await sweepGLJournals());
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setSweeping(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Ledger journals"
        description="What receivables owes the general ledger, newest first. A journal is posted automatically; one the ledger refused is parked here until you retry it."
        actions={
          <Button variant="primary" loading={sweeping} onClick={() => void postDue()}>
            Post due journals
          </Button>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="gl-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as JournalStatus | "")}>
              <option value="">Posted and waiting</option>
              <option value="PENDING">Waiting or parked</option>
              <option value="POSTED">Posted</option>
            </Select>
          )}
        </FormField>
        <span className={styles.check}>
          <input id="gl-parked" type="checkbox" checked={parkedOnly} onChange={(e) => setParkedOnly(e.target.checked)} />
          <label htmlFor="gl-parked">Parked journals only</label>
        </span>
      </div>
      {sweep ? (
        <Alert tone={sweep.failed + sweep.parked > 0 ? "warning" : "success"} title="Journals posted" live="status">
          {sweep.posted} posted, {sweep.failed} to try again later, {sweep.parked} parked because the ledger refused them.
        </Alert>
      ) : null}
      {failure ? <CommandFailure title="Could not post the journals" problem={failure} /> : null}
      {state.error ? (
        <LoadFailure title="Could not load the ledger journals" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Ledger journals"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No journals", description: "Journals appear here when a payment is received or applied, a fee or interest is charged or an amount is written off." }}
          onRowOpen={(row) => setSelected(row)}
        />
      )}
      {state.data && state.data.length >= PAGE ? <p className={styles.note}>Showing the newest {PAGE} journals.</p> : null}
      {selected ? <JournalDetail key={selected.id} journal={selected} onChanged={() => { setSelected(null); state.reload(); }} /> : null}
    </div>
  );
}

const lineColumns: DataTableColumn<GLJournal["lines"][number]>[] = [
  { key: "account", header: "Account", mono: true },
  { key: "side", header: "Side", render: (row) => (row.side === "D" ? "Debit" : "Credit") },
  { key: "amount", header: "Amount", mono: true, align: "end" },
  { key: "text", header: "Text", render: (row) => row.text ?? "" },
];

function JournalDetail({ journal, onChanged }: { journal: GLJournal; onChanged(): void }) {
  const [date, setDate] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [dateError, setDateError] = useState("");

  async function retry() {
    if (date !== "" && !DAY.test(date)) {
      setDateError("Enter a date, or leave it empty to keep the original posting date.");
      return;
    }
    setDateError("");
    setBusy(true);
    setFailure(null);
    try {
      await retryGLJournal(journal.id, date === "" ? undefined : date);
      onChanged();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title={`${SOURCE_LABELS[journal.sourceType] ?? journal.sourceType}, ${journal.postingDate}`} headingLevel={2}>
      <div className={styles.stack}>
        <p className={styles.note}>
          {standing(journal) === "Posted" ? `Posted to the ledger. ${journal.attempts} ${journal.attempts === 1 ? "try" : "tries"}.` : null}
          {standing(journal) === "Waiting" ? `Waiting for the next posting pass. ${journal.attempts} ${journal.attempts === 1 ? "try" : "tries"} so far${journal.nextAttemptAt ? `, next after ${dateTime(journal.nextAttemptAt)}` : ""}.` : null}
          {standing(journal) === "Parked" ? "The ledger refused this journal, so it is parked." : null}
        </p>
        {journal.lastError ? (
          <Alert tone="warning" title="Last problem">
            {journal.lastError}
          </Alert>
        ) : null}
        <DataTable label="Journal lines" columns={lineColumns} rows={journal.lines} getRowKey={(_, i) => String(i)} empty={{ title: "No lines", description: "" }} />
        {standing(journal) === "Parked" ? (
          <form
            className={styles.form}
            onSubmit={(e) => {
              e.preventDefault();
              void retry();
            }}
            noValidate
          >
            <FormField id="gl-retry-date" label="New posting date" optional hint="Use it when the original period is closed." error={dateError}>
              {(c) => <Input {...c} type="date" value={date} onChange={(e) => setDate(e.target.value)} invalid={dateError !== ""} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Retry posting
              </Button>
            </div>
          </form>
        ) : null}
        {failure ? <CommandFailure title="Could not retry the journal" problem={failure} /> : null}
      </div>
    </Card>
  );
}
