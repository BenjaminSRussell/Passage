import { Link, useNavigate } from "react-router-dom";
import { useState } from "react";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { listRebateAgreements, type RebateAgreement, type RebatePartyRole } from "./api/client";
import styles from "./pages.module.css";
import { basisLabel, describeRebateCustomer, describeTiers, PARTY_LABELS, PERIOD_LABELS } from "./rebateFormat";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<RebateAgreement>[] = [
  { key: "name", header: "Agreement" },
  { key: "partyRole", header: "Type", render: (row) => <Badge tone={row.partyRole === "VENDOR" ? "info" : "neutral"}>{PARTY_LABELS[row.partyRole ?? "CUSTOMER"]}</Badge> },
  { key: "customerId", header: "Customer or vendor", mono: true, render: describeRebateCustomer },
  { key: "basis", header: "Basis", render: basisLabel },
  { key: "period", header: "Period", render: (row) => PERIOD_LABELS[row.period] },
  { key: "tiers", header: "Tiers", mono: true, render: describeTiers },
  { key: "currency", header: "Currency", mono: true },
  { key: "validFrom", header: "Validity", mono: true, render: (row) => `${row.validFrom} – ${row.validTo}` },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status.toLowerCase()} /> },
];

/** Rebate agreements (PRD §5.26.6): customer rebates on posted sales invoices and vendor rebates (FR-26.15) on posted vendor invoices. */
export function RebatesPage() {
  const navigate = useNavigate();
  const [party, setParty] = useState<RebatePartyRole | "">("");
  const state = useLoad((signal) => listRebateAgreements("", signal, party), party || "all");

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Rebate agreements"
        description="Tiered customer and vendor rebates. Each posted invoice line accrues against its period; crossing a tier recomputes the period. Vendor rebates are settled by period claims."
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to="/pricing/rebates/claims">
              Vendor rebate claims
            </Link>
            <Link className={buttonClassName("primary")} to="/pricing/rebates/new">
              New rebate agreement
            </Link>
          </div>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="rb-party-filter" label="Type">
          {(c) => (
            <Select {...c} value={party} onChange={(e) => setParty(e.target.value as RebatePartyRole | "")}>
              <option value="">Customer and vendor rebates</option>
              <option value="CUSTOMER">{PARTY_LABELS.CUSTOMER}</option>
              <option value="VENDOR">{PARTY_LABELS.VENDOR}</option>
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the rebate agreements" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Rebate agreements"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No rebate agreements", description: "Create an agreement to start accruing rebates on posted invoices." }}
          onRowOpen={(row) => navigate(`/pricing/rebates/${row.id}`)}
        />
      )}
      {state.data && state.data.length >= 100 ? <p className={styles.note}>Showing the first 100 agreements.</p> : null}
    </div>
  );
}
