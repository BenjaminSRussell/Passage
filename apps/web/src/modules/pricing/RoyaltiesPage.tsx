import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { listRoyaltyAgreements, type ContractStatus, type RoyaltyAgreement } from "./api/client";
import { CONTRACT_STATUS_LABELS, describeRoyaltyProduct, describeRoyaltyTiers, ROYALTY_BASIS_LABELS } from "./contractFormat";
import styles from "./pages.module.css";
import { PERIOD_LABELS } from "./rebateFormat";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<RoyaltyAgreement>[] = [
  { key: "name", header: "Agreement" },
  { key: "licensorVendorId", header: "Licensor", mono: true },
  { key: "productId", header: "Product", render: describeRoyaltyProduct },
  { key: "basis", header: "Basis", render: (row) => ROYALTY_BASIS_LABELS[row.basis] },
  { key: "tiers", header: "Tiers", mono: true, render: describeRoyaltyTiers },
  { key: "minimumRoyalty", header: "Minimum per period", mono: true, align: "end", render: (row) => `${row.minimumRoyalty} ${row.currency}` },
  { key: "period", header: "Period", render: (row) => PERIOD_LABELS[row.period] },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status.toLowerCase()} /> },
];

/** Royalty agreements (FR-26.17): a licensor earns tiered rates on a product, with a minimum royalty per period. */
export function RoyaltiesPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<ContractStatus | "">("");
  const state = useLoad((signal) => listRoyaltyAgreements({ status }, signal), status || "all");
  const rows = state.data ?? [];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Royalty agreements"
        description="A licensor earns tiered rates on net sales or quantity of a product. A period below the minimum royalty is topped up when it closes; the period is then claimed through accounts payable."
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to="/pricing/contract-claims">
              Broker and royalty claims
            </Link>
            <Link className={buttonClassName("primary")} to="/pricing/royalties/new">
              New royalty agreement
            </Link>
          </div>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="ry-status" label="Status">
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
        <Alert tone="danger" title="Could not load the royalty agreements" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Royalty agreements"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No royalty agreements", description: "Create an agreement to accrue royalties on posted invoices." }}
          onRowOpen={(row) => navigate(`/pricing/royalties/${row.id}`)}
        />
      )}
      {rows.length >= 100 ? <p className={styles.note}>Showing the first 100 agreements. Filter by status to see more.</p> : null}
    </div>
  );
}
