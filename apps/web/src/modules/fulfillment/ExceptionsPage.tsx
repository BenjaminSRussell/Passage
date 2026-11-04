import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listExceptions, type ExceptionStatus, type WarehouseException } from "./api/client";
import { EXCEPTION_KIND_LABELS, formatDateTime, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export function ExceptionStatusBadge({ status }: { status: ExceptionStatus }) {
  return status === "OPEN" ? <Badge tone="warning">Open</Badge> : <Badge tone="neutral">Resolved</Badge>;
}

const columns: DataTableColumn<WarehouseException>[] = [
  {
    key: "kind",
    header: "Case",
    render: (row) => <Link to={`/fulfillment/exceptions/${row.id}`}>{EXCEPTION_KIND_LABELS[row.kind] ?? row.kind}</Link>,
  },
  { key: "status", header: "Status", render: (row) => <ExceptionStatusBadge status={row.status} /> },
  { key: "orderId", header: "Order", mono: true, render: (row) => shortId(row.orderId) },
  { key: "qty", header: "Quantity beyond", align: "end" },
  { key: "keptQty", header: "Order line keeps", align: "end" },
  { key: "createdAt", header: "Raised", render: (row) => formatDateTime(row.createdAt) },
];

/**
 * The warehouse supervisor's exceptions: cases fulfillment cannot fix alone when sales cancels the
 * remainder of an order (a packing slip that was already posted, or packed goods that must come back).
 */
export function ExceptionsPage() {
  const [status, setStatus] = useState<ExceptionStatus | "">("OPEN");
  const state = useLoad((signal) => listExceptions({ status, limit: 500 }, signal), status);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Warehouse exceptions"
        description="Cases raised when sales cancelled the remainder of an order and the warehouse had already packed or shipped more than the order line keeps."
      />
      <Card title="Filter">
        <div className={styles.form}>
          <FormField id="exc-status" label="Show">
            {(control) => (
              <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as ExceptionStatus | "")}>
                <option value="OPEN">Open</option>
                <option value="RESOLVED">Resolved</option>
                <option value="">Open and resolved</option>
              </Select>
            )}
          </FormField>
        </div>
      </Card>
      {state.error ? (
        <Alert tone="danger" title="Could not load the exceptions" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Warehouse exceptions"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={
            status === "OPEN"
              ? { title: "No open exceptions", description: "Nothing needs the supervisor right now." }
              : { title: "No exceptions", description: "No exception matches this filter." }
          }
        />
      )}
    </div>
  );
}
