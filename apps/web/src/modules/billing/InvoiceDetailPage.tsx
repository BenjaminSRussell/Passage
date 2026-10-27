import { useState, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getInvoice, postInvoice, type Invoice, type InvoiceLine, type Problem } from "./api/client";
import { invoiceName, STATUS_LABELS, TYPE_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const lineColumns: DataTableColumn<InvoiceLine>[] = [
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

function Detail({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/** Why a post failed, in words the billing clerk can act on (§5.29.18). */
function failureTitle(code: string): string {
  switch (code) {
    case "BIL-0001":
      return "The tax engine is unavailable; nothing was posted";
    case "BIL-0002":
      return "Tax changed since the proposal; review it before posting";
    case "BIL-0103":
      return "The invoice changed since you opened it";
    default:
      return "The invoice was not posted";
  }
}

/** Invoice proposal / detail (§5.29.6): lines, tax, totals, and the post action for a proposal. */
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
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  const invoice = posted ?? state.data;
  if (!invoice) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Invoice" />
        <span className="visually-hidden">Loading the invoice…</span>
        <Skeleton height="tile" />
      </div>
    );
  }

  async function post(inv: Invoice) {
    setPosting(true);
    setFailure(null);
    setDone("");
    try {
      const result = await postInvoice(inv.id, inv.version);
      setPosted(result);
      setDone(`Posted as ${result.number ?? "an invoice"}. The number is final and the invoice can no longer change.`);
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
  return (
    <div className={styles.stack}>
      <PageHeader
        title={invoiceName(invoice)}
        description={`${TYPE_LABELS[invoice.type] ?? invoice.type} invoice · ${invoice.totals.gross} ${currency} · due ${invoice.dueDate}`}
        meta={<StatusPill status={STATUS_LABELS[invoice.status] ?? invoice.status} />}
        actions={
          <div className={styles.actions}>
            {invoice.status === "PROPOSAL" ? (
              <Button variant="primary" loading={posting} onClick={() => post(invoice)}>
                Post invoice
              </Button>
            ) : null}
            <Link className={buttonClassName("ghost")} to="/billing">
              All invoices
            </Link>
          </div>
        }
      />
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failureTitle(failure.code)} live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {invoice.status === "PROPOSAL" ? (
        <p className={styles.note}>
          Posting re-quotes the tax, assigns the next gap-free invoice number and publishes the invoice to receivables and the ledger. A tax change of
          more than 0.01 stops the post for review.
        </p>
      ) : null}
      <Card title="Totals">
        <dl className={styles.details}>
          <Detail term="Net" mono>{`${invoice.totals.net} ${currency}`}</Detail>
          <Detail term="Charges" mono>{`${invoice.totals.charges} ${currency}`}</Detail>
          <Detail term="Tax" mono>{`${invoice.totals.tax} ${currency}`}</Detail>
          <Detail term="Gross" mono>{`${invoice.totals.gross} ${currency}`}</Detail>
        </dl>
      </Card>
      <Card title="Invoice">
        <dl className={styles.details}>
          <Detail term="Status">{STATUS_LABELS[invoice.status] ?? invoice.status}</Detail>
          <Detail term="Number" mono>
            {invoice.number ?? "Assigned at posting"}
          </Detail>
          <Detail term="Invoice date" mono>
            {invoice.invoiceDate}
          </Detail>
          <Detail term="Due date" mono>
            {`${invoice.dueDate} (${invoice.paymentTermDays} days)`}
          </Detail>
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
          label="Invoice lines"
          columns={lineColumns}
          rows={invoice.lines}
          getRowKey={(l) => String(l.lineNo)}
          empty={{ title: "No lines", description: "This invoice has no lines." }}
        />
      </Card>
    </div>
  );
}
