import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  fetchJournalBatch,
  fetchRecurringJournal,
  fetchRecurringRuns,
  generateRecurringJournal,
  JOURNAL_NAME_CODE,
  LedgerApiError,
  putRecurringJournal,
  recurringStatusAction,
  RECURRING_FREQUENCIES,
  type JournalBatch,
  type RecurringJournal,
  type RecurringJournalInput,
  type RecurringRun,
} from "./api/client";
import { parseDimensions } from "./BatchLinesEditor";
import { dayLabel, RecurringStatusBadge } from "./RecurringJournalsPage";
import { UUID } from "./pages";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

const ACCOUNT = /^[A-Za-z0-9._-]{1,20}$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const DECIMAL = /^\d{1,14}(\.\d{1,6})?$/;
const QUANTITY = /^\d{1,12}(\.\d{1,6})?$/;
const UNIT = /^[A-Z0-9]{1,10}$/;

export interface RecurringLineDraft {
  key: string;
  account: string;
  dimensions: string;
  side: "D" | "C";
  kind: "fixed" | "percent";
  amount: string;
  text: string;
  /** Statistical quantity (BR-11.2) and unit; both empty for an ordinary line. */
  quantity: string;
  unit: string;
}

export interface RecurringDraft {
  ledgerId: string;
  journalName: string;
  name: string;
  description: string;
  frequency: (typeof RECURRING_FREQUENCIES)[number];
  dayOfMonth: string;
  startDate: string;
  endDate: string;
  baseAmount: string;
  currency: string;
  lines: RecurringLineDraft[];
}

let nextKey = 0;
export function emptyRecurringLine(side: "D" | "C" = "D"): RecurringLineDraft {
  nextKey += 1;
  return { key: `rl-${nextKey}`, account: "", dimensions: "", side, kind: "fixed", amount: "", text: "", quantity: "", unit: "" };
}

export function emptyRecurring(): RecurringDraft {
  return {
    ledgerId: "",
    journalName: "",
    name: "",
    description: "",
    frequency: "monthly",
    dayOfMonth: "0",
    startDate: "",
    endDate: "",
    baseAmount: "",
    currency: "USD",
    lines: [emptyRecurringLine("D"), emptyRecurringLine("C")],
  };
}

type Errors = Record<string, string>;

/** Client checks that mirror PUT /recurring-journals/{id}; the service has the last word. */
export function validateRecurring(v: RecurringDraft): Errors {
  const e: Errors = {};
  if (!UUID.test(v.ledgerId.trim())) e.ledgerId = "Choose the ledger.";
  if (!JOURNAL_NAME_CODE.test(v.journalName)) e.journalName = "Use 1 to 20 letters, digits, dashes or underscores.";
  if (v.name.trim() === "" || v.name.length > 80) e.name = "Enter a name of up to 80 characters.";
  if (v.description.length > 255) e.description = "Use at most 255 characters.";
  const day = Number(v.dayOfMonth);
  if (!/^\d{1,2}$/.test(v.dayOfMonth) || day > 31) e.dayOfMonth = "Enter 1 to 31, or 0 for the last day of the month.";
  if (!DATE.test(v.startDate)) e.startDate = "Enter the start date.";
  if (v.endDate && (!DATE.test(v.endDate) || v.endDate < v.startDate)) e.endDate = "Enter an end date on or after the start date.";
  if (v.baseAmount && !DECIMAL.test(v.baseAmount)) e.baseAmount = "Enter a non-negative amount such as 1000.00.";
  if (!/^[A-Z]{3}$/.test(v.currency)) e.currency = "Enter a 3-letter currency code.";
  if (v.lines.length === 0) e.lines = "Add at least one line.";
  v.lines.forEach((l, i) => {
    const n = i + 1;
    if (!ACCOUNT.test(l.account.trim())) e[`lines.${i}.account`] = `Line ${n}: enter an account of up to 20 letters, digits, dots, dashes or underscores.`;
    if (!DECIMAL.test(l.amount.trim())) e[`lines.${i}.amount`] = `Line ${n}: enter a non-negative amount.`;
    else if (l.kind === "percent" && Number(l.amount) > 1000) e[`lines.${i}.amount`] = `Line ${n}: a percentage is at most 1000.`;
    if (parseDimensions(l.dimensions) === null) e[`lines.${i}.dimensions`] = `Line ${n}: write dimensions as DEPT=D100, CC=C200.`;
    const quantity = l.quantity.trim();
    const unit = l.unit.trim();
    const hasQuantity = quantity !== "" && !/^0+(\.0+)?$/.test(quantity);
    if (quantity !== "" && !QUANTITY.test(quantity)) e[`lines.${i}.quantity`] = `Line ${n}: enter a quantity such as 12.5 with at most 6 decimals.`;
    else if (hasQuantity && unit === "") e[`lines.${i}.unit`] = `Line ${n}: a quantity needs a unit.`;
    else if (!hasQuantity && unit !== "") e[`lines.${i}.unit`] = `Line ${n}: a unit needs a quantity above zero.`;
    else if (unit !== "" && !UNIT.test(unit)) e[`lines.${i}.unit`] = `Line ${n}: a unit is 1 to 10 capital letters or digits.`;
  });
  return e;
}

export function toRecurringInput(v: RecurringDraft): RecurringJournalInput {
  const input: RecurringJournalInput = {
    ledgerId: v.ledgerId.trim(),
    journalName: v.journalName,
    name: v.name.trim(),
    description: v.description.trim(),
    frequency: v.frequency,
    dayOfMonth: Number(v.dayOfMonth),
    startDate: v.startDate,
    lines: v.lines.map((l) => {
      const dimensions = parseDimensions(l.dimensions) ?? {};
      return {
        account: l.account.trim(),
        ...(Object.keys(dimensions).length > 0 ? { dimensions } : {}),
        debitCredit: l.side,
        amountKind: l.kind,
        amount: { currency: v.currency, value: l.amount.trim() },
        ...(l.text.trim() ? { text: l.text.trim() } : {}),
        ...(l.quantity.trim() !== "" && l.unit.trim() !== "" ? { quantity: l.quantity.trim(), unit: l.unit.trim() } : {}),
      };
    }),
  };
  if (v.endDate) input.endDate = v.endDate;
  if (v.baseAmount) input.baseAmount = v.baseAmount;
  return input;
}

function toDraft(r: RecurringJournal): RecurringDraft {
  return {
    ledgerId: r.ledgerId,
    journalName: r.journalName,
    name: r.name,
    description: r.description,
    frequency: r.frequency,
    dayOfMonth: String(r.dayOfMonth),
    startDate: r.startDate,
    endDate: r.endDate ?? "",
    baseAmount: r.baseAmount ?? "",
    currency: r.lines?.[0]?.currency ?? "USD",
    lines: (r.lines ?? []).map((l) => ({
      ...emptyRecurringLine(l.debitCredit),
      account: l.account,
      dimensions: Object.entries(l.dimensions ?? {})
        .map(([a, v]) => `${a}=${v}`)
        .join(", "),
      kind: l.amountKind,
      amount: l.amount,
      text: l.text ?? "",
      quantity: l.quantity ?? "",
      unit: l.unit ?? "",
    })),
  };
}

/** A recurring journal draft filled from an existing (draft or posted) batch: its lines as fixed amounts. */
export function draftFromBatch(b: JournalBatch): RecurringDraft {
  return {
    ...emptyRecurring(),
    ledgerId: b.ledgerId,
    journalName: b.journalName,
    name: (b.description || `${b.journalName} recurring journal`).slice(0, 80),
    description: b.description,
    startDate: b.postingDate,
    currency: b.currency,
    lines: (b.lines ?? []).map((l) => ({
      ...emptyRecurringLine(l.debitCredit),
      account: l.account,
      dimensions: Object.entries(l.dimensions ?? {})
        .map(([a, v]) => `${a}=${v}`)
        .join(", "),
      amount: l.amount,
      text: l.text ?? "",
      quantity: l.quantity ?? "",
      unit: l.unit ?? "",
    })),
  };
}

function failureText(error: unknown): ReactNode {
  const fields = error instanceof LedgerApiError ? Object.entries(error.fields).map(([f, m]) => `${f}: ${m}`) : [];
  return (
    <>
      <ProblemMessage problem={error} />
      {fields.length > 0 ? <div>{fields.join(" ")}</div> : null}
    </>
  );
}

function RecurringForm({ initial, existing }: { initial: RecurringDraft; existing?: RecurringJournal }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [values, setValues] = useState<RecurringDraft>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<ReactNode>(null);
  const [busy, setBusy] = useState(false);
  const update = (patch: Partial<RecurringDraft>) => setValues((prev) => ({ ...prev, ...patch }));
  const updateLine = (key: string, patch: Partial<RecurringLineDraft>) => update({ lines: values.lines.map((l) => (l.key === key ? { ...l, ...patch } : l)) });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateRecurring(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const id = existing?.id ?? crypto.randomUUID();
      const saved = await putRecurringJournal(id, toRecurringInput(values), existing?.version);
      await queryClient.invalidateQueries({ queryKey: ["ledger", "recurring-journals"] });
      navigate(`/ledger/recurring-journals/${saved.id}`);
    } catch (error) {
      setFailure(failureText(error));
      if (error instanceof LedgerApiError && error.status === 412) await queryClient.invalidateQueries({ queryKey: ["ledger", "recurring-journals", existing?.id] });
    } finally {
      setBusy(false);
    }
  }

  const lineColumns: DataTableColumn<RecurringLineDraft>[] = [
    { key: "n", header: "Line", align: "end", render: (l) => values.lines.indexOf(l) + 1 },
    {
      key: "account",
      header: "Account",
      render: (l) => <Input aria-label={`Line ${values.lines.indexOf(l) + 1} account`} className={styles.mono} value={l.account} maxLength={20} onChange={(e) => updateLine(l.key, { account: e.target.value })} />,
    },
    {
      key: "dimensions",
      header: "Dimensions",
      render: (l) => <Input aria-label={`Line ${values.lines.indexOf(l) + 1} dimensions`} className={styles.mono} placeholder="DEPT=D100" value={l.dimensions} onChange={(e) => updateLine(l.key, { dimensions: e.target.value.toUpperCase() })} />,
    },
    {
      key: "side",
      header: "D/C",
      render: (l) => (
        <Select aria-label={`Line ${values.lines.indexOf(l) + 1} debit or credit`} value={l.side} onChange={(e) => updateLine(l.key, { side: e.target.value === "C" ? "C" : "D" })}>
          <option value="D">Debit</option>
          <option value="C">Credit</option>
        </Select>
      ),
    },
    {
      key: "kind",
      header: "Amount is",
      render: (l) => (
        <Select aria-label={`Line ${values.lines.indexOf(l) + 1} amount kind`} value={l.kind} onChange={(e) => updateLine(l.key, { kind: e.target.value === "percent" ? "percent" : "fixed" })}>
          <option value="fixed">Fixed amount</option>
          <option value="percent">Percent of base</option>
        </Select>
      ),
    },
    {
      key: "amount",
      header: "Amount or %",
      render: (l) => <Input aria-label={`Line ${values.lines.indexOf(l) + 1} amount`} className={styles.amount} inputMode="decimal" value={l.amount} onChange={(e) => updateLine(l.key, { amount: e.target.value })} />,
    },
    {
      key: "quantity",
      header: "Quantity",
      render: (l) => (
        <Input aria-label={`Line ${values.lines.indexOf(l) + 1} quantity`} className={styles.amount} inputMode="decimal" value={l.quantity} onChange={(e) => updateLine(l.key, { quantity: e.target.value })} />
      ),
    },
    {
      key: "unit",
      header: "Unit",
      render: (l) => (
        <Input aria-label={`Line ${values.lines.indexOf(l) + 1} unit`} className={styles.mono} maxLength={10} value={l.unit} onChange={(e) => updateLine(l.key, { unit: e.target.value.toUpperCase() })} />
      ),
    },
    {
      key: "text",
      header: "Text",
      render: (l) => <Input aria-label={`Line ${values.lines.indexOf(l) + 1} text`} value={l.text} maxLength={255} onChange={(e) => updateLine(l.key, { text: e.target.value })} />,
    },
    {
      key: "remove",
      header: "Remove",
      render: (l) => (
        <Button size="sm" variant="ghost" disabled={values.lines.length === 1} onClick={() => update({ lines: values.lines.filter((x) => x.key !== l.key) })}>
          Remove line {values.lines.indexOf(l) + 1}
        </Button>
      ),
    },
  ];

  const lineErrors = Object.entries(errors).filter(([k]) => k.startsWith("lines"));

  return (
    <Card>
      <form className={styles.form} onSubmit={submit} noValidate aria-label={existing ? `Edit recurring journal ${existing.name}` : "New recurring journal"}>
        <LookupFormField id="rj-ledger" label="Ledger" entity="ledger" hint={existing ? "The ledger can't be changed." : undefined} value={values.ledgerId} readOnly={Boolean(existing)} error={errors.ledgerId} onChange={(ledgerId) => update({ ledgerId })} />
        <FormField id="rj-journal" label="Journal name" hint="The batches it generates are entered under this name." error={errors.journalName}>
          {(c) => <Input {...c} className={styles.mono} value={values.journalName} maxLength={20} onChange={(e) => update({ journalName: e.target.value })} invalid={Boolean(errors.journalName)} />}
        </FormField>
        <FormField id="rj-name" label="Name" error={errors.name}>
          {(c) => <Input {...c} value={values.name} maxLength={80} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
        </FormField>
        <FormField id="rj-description" label="Batch description" error={errors.description}>
          {(c) => <Input {...c} value={values.description} maxLength={255} onChange={(e) => update({ description: e.target.value })} invalid={Boolean(errors.description)} />}
        </FormField>
        <FormField id="rj-frequency" label="Frequency">
          {(c) => (
            <Select {...c} value={values.frequency} onChange={(e) => update({ frequency: e.target.value as RecurringDraft["frequency"] })}>
              {RECURRING_FREQUENCIES.map((f) => (
                <option key={f} value={f}>
                  {f[0].toUpperCase() + f.slice(1)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="rj-day" label="Day of month" hint="0 is the last day of the month." error={errors.dayOfMonth}>
          {(c) => <Input {...c} inputMode="numeric" value={values.dayOfMonth} onChange={(e) => update({ dayOfMonth: e.target.value })} invalid={Boolean(errors.dayOfMonth)} />}
        </FormField>
        <FormField id="rj-start" label="Start date" error={errors.startDate}>
          {(c) => <Input {...c} type="date" value={values.startDate} onChange={(e) => update({ startDate: e.target.value })} invalid={Boolean(errors.startDate)} />}
        </FormField>
        <FormField id="rj-end" label="End date" hint="Optional. No run is scheduled after it." error={errors.endDate}>
          {(c) => <Input {...c} type="date" value={values.endDate} onChange={(e) => update({ endDate: e.target.value })} invalid={Boolean(errors.endDate)} />}
        </FormField>
        <FormField id="rj-currency" label="Currency" error={errors.currency}>
          {(c) => <Input {...c} className={styles.mono} value={values.currency} maxLength={3} onChange={(e) => update({ currency: e.target.value.toUpperCase() })} invalid={Boolean(errors.currency)} />}
        </FormField>
        <FormField id="rj-base" label="Base amount" hint="What percent lines apply to. Each run can override it." error={errors.baseAmount}>
          {(c) => <Input {...c} inputMode="decimal" value={values.baseAmount} onChange={(e) => update({ baseAmount: e.target.value })} invalid={Boolean(errors.baseAmount)} />}
        </FormField>
        <div className={styles.formActions}>
          <DataTable label="Template lines" columns={lineColumns} rows={values.lines} getRowKey={(l) => l.key} empty={{ title: "No lines", description: "Add a line to start the template." }} />
        </div>
        <div className={styles.formActions}>
          <Button size="sm" onClick={() => update({ lines: [...values.lines, emptyRecurringLine("D")] })}>
            Add line
          </Button>
        </div>
        {lineErrors.length > 0 ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Fix the template lines" live="alert">
              {lineErrors.map(([k, m]) => (
                <div key={k}>{m}</div>
              ))}
            </Alert>
          </div>
        ) : null}
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not save the recurring journal" live="alert">
              {failure}
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save recurring journal
          </Button>
          <Link className={buttonClassName("ghost")} to="/ledger/recurring-journals">
            Cancel
          </Link>
        </div>
      </form>
    </Card>
  );
}

/** Create a recurring journal (FR-11.10), empty or, with ?fromBatch=<id>, filled from that batch. */
export function NewRecurringJournalPage() {
  const [params] = useSearchParams();
  const fromBatch = params.get("fromBatch") ?? "";
  const source = useQuery({ queryKey: ["ledger", "journal-batch", fromBatch], queryFn: () => fetchJournalBatch(fromBatch), enabled: UUID.test(fromBatch) });
  let body: ReactNode;
  if (fromBatch === "") body = <RecurringForm initial={emptyRecurring()} />;
  else if (!UUID.test(fromBatch)) body = <Alert tone="danger" title="The batch id in the link is not valid" live="alert" />;
  else if (source.isError)
    body = (
      <Alert tone="danger" title="Could not load the batch to copy" live="alert" action={<Button size="sm" onClick={() => void source.refetch()}>Try again</Button>}>
        {(source.error as Error).message}
      </Alert>
    );
  else if (source.isPending)
    body = (
      <Card>
        <div aria-busy="true">
          <span className="visually-hidden">Loading the batch…</span>
          <Skeleton height="control" />
        </div>
      </Card>
    );
  else body = <RecurringForm initial={draftFromBatch(source.data)} />;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="New recurring journal"
        description={
          fromBatch
            ? "Filled from the batch: its lines become fixed amounts. Set the schedule, then save."
            : "Describe the journal lines and when they repeat. Percent lines apply to the base amount."
        }
      />
      {body}
    </div>
  );
}

const runColumns: DataTableColumn<RecurringRun>[] = [
  { key: "scheduledDate", header: "Scheduled date" },
  { key: "batchId", header: "Draft batch", render: (r) => <Link to={`/ledger/journal-batches/${r.batchId}`}>Open batch</Link> },
  { key: "generatedBy", header: "Generated by" },
  { key: "generatedAt", header: "Generated at" },
];

function Actions({ journal, onChanged }: { journal: RecurringJournal; onChanged(): Promise<void> }) {
  // null follows the template's next run date, which moves after each run.
  const [chosenDate, setDate] = useState<string | null>(null);
  const date = chosenDate ?? journal.nextRunDate ?? "";
  const [dateError, setDateError] = useState<string | null>(null);
  const [base, setBase] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ tone: "success" | "danger"; text: ReactNode } | null>(null);

  async function run(work: () => Promise<string>) {
    setBusy(true);
    setMessage(null);
    try {
      setMessage({ tone: "success", text: await work() });
      await onChanged();
    } catch (error) {
      setMessage({ tone: "danger", text: failureText(error) });
    } finally {
      setBusy(false);
    }
  }

  const paused = journal.status === "paused";
  return (
    <Card>
      <div className={styles.form}>
        <FormField
          id="rj-gen-date"
          label="Scheduled date"
          hint={journal.nextRunDate ? `The next run date, ${journal.nextRunDate}. Enter an earlier date of the schedule to catch up a missed run.` : "This template has no run date left; enter a date of the schedule."}
          error={dateError ?? undefined}
        >
          {(c) => <Input {...c} type="date" value={date} onChange={(e) => setDate(e.target.value)} invalid={Boolean(dateError)} />}
        </FormField>
        <FormField id="rj-gen-base" label="Base amount for this run" hint="Blank uses the template's base amount.">
          {(c) => <Input {...c} inputMode="decimal" value={base} onChange={(e) => setBase(e.target.value)} />}
        </FormField>
        <div className={styles.formActions}>
          <Button
            variant="primary"
            loading={busy}
            disabled={paused}
            onClick={() => {
              if (!DATE.test(date)) {
                setDateError("Enter the scheduled date of the run.");
                return;
              }
              setDateError(null);
              void run(async () => {
                const r = await generateRecurringJournal(journal.id, { scheduledDate: date, ...(base ? { baseAmount: base } : {}) });
                setDate(null);
                return `Draft batch generated for ${r.scheduledDate}.`;
              });
            }}
          >
            Generate draft batch
          </Button>
          <Button
            loading={busy}
            onClick={() =>
              void run(async () => {
                const next = await recurringStatusAction(journal.id, paused ? "resume" : "pause", journal.version);
                return next.status === "paused" ? "Recurring journal paused." : "Recurring journal resumed.";
              })
            }
          >
            {paused ? "Resume" : "Pause"}
          </Button>
        </div>
        {message ? (
          <div className={styles.formActions}>
            <Alert tone={message.tone} title={message.tone === "success" ? "Done" : "That didn't work"} live={message.tone === "danger" ? "alert" : "status"}>
              {message.text}
            </Alert>
          </div>
        ) : null}
      </div>
    </Card>
  );
}

/** A recurring journal: status and schedule, generate and pause actions, generated batches, and the template form. */
export function RecurringJournalPage() {
  const { id = "" } = useParams();
  const queryClient = useQueryClient();
  const journal = useQuery({ queryKey: ["ledger", "recurring-journals", id], queryFn: () => fetchRecurringJournal(id) });
  const runs = useQuery({ queryKey: ["ledger", "recurring-journals", id, "runs"], queryFn: () => fetchRecurringRuns(id) });
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["ledger", "recurring-journals"] });

  return (
    <div className={styles.stack}>
      <PageHeader title={journal.data ? journal.data.name : "Recurring journal"} description="Generate the draft batch of a run, pause the schedule, or change the template." />
      {journal.isError ? (
        <Alert tone="danger" title="Could not load the recurring journal" live="alert" action={<Button size="sm" onClick={() => void journal.refetch()}>Try again</Button>}>
          {(journal.error as Error).message}
        </Alert>
      ) : journal.isPending ? (
        <Card>
          <div aria-busy="true">
            <span className="visually-hidden">Loading the recurring journal…</span>
            <Skeleton height="control" />
            <Skeleton height="control" width="1/2" />
          </div>
        </Card>
      ) : (
        <>
          <Card>
            <dl className={styles.details} aria-label="Schedule">
              <Detail term="Status" value={<RecurringStatusBadge status={journal.data.status} />} />
              <Detail term="Schedule" value={`${journal.data.frequency}, ${dayLabel(journal.data.dayOfMonth).toLowerCase()}`} />
              <Detail term="Next run" value={journal.data.nextRunDate ?? "None"} />
              <Detail term="Last run" value={journal.data.lastRunDate ?? "Never"} />
            </dl>
          </Card>
          <Actions journal={journal.data} onChanged={refresh} />
          {runs.isError ? (
            <Alert tone="danger" title="Could not load the generated batches" live="alert">
              {(runs.error as Error).message}
            </Alert>
          ) : (
            <DataTable
              label="Generated batches"
              columns={runColumns}
              rows={runs.data ?? []}
              getRowKey={(r) => r.id}
              loading={runs.isPending}
              empty={{ title: "Nothing generated yet", description: "Generate a draft batch to see it here." }}
            />
          )}
          <RecurringForm key={`form-${journal.data.version}`} initial={toDraft(journal.data)} existing={journal.data} />
        </>
      )}
    </div>
  );
}

function Detail({ term, value }: { term: string; value: ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{value}</dd>
    </div>
  );
}
