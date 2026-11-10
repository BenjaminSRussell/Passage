import { useState } from "react";
import { Link } from "react-router-dom";
import { Badge, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listReconciliations, RECONCILIATION_STATUSES, type Reconciliation, type ReconciliationStatus } from "./api/client";
import { signed } from "./format";
import { ProblemAlert } from "./parts";
import { reconciliationStatusLabels, reconciliationTones } from "./ReconciliationPage";
import { Named } from "../purchasing/Named";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Reconciliation>[] = [
  { key: "runDate", header: "Run date", mono: true, render: (row) => <Link to={`/inventory/reconciliations/${row.id}`}>{row.runDate}</Link> },
  { key: "legalEntityId", header: "Legal entity", render: (row) => <Named entity="legalEntity" id={row.legalEntityId} /> },
  { key: "account", header: "Account", mono: true },
  { key: "inventoryValue", header: "Inventory value", align: "end", mono: true, render: (row) => `${row.inventoryValue} ${row.currency}` },
  { key: "ledgerBalance", header: "Ledger balance", align: "end", mono: true, render: (row) => `${row.ledgerBalance} ${row.currency}` },
  { key: "diff", header: "Difference", align: "end", mono: true, render: (row) => `${signed(row.diff)} ${row.currency}` },
  { key: "status", header: "Status", render: (row) => <Badge tone={reconciliationTones[row.status]}>{reconciliationStatusLabels[row.status]}</Badge> },
  { key: "negativeRows", header: "Negative rows", align: "end", mono: true, render: (row) => String(row.negativeRows) },
];

/** Inventory-to-ledger reconciliation report (FR-16.12, PRD 3.16.14), newest run date first. */
export function ReconciliationsPage() {
  const [status, setStatus] = useState<ReconciliationStatus | "">("");
  const load = useLoad((signal) => listReconciliations({ status }, signal), status);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Inventory reconciliation"
        description="Each run compares the on-hand value of a legal entity with the ledger balance of the inventory account. A difference means stock and ledger disagree and needs investigation."
        actions={
          <Link className={buttonClassName("primary")} to="/inventory/reconciliations/new">
            Run reconciliation
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="reconciliation-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as ReconciliationStatus | "")}>
              <option value="">All</option>
              {RECONCILIATION_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {reconciliationStatusLabels[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {load.error ? (
        <ProblemAlert title="Could not load reconciliation runs" problem={load.error} onRetry={load.reload} />
      ) : (
        <DataTable
          label="Reconciliation runs"
          columns={columns}
          rows={load.data ?? []}
          getRowKey={(row) => row.id}
          loading={load.loading}
          empty={{
            title: status ? "No runs with this status" : "No reconciliation runs yet",
            description: "Run a reconciliation to compare inventory value with the ledger.",
          }}
        />
      )}
    </div>
  );
}
