import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { BILL_STATUSES, listFreightBills, type BillStatus, type FreightBill } from "./api/client";
import { formatDateTime, matchTone, money, shortId, statusLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<FreightBill>[] = [
  { key: "id", header: "Freight bill", mono: true, render: (row) => shortId(row.id) },
  { key: "loadId", header: "Load", mono: true, render: (row) => <span title={row.loadId}>{shortId(row.loadId)}</span> },
  { key: "carrierId", header: "Carrier", mono: true, render: (row) => <span title={row.carrierId}>{shortId(row.carrierId)}</span> },
  { key: "expectedTotal", header: "Expected", align: "end", render: (row) => money(row.expectedTotal, row.currency) },
  { key: "varianceTotal", header: "Variance", align: "end", render: (row) => money(row.varianceTotal, row.currency) },
  { key: "matchStatus", header: "Match", render: (row) => <Badge tone={matchTone(row.matchStatus)}>{statusLabel(row.matchStatus)}</Badge> },
  { key: "createdAt", header: "Created", render: (row) => formatDateTime(row.createdAt) },
];

/** Freight reconciliation status (PRD 6.34.14): expected charges per load and their match state. */
export function FreightBillsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<BillStatus | "">("");
  const state = useLoad((signal) => listFreightBills({ matchStatus: status }, signal), `freight-bills:${status}`);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Freight bills"
        description="The charges you expect to pay per load. A carrier invoice within $5.00 or 2 % on every charge is matched; anything else is a variance to review."
        actions={
          <Link className={buttonClassName("primary")} to="/transport/freight-bills/new">
            New freight bill
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="bill-status" label="Match status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as BillStatus | "")}>
              <option value="">All</option>
              {BILL_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {statusLabel(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load freight bills" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Freight bills"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{
            title: status === "" ? "No freight bills yet" : `No ${statusLabel(status).toLowerCase()} freight bills`,
            description: "Freight bills hold the expected charges of a shipped load until the carrier invoice arrives.",
          }}
          onRowOpen={(row) => navigate(`/transport/freight-bills/${row.id}`)}
        />
      )}
    </div>
  );
}
