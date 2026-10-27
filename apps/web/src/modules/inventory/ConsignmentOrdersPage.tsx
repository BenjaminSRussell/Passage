import { useState } from "react";
import { Link } from "react-router-dom";
import { Badge, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { CONSIGNMENT_STATUSES, listConsignments, type ConsignmentOrder, type ConsignmentStatus } from "./api/client";
import { dash, sentence } from "./format";
import { ProblemAlert, stateTones } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** "120 of 500 received" across the order's lines. */
function received(order: ConsignmentOrder): string {
  if (order.status === "CREATED") return dash;
  const sum = (pick: (l: ConsignmentOrder["lines"][number]) => string) => order.lines.reduce((total, l) => total + Number(pick(l)), 0);
  return `${sum((l) => l.receivedQty)} of ${sum((l) => l.qty)}`;
}

const columns: DataTableColumn<ConsignmentOrder>[] = [
  { key: "number", header: "Number", mono: true, render: (row) => <Link to={`/inventory/consignment-orders/${row.id}`}>{row.number}</Link> },
  { key: "status", header: "Status", render: (row) => <Badge tone={stateTones[row.status] ?? "neutral"}>{sentence(row.status)}</Badge> },
  { key: "vendorId", header: "Vendor", mono: true },
  { key: "warehouseId", header: "Warehouse", mono: true, render: (row) => `${row.warehouseId} (site ${row.siteId})` },
  { key: "requestedDate", header: "Requested", mono: true },
  { key: "lines", header: "Lines", align: "end", render: (row) => String(row.lines.length) },
  { key: "received", header: "Received", align: "end", mono: true, render: received },
];

/** Vendor consignment replenishment orders (FR-16.17): vendor-owned stock delivered as quantity only. */
export function ConsignmentOrdersPage() {
  const [status, setStatus] = useState<ConsignmentStatus | "">("");
  const load = useLoad((signal) => listConsignments({ status }, signal), status);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Consignment orders"
        description="Replenishment orders for stock the vendor keeps owning until it is taken into company ownership. Receipts carry no value and post nothing to the ledger."
        actions={
          <>
            <Link className={buttonClassName("ghost")} to="/inventory/consignment/stock">
              Consignment stock
            </Link>
            <Link className={buttonClassName("primary")} to="/inventory/consignment-orders/new">
              New consignment order
            </Link>
          </>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="consignment-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as ConsignmentStatus | "")}>
              <option value="">All</option>
              {CONSIGNMENT_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {load.error ? (
        <ProblemAlert title="Could not load consignment orders" problem={load.error} onRetry={load.reload} />
      ) : (
        <DataTable
          label="Consignment orders"
          columns={columns}
          rows={load.data ?? []}
          getRowKey={(row) => row.id}
          loading={load.loading}
          empty={{
            title: status ? "No consignment orders in this status" : "No consignment orders yet",
            description: "Create a consignment order when a vendor delivers stock it keeps owning.",
          }}
        />
      )}
    </div>
  );
}
