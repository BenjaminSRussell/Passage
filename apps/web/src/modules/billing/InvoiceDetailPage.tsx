import { useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getInvoice,
  listInvoices,
  postCreditNote,
  postInvoice,
  type Installment,
  type Invoice,
  type InvoiceLine,
  type InvoiceSummary,
  type Problem,
} from "./api/client";
import { invoiceName, STATUS_LABELS, TYPE_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const baseColumns: DataTableColumn<InvoiceLine>[] = [
  { key: "lineNo", header: "Line", mono: true, align: "end" },
  { key: "description", header: "Description" },
  { key: "qty", header: "Quantity", mono: true, align: "end" },
  { key: "unitPrice", header: "Unit price", mono: true, align: "end" },
  { key: "discountPct", header: "Discount %", mono: true, align: "end", render: (l) => (Number(l.discountPct) === 0 ? "—" : l.discountPct) },
  { key: "revenueAccount", header: "Revenue account", mono: true },
  { key: "taxCode", header: "Tax code", mono: true },
  { key: "net", header: "Net", mono: true, align: "end" },
  { key: "tax", header: "Tax", mono: true, align: "end" },
];

const creditedColumn: DataTableColumn<InvoiceLine> = {
  key: "creditedQty",
  header: "Credited qty",
  mono: true,
  align: "end",
  render: (l) => (Number(l.creditedQty ?? 0) === 0 ? "—" : l.creditedQty),
};

const originalLineColumn: DataTableColumn<InvoiceLine> = { key: "originalLineNo", header: "Original line", mono: true, align: "end" };

const installmentColumns: DataTableColumn<Installment>[] = [
  { key: "no", header: "Installment", mono: true, align: "end" },
  { key: "percent", header: "Share %", mono: true, align: "end" },
  { key: "offsetDays", header: "Offset (days)", mono: true, align: "end" },
  { key: "dueDate", header: "Due", mono: true },
  { key: "amount", header: "Amount", mono: true, align: "end" },
];

const creditNoteColumns: DataTableColumn<InvoiceSummary>[] = [
  { key: "number", header: "Credit note", mono: true, render: (row) => invoiceName(row) },
  { key: "status", header: "Status", render: (row) => <StatusPill status={STATUS_LABELS[row.status] ?? row.status} /> },
  { key: "invoiceDate", header: "Credit date", mono: true },
  { key: "gross", header: "Gross", mono: true, align: "end", render: (row) => `${row.totals.gross} ${row.currency}` },
];

function Detail({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/** Why a post failed, in words the billing clerk can act on (§5.29.18). */
function failureTitle(code: string, creditNote: boolean): string {
  switch (code) {
    case "BIL-0001":
      return "The tax engine is unavailable; nothing was posted";
    case "BIL-0002":
      return "Tax changed since the proposal; review it before posting";
    case "BIL-0005":
      return "The credit no longer fits the original invoice";
    case "BIL-0103":
      return creditNote ? "The credit note changed since you opened it" : "The invoice changed since you opened it";
    default:
      return creditNote ? "The credit note was not posted" : "The invoice was not posted";
  }
}

/** The credit notes raised against one posted invoice. */
function CreditNotesCard({ invoiceId }: { invoiceId: string }) {
  const navigate = useNavigate();
  const state = useLoad((signal) => listInvoices({ type: "CREDIT_NOTE", originalInvoiceId: invoiceId, limit: 50 }, signal), invoiceId);
  return (
    <Card title="Credit notes">
      {state.error ? (
        <Alert tone="danger" title="Could not load the credit notes" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Credit notes of this invoice"
          columns={creditNoteColumns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No credit notes", description: "Nothing has been credited against this invoice." }}
          onRowOpen={(row) => navigate(`/billing/invoices/${row.id}`)}
        />
      )}
    </Card>
  );
}

/** Invoice or credit note proposal / detail (§5.29.6): lines, tax, totals, installments, and the post and credit actions. */
export function InvoiceDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getInvoice(id, signal), id);
  const [posting, setPosting] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState("");
  const [posted, setPosted] = useState<Invoice | null>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Invoice" />
        <Alert tone="danger" title="Could not load the invoice" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  const loaded = posted?.id === id ? posted : state.data;
  if (!loaded || loaded.id !== id) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Invoice" />
        <span className="visually-hidden">Loading the invoice…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const invoice: Invoice = loaded;
  const creditNote = invoice.type === "CREDIT_NOTE";

  async function post(inv: Invoice) {
    setPosting(true);
    setFailure(null);
    setDone("");
    try {
      const result = creditNote ? await postCreditNote(inv.id, inv.version) : await postInvoice(inv.id, inv.version);
      setPosted(result);
      setDone(
        creditNote
          ? `Posted as ${result.number ?? "a credit note"}. Receivables, revenue recognition and the ledger reverse the credited amounts.`
          : `Posted as ${result.number ?? "an invoice"}. The number is final and the invoice can no longer change.`,
      );
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "BIL-0103" || problem.code === "BIL-0101") {
        setPosted(null);
        state.reload();
      }
    } finally {
      setPosting(false);
    }
  }

  const currency = invoice.currency;
  const remaining = invoice.credited ? Number(invoice.credited.remaining) : 0;
  const lineColumns = creditNote ? [...baseColumns.slice(0, 1), originalLineColumn, ...baseColumns.slice(1)] : invoice.credited ? [...baseColumns, creditedColumn] : baseColumns;
  const kind = creditNote ? "Credit note" : `${TYPE_LABELS[invoice.type] ?? invoice.type} invoice`;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={invoiceName(invoice)}
        description={
          creditNote
            ? `${kind} · ${invoice.totals.gross} ${currency} · credits ${invoice.originalNumber || "the original invoice"}`
            : `${kind} · ${invoice.totals.gross} ${currency} · due ${invoice.dueDate}`
        }
        meta={<StatusPill status={STATUS_LABELS[invoice.status] ?? invoice.status} />}
        actions={
          <div className={styles.actions}>
            {invoice.status === "PROPOSAL" ? (
              <Button variant="primary" loading={posting} onClick={() => post(invoice)}>
                {creditNote ? "Post credit note" : "Post invoice"}
              </Button>
            ) : null}
            {invoice.credited && remaining > 0 ? (
              <Link className={buttonClassName("secondary")} to={`/billing/credit-notes/new?invoice=${encodeURIComponent(invoice.id)}`}>
                Credit invoice
              </Link>
            ) : null}
            {creditNote && invoice.originalInvoiceId ? (
              <Link className={buttonClassName("ghost")} to={`/billing/invoices/${invoice.originalInvoiceId}`}>
                Original invoice
              </Link>
            ) : null}
            <Link className={buttonClassName("ghost")} to={`/billing/invoices/${invoice.id}/trace`}>
              Trace
            </Link>
            <Link className={buttonClassName("ghost")} to={creditNote ? "/billing/credit-notes" : "/billing"}>
              {creditNote ? "All credit notes" : "All invoices"}
            </Link>
          </div>
        }
      />
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failureTitle(failure.code, creditNote)} live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {invoice.status === "PROPOSAL" ? (
        <p className={styles.note}>
          {creditNote
            ? "Posting checks the credit again against what the original invoice still allows, assigns the next gap-free CN- number and publishes the credit note to receivables, revenue recognition and the ledger."
            : "Posting re-quotes the tax, assigns the next gap-free invoice number and publishes the invoice to receivables and the ledger. A tax change of more than 0.01 stops the post for review."}
        </p>
      ) : null}
      <Card title="Totals">
        <dl className={styles.details}>
          <Detail term="Net" mono>{`${invoice.totals.net} ${currency}`}</Detail>
          <Detail term="Charges" mono>{`${invoice.totals.charges} ${currency}`}</Detail>
          <Detail term="Tax" mono>{`${invoice.totals.tax} ${currency}`}</Detail>
          <Detail term="Gross" mono>{`${invoice.totals.gross} ${currency}`}</Detail>
          {invoice.credited ? (
            <>
              <Detail term="Credited" mono>{`${invoice.credited.gross} ${currency}`}</Detail>
              <Detail term="Left to credit" mono>{`${invoice.credited.remaining} ${currency}`}</Detail>
            </>
          ) : null}
        </dl>
      </Card>
      <Card title={creditNote ? "Credit note" : "Invoice"}>
        <dl className={styles.details}>
          <Detail term="Status">{STATUS_LABELS[invoice.status] ?? invoice.status}</Detail>
          <Detail term="Number" mono>
            {invoice.number ?? "Assigned at posting"}
          </Detail>
          {creditNote ? (
            <>
              <Detail term="Original invoice" mono>
                {invoice.originalNumber || invoice.originalInvoiceId}
              </Detail>
              <Detail term="Reason">{invoice.reason}</Detail>
              <Detail term="Credit date" mono>
                {invoice.invoiceDate}
              </Detail>
            </>
          ) : (
            <>
              <Detail term="Invoice date" mono>
                {invoice.invoiceDate}
              </Detail>
              <Detail term="Due date" mono>
                {invoice.installments.length > 0 ? `${invoice.dueDate} (last installment)` : `${invoice.dueDate} (${invoice.paymentTermDays} days)`}
              </Detail>
            </>
          )}
          <Detail term="Posting date" mono>
            {invoice.postingDate ?? "Not posted"}
          </Detail>
          <Detail term="Customer" mono>
            {invoice.customerId}
          </Detail>
          <Detail term="Invoice account" mono>
            {invoice.invoiceAccountId}
          </Detail>
          <Detail term="Legal entity" mono>
            {invoice.legalEntityId}
          </Detail>
        </dl>
      </Card>
      <Card title="Lines">
        <DataTable
          label={creditNote ? "Credit note lines" : "Invoice lines"}
          columns={lineColumns}
          rows={invoice.lines}
          getRowKey={(l) => String(l.lineNo)}
          empty={{ title: "No lines", description: creditNote ? "This credit note credits header charges only." : "This invoice has no lines." }}
        />
      </Card>
      {invoice.installments.length > 0 ? (
        <Card title="Installments">
          <p className={styles.note}>Each installment becomes its own open item under the invoice number when the invoice posts. They sum to the gross exactly; the last one takes the rounding residual.</p>
          <DataTable label="Installments" columns={installmentColumns} rows={invoice.installments} getRowKey={(i) => String(i.no)} />
        </Card>
      ) : null}
      {invoice.credited ? <CreditNotesCard invoiceId={invoice.id} /> : null}
    </div>
  );
}
