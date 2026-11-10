import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listVoyages, VOYAGE_STATUSES, type VoyageStatus, type VoyageSummary } from "./api/client";
import { formatDateTime, money, stateTone, statusLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<VoyageSummary>[] = [
  { key: "reference", header: "Voyage", mono: true },
  { key: "status", header: "Status", render: (row) => <Badge tone={stateTone(row.status)}>{statusLabel(row.status)}</Badge> },
  { key: "costTotal", header: "Landed cost", align: "end", render: (row) => money(row.costTotal, row.currency) },
  { key: "containers", header: "Containers", align: "end" },
  { key: "receiptLines", header: "Receipt lines", align: "end" },
  { key: "createdAt", header: "Created", render: (row) => formatDateTime(row.createdAt) },
  { key: "postedAt", header: "Posted", render: (row) => formatDateTime(row.postedAt) },
];

/** Landed cost voyages (PRD 6.34.6): inbound shipments whose freight, duty and other costs go onto inventory. */
export function VoyagesPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<VoyageStatus | "">("");
  const state = useLoad((signal) => listVoyages({ status }, signal), `voyages:${status}`);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Landed cost voyages"
        description="A voyage groups containers and the purchase receipt lines they carry. Allocating it spreads each cost over the receipt lines and posts the result to inventory costing."
        actions={
          <Link className={buttonClassName("primary")} to="/transport/voyages/new">
            New voyage
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="voyage-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as VoyageStatus | "")}>
              <option value="">All</option>
              {VOYAGE_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {statusLabel(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load voyages" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Voyages"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{
            title: status === "" ? "No voyages yet" : `No ${statusLabel(status).toLowerCase()} voyages`,
            description: "Create a voyage for an inbound shipment to allocate its landed cost.",
          }}
          onRowOpen={(row) => navigate(`/transport/voyages/${row.id}`)}
        />
      )}
    </div>
  );
}
