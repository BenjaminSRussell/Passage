import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { HOLD_STATUSES, listScreeningHolds, type HoldStatus, type ScreeningHold } from "./api/client";
import { dash, formatDateTime, shortId, stateTone, statusLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** The parties that hold the load, such as "Northwind (potential match)". */
export function heldPartiesText(hold: ScreeningHold): string {
  const held = hold.parties.filter((p) => p.status === "POTENTIAL_MATCH" || p.status === "BLOCKED" || p.status === "SCREENING_PENDING");
  return held.length === 0 ? dash : held.map((p) => `${p.name} (${statusLabel(p.status).toLowerCase()})`).join(", ");
}

const holdColumns: DataTableColumn<ScreeningHold>[] = [
  { key: "id", header: "Hold", mono: true, render: (row) => <span title={row.id}>{shortId(row.id)}</span> },
  { key: "status", header: "Status", render: (row) => <Badge tone={stateTone(row.status)}>{statusLabel(row.status)}</Badge> },
  { key: "loadId", header: "Load", mono: true, render: (row) => <Link to={`/transport/loads/${row.loadId}`} title={row.loadId}>{shortId(row.loadId)}</Link> },
  { key: "parties", header: "Held parties", render: heldPartiesText },
  { key: "createdAt", header: "Held since", render: (row) => formatDateTime(row.createdAt) },
  { key: "releasedBy", header: "Released by", render: (row) => row.releasedBy ?? dash },
];

/** Screening holds inquiry (PRD 6.34.14): loads waiting on trade compliance, and who released the others and why. */
export function ScreeningHoldsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<HoldStatus | "">("OPEN");
  const state = useLoad((signal) => listScreeningHolds({ status }, signal), `holds:${status}`);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Screening holds"
        description="Loads held because a party is a potential match, blocked or could not be screened (TMS-0133). A held load cannot be tendered until a trade compliance officer releases it with a reason; blocked parties stay held until the screening list changes."
      />
      <div className={styles.toolbar}>
        <FormField id="hold-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as HoldStatus | "")}>
              <option value="">All</option>
              {HOLD_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {statusLabel(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the screening holds" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Screening holds"
          columns={holdColumns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{
            title: status === "OPEN" ? "No loads on hold" : status === "" ? "No screening holds yet" : "No released holds",
            description: "Screening a load from its detail page holds it when a party needs review.",
          }}
          onRowOpen={(row) => navigate(`/transport/screening-holds/${row.id}`)}
        />
      )}
    </div>
  );
}
