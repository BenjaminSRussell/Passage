import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { listBrokerContracts, type BrokerContract, type ContractStatus } from "./api/client";
import { CONTRACT_STATUS_LABELS, describeBrokerRate, describeBrokerScope } from "./contractFormat";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<BrokerContract>[] = [
  { key: "name", header: "Contract" },
  { key: "brokerVendorId", header: "Broker", mono: true },
  { key: "rate", header: "Rate", mono: true, render: describeBrokerRate },
  { key: "customerId", header: "Scope", render: describeBrokerScope },
  { key: "validFrom", header: "Validity", mono: true, render: (row) => `${row.validFrom} – ${row.validTo}` },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status.toLowerCase()} /> },
];

/** Broker contracts (FR-26.16): a broker earns a rate on the invoices of its customer and product scope. */
export function BrokersPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<ContractStatus | "">("");
  const state = useLoad((signal) => listBrokerContracts({ status }, signal), status || "all");
  const rows = state.data ?? [];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Broker contracts"
        description="A broker earns a percentage of net or an amount per unit on posted invoice lines in its scope. Accruals are claimed per period and paid through accounts payable."
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to="/pricing/contract-claims">
              Broker and royalty claims
            </Link>
            <Link className={buttonClassName("primary")} to="/pricing/brokers/new">
              New broker contract
            </Link>
          </div>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="bk-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as ContractStatus | "")}>
              <option value="">Any status</option>
              <option value="ACTIVE">{CONTRACT_STATUS_LABELS.ACTIVE}</option>
              <option value="CLOSED">{CONTRACT_STATUS_LABELS.CLOSED}</option>
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the broker contracts" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Broker contracts"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No broker contracts", description: "Create a contract to accrue broker commission on posted invoices." }}
          onRowOpen={(row) => navigate(`/pricing/brokers/${row.id}`)}
        />
      )}
      {rows.length >= 100 ? <p className={styles.note}>Showing the first 100 contracts. Filter by status to see more.</p> : null}
    </div>
  );
}
