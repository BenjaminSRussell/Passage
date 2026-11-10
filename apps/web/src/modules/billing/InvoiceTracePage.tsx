import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import { getInvoiceTrace, type InvoiceTrace, type TraceLine } from "./api/client";
import { invoiceName, STATUS_LABELS, TYPE_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { OrderLineNo, OrderNumber } from "../sales/OrderNumber";

const dash = (v?: string) => v || "—";

const lineColumns: DataTableColumn<TraceLine>[] = [
  { key: "lineNo", header: "Line", mono: true, align: "end" },
  { key: "description", header: "Description" },
  { key: "qty", header: "Quantity", mono: true, align: "end" },
  { key: "net", header: "Net", mono: true, align: "end" },
  { key: "packingSlipId", header: "Packing slip", mono: true, render: (l) => dash(l.packingSlipId) },
  { key: "packingSlipLineId", header: "Slip line", mono: true, render: (l) => dash(l.packingSlipLineId) },
  { key: "orderId", header: "Sales order", render: (l) => (l.orderId ? <OrderNumber id={l.orderId} /> : dash()) },
  { key: "orderLineId", header: "Order line", render: (l) => (l.orderId && l.orderLineId ? <OrderLineNo orderId={l.orderId} lineId={l.orderLineId} /> : dash()) },
  { key: "productId", header: "Product", render: (l) => (l.productId ? <EntityName entity="product" id={l.productId} /> : dash()) },
  { key: "priceExplainRef", header: "Price explanation", mono: true, render: (l) => dash(l.priceExplainRef) },
];

const creditLineColumns: DataTableColumn<TraceLine>[] = [
  lineColumns[0],
  { key: "originalLineNo", header: "Original line", mono: true, align: "end" },
  ...lineColumns.slice(1),
];

const slipColumns: DataTableColumn<InvoiceTrace["packingSlips"][number]>[] = [
  { key: "id", header: "Packing slip", mono: true },
  { key: "date", header: "Slip date", mono: true },
  { key: "orderId", header: "Sales order", render: (s) => <OrderNumber id={s.orderId} /> },
];

/** Invoice trace (FR-29.12, §5.29.14): each invoice line back to its packing slip, order line and price explanation. */
export function InvoiceTracePage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getInvoiceTrace(id, signal), id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Invoice trace" />
        <Alert tone="danger" title="Could not load the trace" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  const trace = state.data;
  if (!trace || trace.invoice.id !== id) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Invoice trace" />
        <span className="visually-hidden">Loading the trace…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const inv = trace.invoice;
  const creditNote = inv.type === "CREDIT_NOTE";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Trace of ${invoiceName(inv)}`}
        description={`${TYPE_LABELS[inv.type] ?? inv.type} · ${inv.totals.gross} ${inv.currency} · invoice date ${inv.invoiceDate}`}
        meta={<StatusPill status={STATUS_LABELS[inv.status] ?? inv.status} />}
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to={`/billing/invoices/${inv.id}`}>
              {creditNote ? "Open credit note" : "Open invoice"}
            </Link>
            {trace.original ? (
              <Link className={buttonClassName("ghost")} to={`/billing/invoices/${trace.original.id}/trace`}>
                {`Trace of ${invoiceName(trace.original)}`}
              </Link>
            ) : null}
          </div>
        }
      />
      {trace.original ? (
        <p className={styles.note}>{`This credit note's lines resolve through the lines of ${invoiceName(trace.original)}, the invoice it credits.`}</p>
      ) : null}
      <Card title="Packing slips">
        <DataTable
          label="Packing slips billed"
          columns={slipColumns}
          rows={trace.packingSlips}
          getRowKey={(s) => s.id}
          empty={{
            title: "No packing slips",
            description: "This document does not bill shipments: free-text invoices and their credit notes have no packing slips behind them.",
          }}
        />
      </Card>
      <Card title="Lines and their sources">
        <DataTable
          label="Trace lines"
          columns={creditNote ? creditLineColumns : lineColumns}
          rows={trace.lines}
          getRowKey={(l) => String(l.lineNo)}
          empty={{ title: "No lines", description: "This document has no lines." }}
        />
        <p className={styles.note}>Revenue schedules of the lines are not on the trace yet; open the revenue contract of the order to see them.</p>
      </Card>
    </div>
  );
}
