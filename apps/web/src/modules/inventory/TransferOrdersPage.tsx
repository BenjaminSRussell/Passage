import { useState } from "react";
import { Link } from "react-router-dom";
import { Badge, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listTransfers, TRANSFER_STATUSES, type TransferOrder, type TransferStatus } from "./api/client";
import { dash, sentence } from "./format";
import { ProblemAlert, stateTones } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Quantity totals of an order, for the list: "60 of 100 received". */
function progress(order: TransferOrder): string {
  if (order.status === "DRAFT" || order.status === "CREATED" || order.status === "CANCELLED") return dash;
  const sum = (pick: (l: TransferOrder["lines"][number]) => string) => order.lines.reduce((total, l) => total + Number(pick(l)), 0);
  return `${sum((l) => l.receivedQty)} of ${sum((l) => l.shippedQty)} received`;
}

const columns: DataTableColumn<TransferOrder>[] = [
  {
    key: "number",
    header: "Number",
    mono: true,
    render: (row) => <Link to={`/inventory/transfer-orders/${row.id}`}>{row.number}</Link>,
  },
  { key: "status", header: "Status", render: (row) => <Badge tone={stateTones[row.status] ?? "neutral"}>{sentence(row.status)}</Badge> },
  { key: "fromWarehouseId", header: "From", mono: true },
  { key: "toWarehouseId", header: "To", mono: true, render: (row) => (row.toSiteId ? `${row.toWarehouseId} (site ${row.toSiteId})` : row.toWarehouseId) },
  { key: "transitWarehouseId", header: "Transit", mono: true, render: (row) => (row.useTransit ? row.transitWarehouseId ?? dash : "One-step") },
  { key: "shipDate", header: "Ship date", mono: true },
  { key: "lines", header: "Lines", align: "end", render: (row) => String(row.lines.length) },
  { key: "progress", header: "Received", render: progress },
];

/** Transfer orders (FR-16.15): stock moving between warehouses, with its status. */
export function TransferOrdersPage() {
  const [status, setStatus] = useState<TransferStatus | "">("");
  const load = useLoad((signal) => listTransfers(status, signal), status);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Transfer orders"
        description="Move stock between warehouses. Shipping takes it out of the shipping warehouse into transit; receiving puts it away at the destination."
        actions={
          <>
            <Link className={buttonClassName("ghost")} to="/inventory/in-transit">
              Stock in transit
            </Link>
            <Link className={buttonClassName("primary")} to="/inventory/transfer-orders/new">
              New transfer order
            </Link>
          </>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="transfers-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as TransferStatus | "")}>
              <option value="">All</option>
              {TRANSFER_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {load.error ? (
        <ProblemAlert title="Could not load transfer orders" problem={load.error} onRetry={load.reload} />
      ) : (
        <DataTable
          label="Transfer orders"
          columns={columns}
          rows={load.data ?? []}
          getRowKey={(row) => row.id}
          loading={load.loading}
          empty={{
            title: status ? "No transfer orders in this status" : "No transfer orders yet",
            description: "Create a transfer order to move stock from one warehouse to another.",
          }}
        />
      )}
    </div>
  );
}
