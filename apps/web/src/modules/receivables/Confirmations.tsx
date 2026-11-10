import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import type { LookupEntityKey } from "../../shell/lookup/entities";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  type FormFieldControlProps,
  Skeleton,
  StatGroup,
  StatTile,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import {
  createConfirmationRun,
  describeProblem,
  getConfirmationRun,
  listConfirmationDifferences,
  listConfirmationRuns,
  newKey,
  recordReply,
  type Confirmation,
  type ConfirmationRun,
  type ConfirmationRunInput,
  type Problem,
  type ReplyInput,
} from "./api/client";
import { DAY, dateTime, DECIMAL, money, REPLY_LABELS, REPLY_TONES, shortId, today, UUID } from "./format";
import { CommandFailure, Detail, keepKeyAfter, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { PlainName } from "../sales/OrderNumber";
import { EntityName } from "../../shell/lookup/EntityName";
import { useNames } from "../mfg-shared/names";

/** The customers still to reply, by name: an option label is plain text, so names are resolved here. */
function WaitingCustomers({ control, waiting, value, onChange }: { control: FormFieldControlProps; waiting: Confirmation[]; value: string; onChange(id: string): void }) {
  const names = useNames("customer", waiting.map((w) => w.customerId));
  return (
    <Select {...control} value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">Choose a customer</option>
      {waiting.map((w) => (
        <option key={w.id} value={w.id}>
          {`${names[w.customerId] ?? shortId(w.customerId)} · ${money(w.ourBalance, w.currency)}`}
        </option>
      ))}
    </Select>
  );
}

const SIGNED = /^-?\d+(\.\d{1,6})?$/;

const runColumns: DataTableColumn<ConfirmationRun>[] = [
  { key: "asOfDate", header: "As of", mono: true },
  { key: "threshold", header: "Threshold", mono: true, align: "end", render: (row) => row.threshold },
  { key: "selected", header: "Customers", mono: true, align: "end", render: (row) => row.counts.selected },
  { key: "agreed", header: "Agreed", mono: true, align: "end", render: (row) => row.counts.agreed },
  { key: "disagreed", header: "Disagreed", mono: true, align: "end", render: (row) => row.counts.disagreed },
  { key: "noReply", header: "No reply", mono: true, align: "end", render: (row) => row.counts.noReply },
  { key: "createdAt", header: "Created", mono: true, render: (row) => dateTime(row.createdAt) },
];

/** Balance confirmation runs (FR-30.20), newest first, with their reply counts. */
export function ConfirmationRunsPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listConfirmationRuns(signal), "runs");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Balance confirmations"
        description="Customers confirm the balance we hold for them as of a key date. A disagreement opens a dispute case."
        actions={
          <Link className={buttonClassName("primary")} to="/receivables/balance-confirmations/new">
            New run
          </Link>
        }
      />
      {state.error ? (
        <LoadFailure title="Could not load the confirmation runs" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Balance confirmation runs"
          columns={runColumns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No confirmation runs", description: "Start a run for the key date your auditor asked for." }}
          onRowOpen={(row) => navigate(`/receivables/balance-confirmations/${row.id}`)}
        />
      )}
    </div>
  );
}

type Errors = Record<string, string>;

export interface RunDraft {
  legalEntityId: string;
  asOfDate: string;
  threshold: string;
  sampleSize: string;
  includeZeroBalances: boolean;
  reminderDays: string;
}

/** Client checks that mirror the service validation; the service has the last word. */
export function validateConfirmationRun(v: RunDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Choose a legal entity.";
  if (!DAY.test(v.asOfDate)) errors.asOfDate = "Enter the key date.";
  if (!DECIMAL.test(v.threshold.trim())) errors.threshold = "Enter a threshold such as 10000.00.";
  const sample = v.sampleSize.trim();
  if (sample !== "" && (!/^\d+$/.test(sample) || Number(sample) < 1 || Number(sample) > 500)) {
    errors.sampleSize = "Enter a sample of 1 to 500 customers, or leave it empty for everyone above the threshold.";
  }
  const days = v.reminderDays.trim();
  if (!/^\d+$/.test(days) || Number(days) < 1 || Number(days) > 60) errors.reminderDays = "Enter 1 to 60 days.";
  return errors;
}

export function toRunInput(v: RunDraft): ConfirmationRunInput {
  const sample = v.sampleSize.trim();
  return {
    legalEntityId: v.legalEntityId.trim(),
    asOfDate: v.asOfDate,
    threshold: v.threshold.trim(),
    sampleSize: sample === "" ? null : Number(sample),
    includeZeroBalances: v.includeZeroBalances,
    reminderDays: Number(v.reminderDays.trim()),
  };
}

/** Start a confirmation run: everyone at or above the threshold, or a reproducible sample of them. */
export function NewConfirmationRunPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<RunDraft>({
    legalEntityId: "",
    asOfDate: today(),
    threshold: "10000.00",
    sampleSize: "",
    includeZeroBalances: false,
    reminderDays: "14",
  });
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
    const found = validateConfirmationRun(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createConfirmationRun(toRunInput(values), key.current);
      navigate(`/receivables/balance-confirmations/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (!keepKeyAfter(error)) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  function text(id: string, field: Exclude<keyof RunDraft, "includeZeroBalances">, label: string, opts: { entity?: LookupEntityKey; hint?: string; type?: string; optional?: boolean } = {}) {
    return (
      <FormField id={id} label={label} hint={opts.hint} error={errors[field]} optional={opts.optional}>
        {(c) =>
        opts.entity ? (
          <EntityLookup
            {...c}
            entity={opts.entity}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<RunDraft>)}
            invalid={Boolean(errors[field])}
          />
        ) : (
          <Input
            {...c}
            type={opts.type}
            className={styles.mono}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<RunDraft>)}
            invalid={Boolean(errors[field])}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New balance confirmation run"
        description="The balance is each customer's open items as of the key date, per currency; later payments are added back."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("bc-entity", "legalEntityId", "Legal entity", { entity: "legalEntity" })}
          {text("bc-date", "asOfDate", "Key date", { type: "date" })}
          {text("bc-threshold", "threshold", "Threshold", { hint: "Customers whose balance reaches this amount, debit or credit." })}
          {text("bc-sample", "sampleSize", "Sample size", { optional: true, hint: "A random but reproducible sample of the qualifying customers." })}
          <FormField id="bc-zero" label="Zero balances" hint="Customers whose items were all paid by the key date.">
            {(c) => (
              <Select {...c} value={values.includeZeroBalances ? "yes" : "no"} onChange={(e) => update({ includeZeroBalances: e.target.value === "yes" })}>
                <option value="no">Leave out</option>
                <option value="yes">Include</option>
              </Select>
            )}
          </FormField>
          {text("bc-reminder", "reminderDays", "Reminder after (days)")}
          {failure ? (
            <div className={styles.formActions}>
              <CommandFailure title="Could not start the run" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Start run
            </Button>
            <Link className={buttonClassName("ghost")} to="/receivables/balance-confirmations">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

function caseLink(row: Confirmation) {
  return row.caseId ? <Link to={`/receivables/cases/${row.caseId}`}>Open case</Link> : "None";
}

const confirmationColumns: DataTableColumn<Confirmation>[] = [
  { key: "customerId", header: "Customer", render: (row) => <PlainName short entity="customer" id={row.customerId} /> },
  { key: "ourBalance", header: "Our balance", mono: true, align: "end", render: (row) => money(row.ourBalance, row.currency) },
  { key: "status", header: "Reply", render: (row) => <Badge tone={REPLY_TONES[row.status]}>{REPLY_LABELS[row.status]}</Badge> },
  {
    key: "customerBalance",
    header: "Their balance",
    mono: true,
    align: "end",
    render: (row) => (row.customerBalance ? money(row.customerBalance, row.currency) : "None"),
  },
  { key: "repliedAt", header: "Replied", mono: true, render: (row) => dateTime(row.repliedAt) },
  { key: "caseId", header: "Case", render: caseLink },
];

const differenceColumns: DataTableColumn<Confirmation>[] = [
  { key: "customerId", header: "Customer", render: (row) => <PlainName short entity="customer" id={row.customerId} /> },
  { key: "ourBalance", header: "Our balance", mono: true, align: "end", render: (row) => money(row.ourBalance, row.currency) },
  {
    key: "customerBalance",
    header: "Their balance",
    mono: true,
    align: "end",
    render: (row) => (row.customerBalance ? money(row.customerBalance, row.currency) : "None"),
  },
  { key: "difference", header: "Difference", mono: true, align: "end", render: (row) => (row.difference ? money(row.difference, row.currency) : "None") },
  { key: "comment", header: "Comment", render: (row) => row.comment ?? "None" },
  { key: "caseId", header: "Case", render: caseLink },
];

export interface ReplyDraft {
  confirmationId: string;
  reply: "AGREED" | "DISAGREED";
  customerBalance: string;
  comment: string;
}

export function validateReply(v: ReplyDraft): Errors {
  const errors: Errors = {};
  if (v.confirmationId === "") errors.confirmationId = "Choose the customer who replied.";
  if (v.reply === "DISAGREED" && !SIGNED.test(v.customerBalance.trim())) {
    errors.customerBalance = "Enter the balance the customer states, such as 11500.00.";
  }
  if (v.comment.length > 500) errors.comment = "Keep the comment to 500 characters.";
  return errors;
}

export function toReplyInput(v: ReplyDraft): ReplyInput {
  const comment = v.comment.trim();
  return {
    confirmationId: v.confirmationId,
    reply: v.reply,
    customerBalance: v.reply === "DISAGREED" ? v.customerBalance.trim() : null,
    comment: comment === "" ? null : comment,
  };
}

/** One run: who was asked, who replied, the difference list for audit and the reply form. */
export function ConfirmationRunDetailPage() {
  const { id = "" } = useParams();
  const [refresh, setRefresh] = useState(0);
  const state = useLoad((signal) => getConfirmationRun(id, signal), `${id}|${refresh}`);
  const diffs = useLoad((signal) => listConfirmationDifferences(id, signal), `${id}|${refresh}`);
  const [draft, setDraft] = useState<ReplyDraft>({ confirmationId: "", reply: "AGREED", customerBalance: "", comment: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Balance confirmation run" />
        <LoadFailure title="Could not load the run" problem={state.error} retry={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Balance confirmation run" />
        <span className="visually-hidden">Loading the run…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const run = state.data;
  const rows = run.confirmations ?? [];
  const waiting = rows.filter((c) => c.status === "NO_REPLY");

  function update(patch: Partial<ReplyDraft>) {
    setDraft((prev) => ({ ...prev, ...patch }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateReply(draft);
    setErrors(found);
    setFailure(null);
    setDone(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const saved = await recordReply(run.id, toReplyInput(draft));
      setDone(saved.caseId ? "Reply recorded. A dispute case was opened for the difference." : "Reply recorded.");
      setDraft({ confirmationId: "", reply: "AGREED", customerBalance: "", comment: "" });
      setRefresh((n) => n + 1);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Balance confirmation as of ${run.asOfDate}`}
        description="Replies are final once recorded. A reminder is due after the reminder days for every customer who has not replied."
        actions={
          <Link className={buttonClassName("secondary")} to="/receivables/balance-confirmations">
            All runs
          </Link>
        }
      />
      <StatGroup label="Replies">
        <StatTile label="Customers asked" value={run.counts.selected} />
        <StatTile label="Agreed" value={run.counts.agreed} />
        <StatTile label="Disagreed" value={run.counts.disagreed} tone="danger" />
        <StatTile label="No reply" value={run.counts.noReply} tone="warning" />
      </StatGroup>
      <Card title="Run" headingLevel={3}>
        <dl className={styles.details}>
          <Detail term="Threshold" mono>{run.threshold}</Detail>
          <Detail term="Sample" mono>{run.sampleSize ?? "Everyone at or above the threshold"}</Detail>
          <Detail term="Zero balances">{run.includeZeroBalances ? "Included" : "Left out"}</Detail>
          <Detail term="Reminder after" mono>{`${run.reminderDays} days`}</Detail>
          <Detail term="Legal entity"><EntityName entity="legalEntity" id={run.legalEntityId} /></Detail>
        </dl>
      </Card>
      {done ? (
        <Alert tone="success" title="Reply recorded" live="status">
          {done}
        </Alert>
      ) : null}
      {failure ? <CommandFailure title="Could not record the reply" problem={failure} /> : null}
      {waiting.length > 0 ? (
        <Card title="Record a reply" headingLevel={3}>
          <form className={styles.form} onSubmit={submit} noValidate>
            <FormField id="rp-customer" label="Customer" error={errors.confirmationId}>
              {(c) => <WaitingCustomers control={c} waiting={waiting} value={draft.confirmationId} onChange={(confirmationId) => update({ confirmationId })} />}
            </FormField>
            <FormField id="rp-reply" label="Reply">
              {(c) => (
                <Select {...c} value={draft.reply} onChange={(e) => update({ reply: e.target.value as ReplyDraft["reply"] })}>
                  <option value="AGREED">Agreed</option>
                  <option value="DISAGREED">Disagreed</option>
                </Select>
              )}
            </FormField>
            {draft.reply === "DISAGREED" ? (
              <FormField id="rp-balance" label="Customer's balance" error={errors.customerBalance}>
                {(c) => (
                  <Input
                    {...c}
                    className={styles.mono}
                    inputMode="decimal"
                    value={draft.customerBalance}
                    onChange={(e) => update({ customerBalance: e.target.value })}
                    invalid={Boolean(errors.customerBalance)}
                  />
                )}
              </FormField>
            ) : null}
            <div className={styles.wide}>
              <FormField id="rp-comment" label="Comment" optional error={errors.comment}>
                {(c) => <Textarea {...c} value={draft.comment} maxLength={500} onChange={(e) => update({ comment: e.target.value })} />}
              </FormField>
            </div>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Record reply
              </Button>
            </div>
          </form>
        </Card>
      ) : (
        <p className={styles.note}>Every customer in this run has replied.</p>
      )}
      <Card title="Difference list" headingLevel={3}>
        {diffs.error ? (
          <LoadFailure title="Could not load the difference list" problem={diffs.error} retry={diffs.reload} />
        ) : (
          <DataTable
            label="Difference list"
            columns={differenceColumns}
            rows={diffs.data ?? []}
            getRowKey={(row) => row.id}
            loading={diffs.loading}
            empty={{ title: "No differences", description: "No customer has disagreed with the balance." }}
          />
        )}
      </Card>
      <Card title="Customers" headingLevel={3}>
        <DataTable
          label="Confirmations"
          columns={confirmationColumns}
          rows={rows}
          getRowKey={(row) => row.id}
          empty={{ title: "No customers selected", description: "No balance reached the threshold on the key date." }}
        />
      </Card>
    </div>
  );
}
