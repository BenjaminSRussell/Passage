import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  batchAction,
  createJournalBatch,
  fetchJournalBatch,
  fetchJournalNames,
  fetchLedger,
  LedgerApiError,
  newKey,
  patchJournalBatch,
  postJournalBatch,
  type BatchAction,
  type BatchLine,
  type JournalBatch,
  type Violation,
} from "./api/client";
import { BatchLinesEditor, emptyLine, linesToDrafts, serviceLineErrors, toLineInputs, validateLines, type LineDraft } from "./BatchLinesEditor";
import { BatchStatusBadge } from "./JournalBatchesPage";
import { LedgerPicker, useLedgerParam } from "./LedgerPicker";
import { UUID } from "./pages";
import styles from "./pages.module.css";

const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** Decimals of an ISO 4217 currency (USD 2, JPY 0, KWD 3); 2 when the runtime does not know it. */
export function currencyScale(currency: string): number {
  try {
    return new Intl.NumberFormat("en", { style: "currency", currency }).resolvedOptions().maximumFractionDigits ?? 2;
  } catch {
    return 2;
  }
}

interface HeaderDraft {
  journalName: string;
  description: string;
  postingDate: string;
  documentDate: string;
}

type Errors = Record<string, string>;

/** Client checks of a batch header and its lines; the service runs the posting rules on submit. */
export function validateBatch(header: HeaderDraft, lines: LineDraft[], needsName: boolean): Errors {
  const errors: Errors = validateLines(lines);
  if (needsName && header.journalName === "") errors.journalName = "Pick a journal name.";
  if (header.description.length > 255) errors.description = "The description is at most 255 characters.";
  if (!DATE.test(header.postingDate)) errors.postingDate = "Enter the posting date.";
  if (header.documentDate !== "" && !DATE.test(header.documentDate)) errors.documentDate = "Enter a date or leave it empty.";
  return errors;
}

/** What went wrong with a request, ready to show. */
interface Failure {
  title: string;
  message: string;
  details: string[];
}

function failureOf(title: string, error: unknown): Failure {
  if (!(error instanceof LedgerApiError)) return { title, message: (error as Error).message, details: [] };
  if (error.status === 412) {
    return { title: "The batch changed since you opened it", message: "It was reloaded. Check it and try again.", details: [] };
  }
  const details = [...error.violations.map(violationText), ...serviceLineErrors(error.fields)];
  return { title, message: `${error.message} (${error.code})`, details };
}

function violationText(v: Violation): string {
  const where = v.lineIndex !== undefined ? `Line ${v.lineIndex + 1}: ` : "";
  return `${where}${v.message} (${v.code})`;
}

function FailureAlert({ failure }: { failure: Failure }) {
  return (
    <Alert tone="danger" title={failure.title} live="alert">
      {failure.message}
      {failure.details.length > 0 ? (
        <ul className={styles.problems}>
          {failure.details.map((d) => (
            <li key={d}>{d}</li>
          ))}
        </ul>
      ) : null}
    </Alert>
  );
}

function HeaderFields({ header, errors, onChange, prefix }: { header: HeaderDraft; errors: Errors; onChange(patch: Partial<HeaderDraft>): void; prefix: string }) {
  return (
    <>
      <FormField id={`${prefix}-description`} label="Description" optional hint="Leave it empty to use the default description template." error={errors.description}>
        {(c) => <Input {...c} value={header.description} maxLength={255} onChange={(e) => onChange({ description: e.target.value })} invalid={Boolean(errors.description)} />}
      </FormField>
      <FormField id={`${prefix}-posting`} label="Posting date" error={errors.postingDate}>
        {(c) => <Input {...c} type="date" value={header.postingDate} onChange={(e) => onChange({ postingDate: e.target.value })} invalid={Boolean(errors.postingDate)} />}
      </FormField>
      <FormField id={`${prefix}-document`} label="Document date" optional hint="Defaults to the posting date." error={errors.documentDate}>
        {(c) => <Input {...c} type="date" value={header.documentDate} onChange={(e) => onChange({ documentDate: e.target.value })} invalid={Boolean(errors.documentDate)} />}
      </FormField>
    </>
  );
}

function lineErrorList(errors: Errors): string[] {
  return Object.entries(errors)
    .filter(([k]) => k.startsWith("lines"))
    .map(([, v]) => v);
}

function NewBatchForm({ ledgerId }: { ledgerId: string }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const ledger = useQuery({ queryKey: ["ledger", "ledger", ledgerId], queryFn: () => fetchLedger(ledgerId) });
  const names = useQuery({ queryKey: ["ledger", "journal-names"], queryFn: () => fetchJournalNames() });
  const [header, setHeader] = useState<HeaderDraft>({ journalName: "", description: "", postingDate: "", documentDate: "" });
  const [lines, setLines] = useState<LineDraft[]>(() => [emptyLine("D"), emptyLine("C")]);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);

  if (ledger.isError || names.isError) {
    const retry = () => void Promise.all([ledger.refetch(), names.refetch()]);
    return (
      <Alert tone="danger" title="Could not load the ledger and its journal names" live="alert" action={<Button size="sm" onClick={retry}>Try again</Button>}>
        {((ledger.error ?? names.error) as Error).message}
      </Alert>
    );
  }
  if (ledger.isPending || names.isPending) {
    return (
      <Card>
        <div aria-busy="true">
          <span className="visually-hidden">Loading the ledger…</span>
          <Skeleton height="control" />
          <Skeleton height="control" width="1/2" />
        </div>
      </Card>
    );
  }
  const active = names.data.filter((n) => n.active);
  if (active.length === 0) {
    return (
      <Alert tone="info" title="No active journal names" action={<Link className={buttonClassName("secondary", "sm")} to="/ledger/journal-names/new">New journal name</Link>}>
        Every batch is entered under a journal name. Create one first.
      </Alert>
    );
  }
  const currency = ledger.data.currency;

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateBatch(header, lines, true);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createJournalBatch({
        ledgerId,
        journalName: header.journalName,
        description: header.description.trim(),
        postingDate: header.postingDate,
        ...(header.documentDate ? { documentDate: header.documentDate } : {}),
        lines: toLineInputs(lines, currency),
      });
      await queryClient.invalidateQueries({ queryKey: ["ledger", "journal-batches"] });
      navigate(`/ledger/journal-batches/${created.id}`);
    } catch (error) {
      setFailure(failureOf("Could not create the batch", error));
    } finally {
      setBusy(false);
    }
  }

  const lineErrors = lineErrorList(errors);
  return (
    <Card title={`Batch on ledger ${ledger.data.code} (${currency})`}>
      <form className={styles.form} onSubmit={submit} noValidate aria-label="New journal batch">
        <FormField id="nb-name" label="Journal name" error={errors.journalName}>
          {(c) => (
            <Select {...c} value={header.journalName} onChange={(e) => setHeader({ ...header, journalName: e.target.value })} invalid={Boolean(errors.journalName)}>
              <option value="">Pick a journal name</option>
              {active.map((n) => (
                <option key={n.code} value={n.code}>
                  {n.code}: {n.name}
                  {n.requiresApproval ? " (approval)" : ""}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <HeaderFields prefix="nb" header={header} errors={errors} onChange={(patch) => setHeader({ ...header, ...patch })} />
        <div className={styles.wide}>
          <BatchLinesEditor lines={lines} currency={currency} scale={currencyScale(currency)} onChange={setLines} />
        </div>
        {lineErrors.length > 0 ? (
          <ul className={`${styles.problems} ${styles.wide}`} role="alert" aria-label="Line problems">
            {lineErrors.map((e) => (
              <li key={e}>{e}</li>
            ))}
          </ul>
        ) : null}
        {failure ? (
          <div className={styles.formActions}>
            <FailureAlert failure={failure} />
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Create batch
          </Button>
          <Link className={buttonClassName("ghost")} to="/ledger/journal-batches">
            Cancel
          </Link>
        </div>
      </form>
    </Card>
  );
}

/** New manual journal batch: pick the ledger, then enter the header and lines (FR-11.9). */
export function NewJournalBatchPage() {
  const [ledgerId, setLedgerId] = useLedgerParam();
  return (
    <div className={styles.stack}>
      <PageHeader title="New journal batch" description="The batch is saved as a draft. Validate it, or submit it for approval, before you post it." />
      <LedgerPicker id="nb-ledger" label="Ledger for the new batch" ledgerId={ledgerId} onChange={setLedgerId} />
      {ledgerId ? <NewBatchForm key={ledgerId} ledgerId={ledgerId} /> : <p className={styles.note}>Enter the id of the ledger the batch posts to.</p>}
    </div>
  );
}

const lineColumns: DataTableColumn<BatchLine>[] = [
  { key: "lineNumber", header: "Line", align: "end" },
  { key: "account", header: "Account", mono: true },
  {
    key: "dimensions",
    header: "Dimensions",
    mono: true,
    render: (l) =>
      Object.entries(l.dimensions ?? {})
        .map(([a, v]) => `${a}=${v}`)
        .join(", "),
  },
  { key: "debitCredit", header: "D/C" },
  { key: "amount", header: "Amount", align: "end" },
  { key: "text", header: "Text", render: (l) => l.text ?? "" },
];

function Detail({ term, value, mono = false }: { term: string; value: string; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
    </div>
  );
}

const actionDone: Record<BatchAction, string> = {
  submit: "Batch submitted",
  approve: "Batch approved",
  reject: "Batch rejected",
  cancel: "Batch cancelled",
};

/** The actions BR-11.10 allows in the batch's status, given its journal name. */
export function availableActions(b: JournalBatch) {
  const s = b.status;
  return {
    edit: s === "draft" || s === "validated" || s === "approved" || s === "rejected",
    submit: s === "draft" || (s === "validated" && b.requiresApproval),
    decide: s === "in_approval",
    post: s === "approved" || (s === "validated" && !b.requiresApproval),
    cancel: s === "draft" || s === "validated" || s === "rejected",
  };
}

/** Edits header and lines. On a version conflict the page reloads the batch; the user's edits stay in the form. */
function EditBatchForm({ batch, onSaved, onCancel, onConflict }: { batch: JournalBatch; onSaved(b: JournalBatch): void; onCancel(): void; onConflict(): void }) {
  const [header, setHeader] = useState<HeaderDraft>({
    journalName: batch.journalName,
    description: batch.description,
    postingDate: batch.postingDate,
    documentDate: batch.documentDate ?? "",
  });
  const [lines, setLines] = useState<LineDraft[]>(() => linesToDrafts(batch.lines ?? []));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateBatch(header, lines, false);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const saved = await patchJournalBatch(batch.id, batch.version, {
        description: header.description.trim(),
        postingDate: header.postingDate,
        documentDate: header.documentDate,
        lines: toLineInputs(lines, batch.currency),
      });
      onSaved(saved);
    } catch (error) {
      setFailure(failureOf("Could not save the batch", error));
      setBusy(false);
      if (error instanceof LedgerApiError && error.status === 412) onConflict();
    }
  }

  const lineErrors = lineErrorList(errors);
  return (
    <Card title="Edit batch">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Edit journal batch">
        <HeaderFields prefix="eb" header={header} errors={errors} onChange={(patch) => setHeader({ ...header, ...patch })} />
        <div className={styles.wide}>
          <BatchLinesEditor lines={lines} currency={batch.currency} scale={currencyScale(batch.currency)} onChange={setLines} />
        </div>
        {lineErrors.length > 0 ? (
          <ul className={`${styles.problems} ${styles.wide}`} role="alert" aria-label="Line problems">
            {lineErrors.map((e) => (
              <li key={e}>{e}</li>
            ))}
          </ul>
        ) : null}
        {batch.status !== "draft" ? <p className={`${styles.note} ${styles.wide}`}>Saving returns the batch to draft; it must be validated{batch.requiresApproval ? " and approved" : ""} again.</p> : null}
        {failure ? (
          <div className={styles.formActions}>
            <FailureAlert failure={failure} />
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save changes
          </Button>
          <Button variant="ghost" onClick={onCancel}>
            Discard changes
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** Journal batch detail (§2.11.6): header, totals, lines and the BR-11.10 actions. */
export function JournalBatchPage() {
  const { id = "" } = useParams();
  const valid = UUID.test(id);
  const queryClient = useQueryClient();
  const queryKey = ["ledger", "journal-batch", id];
  const result = useQuery({ queryKey, queryFn: () => fetchJournalBatch(id), enabled: valid });
  const [editing, setEditing] = useState(false);
  const [confirmPost, setConfirmPost] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [done, setDone] = useState<{ text: string; entryId?: string } | null>(null);
  // One Idempotency-Key per batch version: a retry of the same post replays.
  const postKey = useRef<{ version: number; key: string } | null>(null);

  if (!valid) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Journal batch" />
        <Alert tone="danger" title="The batch id in the link is not valid" live="alert" />
      </div>
    );
  }
  if (result.isError) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Journal batch" />
        <Alert tone="danger" title="Could not load the batch" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      </div>
    );
  }
  const batch = result.data;

  function settle(updated: JournalBatch, text: string, entryId?: string) {
    queryClient.setQueryData(queryKey, updated);
    void queryClient.invalidateQueries({ queryKey: ["ledger", "journal-batches"] });
    setDone({ text, entryId });
  }

  async function fail(title: string, error: unknown) {
    setFailure(failureOf(title, error));
    if (error instanceof LedgerApiError && (error.status === 412 || error.status === 409 || error.status === 422)) {
      await result.refetch();
    }
  }

  async function act(action: BatchAction) {
    if (!batch) return;
    setBusy(action);
    setFailure(null);
    setDone(null);
    try {
      const updated = await batchAction(batch.id, action, batch.version);
      settle(updated, updated.status === "in_approval" ? "Submitted for approval" : updated.status === "validated" ? "Batch validated; it can be posted" : actionDone[action]);
    } catch (error) {
      await fail(action === "submit" ? "The batch did not pass validation" : `Could not ${action} the batch`, error);
    } finally {
      setBusy(null);
    }
  }

  async function post() {
    if (!batch) return;
    if (postKey.current?.version !== batch.version) postKey.current = { version: batch.version, key: newKey() };
    setBusy("post");
    setFailure(null);
    setDone(null);
    try {
      const res = await postJournalBatch(batch.id, batch.version, postKey.current.key);
      const refreshed = await fetchJournalBatch(batch.id);
      const entry = res.entries[0];
      settle(refreshed, `Posted as voucher ${entry.voucher}`, entry.id);
      setConfirmPost(false);
    } catch (error) {
      setConfirmPost(false);
      await fail("The batch was not posted", error);
    } finally {
      setBusy(null);
    }
  }

  const can = batch ? availableActions(batch) : null;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={batch ? `Journal batch ${batch.journalName}` : "Journal batch"}
        description={batch ? batch.description || "No description: the default description template applies when it is posted." : undefined}
        meta={batch ? <BatchStatusBadge status={batch.status} /> : undefined}
        actions={
          <Link className={buttonClassName("ghost")} to="/ledger/journal-batches">
            All batches
          </Link>
        }
      />
      {done ? (
        <Alert tone="success" title={done.text} live="status">
          {done.entryId ? <Link to={`/ledger/entries/${done.entryId}`}>Open the journal entry</Link> : null}
        </Alert>
      ) : null}
      {failure ? <FailureAlert failure={failure} /> : null}
      {batch?.lastError && !failure ? (
        <Alert tone="warning" title="The last post failed">
          {batch.lastError}
        </Alert>
      ) : null}
      {!batch ? (
        <Card>
          <div aria-busy="true">
            <span className="visually-hidden">Loading the batch…</span>
            <Skeleton height="control" />
            <Skeleton height="control" width="1/2" />
          </div>
        </Card>
      ) : (
        <>
          <Card title="Details">
            <dl className={styles.details}>
              <Detail term="Journal name" value={batch.journalName} mono />
              <Detail term="Approval" value={batch.requiresApproval ? "Required" : "Not required"} />
              <Detail term="Posting date" value={batch.postingDate} />
              <Detail term="Document date" value={batch.documentDate ?? batch.postingDate} />
              <Detail term={`Total debit (${batch.currency})`} value={batch.totalDebit} />
              <Detail term={`Total credit (${batch.currency})`} value={batch.totalCredit} />
              <Detail term="Lines" value={String(batch.lineCount)} />
              <Detail term="Owner" value={batch.createdBy} />
              {batch.submittedBy ? <Detail term="Submitted by" value={batch.submittedBy} /> : null}
              {batch.decidedBy ? <Detail term={batch.status === "rejected" ? "Rejected by" : "Approved by"} value={batch.decidedBy} /> : null}
              {batch.postedBy ? <Detail term="Posted by" value={batch.postedBy} /> : null}
              {batch.postedEntryId ? (
                <div className={styles.entry}>
                  <dt className={styles.term}>Journal entry</dt>
                  <dd className={styles.value}>
                    <Link to={`/ledger/entries/${batch.postedEntryId}`}>Open the posted entry</Link>
                  </dd>
                </div>
              ) : null}
              <Detail term="Ledger" value={batch.ledgerId} mono />
            </dl>
          </Card>
          {can && !editing && (can.edit || can.submit || can.decide || can.post || can.cancel) ? (
            <Card title="Actions">
              <div className={styles.formActions}>
                {can.submit ? (
                  <Button variant="primary" loading={busy === "submit"} onClick={() => void act("submit")}>
                    {batch.requiresApproval ? "Submit for approval" : "Validate"}
                  </Button>
                ) : null}
                {can.decide ? (
                  <>
                    <Button variant="primary" loading={busy === "approve"} onClick={() => void act("approve")}>
                      Approve
                    </Button>
                    <Button variant="danger" loading={busy === "reject"} onClick={() => void act("reject")}>
                      Reject
                    </Button>
                  </>
                ) : null}
                {can.post && !confirmPost ? (
                  <Button variant="primary" onClick={() => setConfirmPost(true)}>
                    Post
                  </Button>
                ) : null}
                {can.edit ? <Button onClick={() => { setEditing(true); setDone(null); setFailure(null); }}>Edit batch</Button> : null}
                {can.cancel ? (
                  <Button variant="ghost" loading={busy === "cancel"} onClick={() => void act("cancel")}>
                    Cancel batch
                  </Button>
                ) : null}
              </div>
              {confirmPost ? (
                <div className={styles.confirm}>
                  <p className={styles.note}>
                    Post {batch.lineCount} lines for {batch.totalDebit} {batch.currency} on {batch.postingDate}? Posted entries can't be changed, only reversed.
                  </p>
                  <div className={styles.formActions}>
                    <Button variant="primary" loading={busy === "post"} onClick={() => void post()}>
                      Confirm post
                    </Button>
                    <Button variant="ghost" onClick={() => setConfirmPost(false)}>
                      Don't post
                    </Button>
                  </div>
                </div>
              ) : null}
            </Card>
          ) : null}
          {editing ? (
            <EditBatchForm
              batch={batch}
              onCancel={() => setEditing(false)}
              onConflict={() => void result.refetch()}
              onSaved={(saved) => {
                setEditing(false);
                settle(saved, saved.status === "draft" && batch.status !== "draft" ? "Saved; the batch is back in draft" : "Batch saved");
              }}
            />
          ) : (
            <DataTable label="Batch lines" columns={lineColumns} rows={batch.lines ?? []} getRowKey={(l) => String(l.lineNumber)} empty={{ title: "No lines" }} />
          )}
          {batch.status === "posted" || batch.status === "cancelled" ? (
            <p className={styles.note}>{batch.status === "posted" ? "Posted batches can't be changed; reverse the journal entry instead." : "Cancelled batches can't be changed."}</p>
          ) : null}
        </>
      )}
    </div>
  );
}
