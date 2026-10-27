import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listLoads, LOAD_STATUSES, type Load, type LoadStatus } from "./api/client";
import { fixed, formatDateTime, shortId, stateTone, statusLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** "12,400 / 20,000 kg": used against the load's capacity. */
const used = (value: string, max: string, unit: string) => `${fixed(value, 0)} / ${fixed(max, 0)} ${unit}`;

export const loadColumns: DataTableColumn<Load>[] = [
  { key: "id", header: "Load", mono: true, render: (row) => <span title={row.id}>{shortId(row.id)}</span> },
  { key: "status", header: "Status", render: (row) => <Badge tone={stateTone(row.status)}>{statusLabel(row.status)}</Badge> },
  { key: "mode", header: "Mode" },
  { key: "shipToRegion", header: "Ship-to region", mono: true },
  { key: "pickupDate", header: "Pickup" },
  { key: "shipments", header: "Shipments", align: "end", render: (row) => String(row.shipmentIds.length) },
  { key: "weight", header: "Weight", align: "end", render: (row) => used(row.totalWeightKg, row.capacity.maxWeightKg, "kg") },
  { key: "pallets", header: "Pallets", align: "end", render: (row) => `${row.pallets} / ${row.capacity.maxPallets}` },
  { key: "createdAt", header: "Built", render: (row) => formatDateTime(row.createdAt) },
];

/** Loads by status (PRD 6.34.14): what has been planned, and how full each load is. */
export function LoadsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<LoadStatus | "">("");
  const state = useLoad((signal) => listLoads({ status }, signal), `loads:${status}`);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Loads"
        description="Shipments consolidated into loads by ship-from site, ship-to region, mode and pickup date. Open a load to check its dangerous goods, screen its parties and tender it."
        actions={
          <Link className={buttonClassName("primary")} to="/transport/loads/build">
            Build loads
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="load-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as LoadStatus | "")}>
              <option value="">All</option>
              {LOAD_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {statusLabel(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load loads" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Loads"
          columns={loadColumns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{
            title: status === "" ? "No loads yet" : `No ${statusLabel(status).toLowerCase()} loads`,
            description: "Build loads from your open shipments to plan them.",
          }}
          onRowOpen={(row) => navigate(`/transport/loads/${row.id}`)}
        />
      )}
    </div>
  );
}
