import { Link, useNavigate } from "react-router-dom";
import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, Input, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { listRebateAgreements, type RebateAgreement, type RebatePartyRole, type RebateScopeFilter } from "./api/client";
import styles from "./pages.module.css";
import { UUID } from "./format";
import { basisLabel, describeProductScope, describeRebateCustomer, describeTiers, PARTY_LABELS, PERIOD_LABELS } from "./rebateFormat";
import { useLoad } from "./useLoad";

type ScopeKind = "" | "product" | "group";

const columns: DataTableColumn<RebateAgreement>[] = [
  { key: "name", header: "Agreement" },
  { key: "partyRole", header: "Type", render: (row) => <Badge tone={row.partyRole === "VENDOR" ? "info" : "neutral"}>{PARTY_LABELS[row.partyRole ?? "CUSTOMER"]}</Badge> },
  { key: "customerId", header: "Customer or vendor", mono: true, render: describeRebateCustomer },
  { key: "productId", header: "Products", render: describeProductScope },
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
  const [scopeKind, setScopeKind] = useState<ScopeKind>("");
  const [scopeId, setScopeId] = useState("");
  const [applied, setApplied] = useState<RebateScopeFilter>({});
  const [scopeError, setScopeError] = useState("");
  const state = useLoad((signal) => listRebateAgreements("", signal, party, applied), `${party || "all"}|${applied.productId ?? ""}|${applied.productGroupId ?? ""}`);

  function applyScope(event: FormEvent) {
    event.preventDefault();
    const id = scopeId.trim();
    if (scopeKind === "") {
      setScopeError("");
      setApplied({});
    } else if (!UUID.test(id)) {
      setScopeError(scopeKind === "product" ? "Enter the product id as a UUID." : "Enter the product group id as a UUID.");
    } else {
      setScopeError("");
      setApplied(scopeKind === "product" ? { productId: id } : { productGroupId: id });
    }
  }

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
        <form className={styles.toolbar} onSubmit={applyScope} noValidate>
          <FormField id="rb-scope-kind" label="Limited to">
            {(c) => (
              <Select {...c} value={scopeKind} onChange={(e) => setScopeKind(e.target.value as ScopeKind)}>
                <option value="">Any product scope</option>
                <option value="product">One product</option>
                <option value="group">One product group</option>
              </Select>
            )}
          </FormField>
          {scopeKind !== "" ? (
            <FormField id="rb-scope-id" label={scopeKind === "product" ? "Product id" : "Product group id"} error={scopeError || undefined}>
              {(c) => <Input {...c} className={styles.mono} value={scopeId} onChange={(e) => setScopeId(e.target.value)} invalid={Boolean(scopeError)} />}
            </FormField>
          ) : null}
          <Button type="submit">Apply</Button>
        </form>
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
          empty={
            applied.productId || applied.productGroupId
              ? { title: "No agreements for that scope", description: "No rebate agreement is limited to exactly this product or product group. Agreements without a product scope are not listed here." }
              : { title: "No rebate agreements", description: "Create an agreement to start accruing rebates on posted invoices." }
          }
          onRowOpen={(row) => navigate(`/pricing/rebates/${row.id}`)}
        />
      )}
      {state.data && state.data.length >= 100 ? <p className={styles.note}>Showing the first 100 agreements.</p> : null}
    </div>
  );
}
