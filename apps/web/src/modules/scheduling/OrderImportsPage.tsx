import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listOrderImports, type OrderImport } from "./api/client";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const IMPORT_STATUS_LABELS: Record<OrderImport["status"], string> = { imported: "Imported", partial: "Partly imported", rejected: "Rejected" };
const STATUS_TONES = { imported: "success", partial: "warning", rejected: "danger" } as const;

export function ImportStatusBadge({ status }: { status: OrderImport["status"] }) {
  return <Badge tone={STATUS_TONES[status]}>{IMPORT_STATUS_LABELS[status]}</Badge>;
}

const columns: DataTableColumn<OrderImport>[] = [
  { key: "orderNumber", header: "Order", mono: true },
  { key: "status", header: "Result", render: (row) => <ImportStatusBadge status={row.status} /> },
  { key: "operationsTotal", header: "Operations", align: "end" },
  { key: "created", header: "Registered", align: "end", render: (row) => String(row.created + row.updated + row.unchanged) },
  { key: "skipped", header: "Left out", align: "end", render: (row) => String(row.skipped.filter((s) => s.code !== "firmed").length) },
  { key: "replanRequired", header: "Run needed", render: (row) => (row.replanRequired ? <Badge tone="warning">Schedule again</Badge> : "") },
  { key: "orderVersion", header: "Order version", align: "end" },
  { key: "receivedAt", header: "Received", render: (row) => formatDateTime(row.receivedAt) },
];

/** Production orders received from wave 21 and what scheduling made of their operations (FR-23.10). */
export function OrderImportsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState("");
  const state = useLoad((signal) => listOrderImports(status, signal), status);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Production orders received"
        description="Each production order that was dated in wave 21 arrives here, and its route operations become operations to schedule. Orders with operations that could not be registered are listed with the reason; run the schedule after an order changes."
        actions={
          <Link className={buttonClassName("secondary")} to="/scheduling/operations">
            Operations
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="import-status" label="Result">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value)}>
              <option value="">All orders</option>
              <option value="imported">Imported</option>
              <option value="partial">Partly imported</option>
              <option value="rejected">Rejected</option>
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load production orders" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Production orders received"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.orderId}
          loading={state.loading}
          empty={{
            title: status ? "No orders with that result" : "No production orders received yet",
            description: status ? "Choose another result to see more orders." : "Orders appear here once production schedules them with infinite capacity.",
          }}
          onRowOpen={(row) => navigate(`/scheduling/order-imports/${row.orderId}`)}
        />
      )}
    </div>
  );
}
