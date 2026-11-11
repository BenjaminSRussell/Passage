import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, Card, ConfirmDialog, DataTable, FormField, PageHeader, Select, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { fetchReceipt, type Receipt } from "../receiving/api/client";
import { ApiError, fetchInvoices, fetchSubmission, fetchSubmissions, newKey, rejectSubmission, resolveSubmission, type Invoice, type Submission } from "./api/client";
import { submissionStatusLabel, submissionStatusTone, type InvoiceNavState } from "./parts";

const POST_DUTY = "receiving.vendor-invoice.post";
const RECEIPT_DUTY = "receiving.receipt.maintain";
const REASON_MAX = 1000;

type Line = Submission["lines"][number];

function Detail({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}

/** The post page, filled in from the submission: vendor, legal entity, invoice number and date, and the received lines. */
export function postState(s: Submission): InvoiceNavState {
  return {
    header: { legalEntityId: s.legalEntityId, invoiceNumber: s.invoiceNumber, invoiceDate: s.invoiceDate },
    items: s.lines.map((l) => ({ vendorId: s.vendorId ?? "", currency: s.currency, receiptId: l.receiptId, receiptLineNo: l.receiptLineNo, qty: l.qty, unitPrice: l.unitPrice })),
  };
}

export function SubmissionDetailPage() {
  const { id = "" } = useParams();
  return <SubmissionDetail key={id} id={id} />;
}

function SubmissionDetail({ id }: { id: string }) {
  const duties = usePrincipalDuties();
  const canPost = holdsDuty(duties, POST_DUTY);
  const canReceipt = holdsDuty(duties, RECEIPT_DUTY);
  const queryClient = useQueryClient();
  const result = useQuery({ queryKey: ["ap-invoices", "submission", id], queryFn: () => fetchSubmission(id) });
  const submission = result.data;
  const receiptIds = [...new Set(submission?.lines.map((l) => l.receiptId) ?? [])];
  const receipts = useQueries({ queries: receiptIds.map((receiptId) => ({ queryKey: ["receiving", "receipt", receiptId], queryFn: () => fetchReceipt(receiptId), enabled: canReceipt })) });
  const receiptById = new Map<string, Receipt>(receipts.flatMap((r) => (r.data ? [[r.data.id, r.data] as [string, Receipt]] : [])));
  const [message, setMessage] = useState<{ tone: "success" | "danger"; text?: string; problem?: unknown } | null>(null);

  const decided = async (updated: Submission, text: string) => {
    queryClient.setQueryData(["ap-invoices", "submission", id], updated);
    await queryClient.invalidateQueries({ queryKey: ["ap-invoices", "submissions"] });
    setMessage({ tone: "success", text });
  };

  const columns: DataTableColumn<Line>[] = [
    {
      key: "receiptId",
      header: "Receipt",
      render: (l) => {
        const receipt = receiptById.get(l.receiptId);
        return receipt ? <Link to={`/receiving/receipts/${encodeURIComponent(l.receiptId)}`}>{receipt.receiptNumber}</Link> : "Goods receipt";
      },
    },
    { key: "receiptLineNo", header: "Receipt line", align: "end" },
    { key: "qty", header: "Quantity", align: "end" },
    { key: "unitPrice", header: "Unit price", align: "end" },
    { key: "amount", header: "Amount", align: "end", render: (l) => `${l.amount} ${submission?.currency ?? ""}` },
  ];

  const missing = result.error instanceof ApiError && result.error.status === 404;
  const forbidden = result.error instanceof ApiError && result.error.status === 403;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={submission ? `Invoice submission ${submission.invoiceNumber}` : "Invoice submission"}
        description="An invoice a vendor submitted in the supplier portal against goods you received."
        meta={submission ? <Badge tone={submissionStatusTone[submission.status]}>{submissionStatusLabel[submission.status]}</Badge> : undefined}
        actions={
          <Link className={buttonClassName("ghost")} to="/ap-invoices/submissions">
            Back to invoice submissions
          </Link>
        }
      />
      {message ? (
        <Alert tone={message.tone} title={message.tone === "danger" ? "The decision failed" : undefined} live="status">
          {message.problem ? <ProblemMessage problem={message.problem} /> : message.text}
        </Alert>
      ) : null}
      {result.isError ? (
        <Alert
          tone="danger"
          title={forbidden ? "You can't view invoice submissions" : missing ? "Submission not found" : "Could not load the submission"}
          live="alert"
          action={forbidden || missing ? undefined : <Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}
        >
          <ProblemMessage problem={result.error} />
        </Alert>
      ) : !submission ? (
        <Card>
          <div aria-busy="true">
            <span className="visually-hidden">Loading the submission…</span>
            <Skeleton height="control" />
            <Skeleton height="control" width="1/2" />
          </div>
        </Card>
      ) : (
        <>
          <Card title="Details">
            <dl className={styles.details}>
              <Detail term="Vendor">
                <EntityName entity="vendor" id={submission.vendorId ?? ""} fallback="Unknown vendor" />
              </Detail>
              <Detail term="Legal entity">
                <EntityName entity="legalEntity" id={submission.legalEntityId} fallback="Unknown legal entity" />
              </Detail>
              <Detail term="Vendor's invoice number">{submission.invoiceNumber}</Detail>
              <Detail term="Invoice date">{submission.invoiceDate}</Detail>
              <Detail term="Total">{`${submission.total} ${submission.currency}`}</Detail>
              <Detail term="Submitted">{submission.createdAt.slice(0, 10)}</Detail>
              {submission.note ? <Detail term="Vendor's note">{submission.note}</Detail> : null}
              {submission.decidedAt ? <Detail term="Decided">{submission.decidedAt.slice(0, 10)}</Detail> : null}
              {submission.decisionNote ? <Detail term="Reason given to the vendor">{submission.decisionNote}</Detail> : null}
              {submission.invoiceId ? (
                <Detail term="Vendor invoice">
                  <Link to={`/ap-invoices/${encodeURIComponent(submission.invoiceId)}`}>Open the posted invoice</Link>
                </Detail>
              ) : null}
            </dl>
          </Card>
          <DataTable label="Submitted lines" columns={columns} rows={submission.lines} getRowKey={(l) => `${l.receiptId}-${l.receiptLineNo}`} />
          {submission.status === "SUBMITTED" ? (
            canPost ? (
              <>
                <Card title="Post it">
                  <p>Post a vendor invoice with these lines; the form opens filled in from the submission. Then record the posted invoice below.</p>
                  <Link className={buttonClassName("primary")} to="/ap-invoices/post" state={postState(submission)}>
                    Post as vendor invoice
                  </Link>
                </Card>
                <ResolveCard submission={submission} onDone={(s) => void decided(s, "Recorded. The vendor sees the submission as posted.")} onFailed={(problem) => setMessage({ tone: "danger", problem })} />
                <RejectCard submission={submission} onDone={(s) => void decided(s, "Rejected. The vendor sees your reason in the portal.")} onFailed={(problem) => setMessage({ tone: "danger", problem })} />
              </>
            ) : (
              <p>Posting or rejecting a submission needs the receiving.vendor-invoice.post duty.</p>
            )
          ) : null}
        </>
      )}
    </div>
  );
}

/** The most pages of the vendor's invoices and submissions the picker reads; a vendor with more is told so. */
const RESOLVE_PAGES = 10;
const RESOLVE_PAGE_SIZE = 200;

async function allPages<T>(load: (cursor?: string) => Promise<{ items: T[]; nextCursor?: string }>): Promise<{ items: T[]; capped: boolean }> {
  const items: T[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < RESOLVE_PAGES; page++) {
    const body = await load(cursor);
    items.push(...body.items);
    cursor = body.nextCursor;
    if (!cursor) return { items, capped: false };
  }
  return { items, capped: true };
}

/**
 * The invoices that can close a submission, as the service checks them: the vendor's, of the submission's legal entity,
 * posted or held for audit, and not already closing another submission. The one with the submission's number comes first.
 */
export async function closingInvoices(submission: Submission): Promise<{ items: Invoice[]; capped: boolean }> {
  const vendorId = submission.vendorId;
  const [posted, held, closed] = await Promise.all([
    allPages((cursor) => fetchInvoices({ vendorId, status: "POSTED", cursor, limit: RESOLVE_PAGE_SIZE })),
    allPages((cursor) => fetchInvoices({ vendorId, status: "AUDIT_BLOCKED", cursor, limit: RESOLVE_PAGE_SIZE })),
    allPages((cursor) => fetchSubmissions({ vendorId, status: "POSTED", cursor, limit: RESOLVE_PAGE_SIZE })),
  ]);
  const taken = new Set(closed.items.filter((s) => s.id !== submission.id && s.invoiceId).map((s) => s.invoiceId));
  const items = [...posted.items, ...held.items]
    .filter((i) => i.legalEntityId === submission.legalEntityId && !taken.has(i.id))
    .sort((a, b) => Number(b.invoiceNumber === submission.invoiceNumber) - Number(a.invoiceNumber === submission.invoiceNumber) || b.invoiceDate.localeCompare(a.invoiceDate));
  return { items, capped: posted.capped || held.capped || closed.capped };
}

function ResolveCard({ submission, onDone, onFailed }: { submission: Submission; onDone(s: Submission): void; onFailed(problem: unknown): void }) {
  const invoices = useQuery({ queryKey: ["ap-invoices", "closing-invoices", submission.id], queryFn: () => closingInvoices(submission), enabled: Boolean(submission.vendorId) });
  const options = invoices.data?.items ?? [];
  const [invoiceId, setInvoiceId] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (invoiceId === "") {
      setError("Choose the vendor invoice you posted from this submission.");
      return;
    }
    setError("");
    setBusy(true);
    try {
      onDone(await resolveSubmission(submission.id, invoiceId, key.current));
    } catch (failure) {
      onFailed(failure);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Record the posted invoice">
      <form onSubmit={(event) => void submit(event)} noValidate aria-label="Record the posted invoice">
        <FormField
          id="submission-invoice"
          label="Posted vendor invoice"
          hint={`This vendor's invoices in the submission's legal entity that are posted or held for audit and close no other submission; the one with the same number is first.${invoices.data?.capped ? " Only the newest are listed." : ""}`}
          error={error || (invoices.isError ? "The vendor's invoices could not be loaded." : undefined)}
        >
          {(control) => (
            <Select {...control} value={invoiceId} onChange={(e) => setInvoiceId(e.target.value)} invalid={Boolean(error)}>
              <option value="">{invoices.isPending ? "Loading invoices…" : "Choose an invoice"}</option>
              {options.map((i) => (
                <option key={i.id} value={i.id}>
                  {`${i.invoiceNumber}, ${i.total} ${i.currency}, ${i.invoiceDate}${i.status === "AUDIT_BLOCKED" ? ", held for audit" : ""}`}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button type="submit" loading={busy}>
          Mark as posted
        </Button>
      </form>
    </Card>
  );
}

function RejectCard({ submission, onDone, onFailed }: { submission: Submission; onDone(s: Submission): void; onFailed(problem: unknown): void }) {
  const [reason, setReason] = useState("");
  const [error, setError] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function ask(event: FormEvent) {
    event.preventDefault();
    const text = reason.trim();
    if (text === "" || text.length > REASON_MAX) {
      setError(`Enter a reason of at most ${REASON_MAX} characters.`);
      return;
    }
    setError("");
    setConfirming(true);
  }

  async function reject() {
    setConfirming(false);
    setBusy(true);
    try {
      onDone(await rejectSubmission(submission.id, reason.trim(), key.current));
    } catch (failure) {
      onFailed(failure);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Reject it">
      <form onSubmit={ask} noValidate aria-label="Reject the submission">
        <FormField id="submission-reason" label="Reason" hint={`The vendor reads this in the portal. At most ${REASON_MAX} characters.`} error={error || undefined}>
          {(control) => <Textarea {...control} rows={3} maxLength={REASON_MAX} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(error)} />}
        </FormField>
        <Button type="submit" variant="danger" loading={busy}>
          Reject submission
        </Button>
      </form>
      {confirming ? (
        <ConfirmDialog
          title={`Reject invoice ${submission.invoiceNumber}?`}
          description="The received lines are free to invoice again, and the vendor sees your reason."
          confirmLabel="Confirm rejection"
          cancelLabel="Cancel"
          onCancel={() => setConfirming(false)}
          onConfirm={() => void reject()}
        />
      ) : null}
    </Card>
  );
}
