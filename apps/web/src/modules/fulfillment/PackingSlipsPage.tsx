import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Alert, Button, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listPackingSlips, type PackingSlipStatus, type PackingSlipSummary } from "./api/client";
import { SlipStatusBadge } from "./badges";
import { formatDate, formatMoney, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<PackingSlipSummary>[] = [
  { key: "packingSlipNumber", header: "Packing slip", mono: true },
  { key: "status", header: "Status", render: (row) => <SlipStatusBadge status={row.status} reversal={row.reversalOfId !== null} /> },
  { key: "postingDate", header: "Posting date", render: (row) => formatDate(row.postingDate) },
  { key: "orderId", header: "Order", mono: true, render: (row) => shortId(row.orderId) },
  { key: "shipmentId", header: "Shipment", mono: true, render: (row) => shortId(row.shipmentId) },
  { key: "totalCost", header: "Cost", align: "end", render: (row) => formatMoney(row.totalCost, row.currency) },
];

const statuses: { value: PackingSlipStatus | ""; label: string }[] = [
  { value: "", label: "All statuses" },
  { value: "POSTED", label: "Posted" },
  { value: "CANCELLED", label: "Cancelled" },
  { value: "INVOICED", label: "Invoiced" },
];

/** Packing slip journal (section 5.28.14): posted delivery notes and their reversals. */
export function PackingSlipsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<PackingSlipStatus | "">("");
  const state = useLoad((signal) => listPackingSlips({ status }, signal), status);

  return (
    <div className={styles.stack}>
      <PageHeader title="Packing slips" description="Posted packing slips with the cost they moved from inventory. A cancelled slip has a reversal with the mirror entries." />
      <div className={styles.toolbar}>
        <FormField id="slip-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as PackingSlipStatus | "")}>
              {statuses.map((s) => (
                <option key={s.value} value={s.value}>
                  {s.label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load packing slips" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Packing slips"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          onRowOpen={(row) => navigate(`/fulfillment/packing-slips/${row.id}`)}
          empty={{ title: "No packing slips yet", description: "Packing slips appear here when a packed shipment is posted." }}
        />
      )}
    </div>
  );
}
