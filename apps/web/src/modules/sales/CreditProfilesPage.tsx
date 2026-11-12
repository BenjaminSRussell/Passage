import { Link, useNavigate } from "react-router-dom";
import { Badge, buttonClassName, ListPage, loadedRows, type DataTableColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { listCreditProfiles, type CreditProfile } from "./api/client";
import { dash, money, sentence } from "./format";
import { useLoad } from "./useLoad";
import { EntityName } from "../../shell/lookup/EntityName";
import { PlainName } from "./OrderNumber";

const columns: DataTableColumn<CreditProfile>[] = [
  { key: "customerId", header: "Customer", render: (row) => <PlainName entity="customer" id={row.customerId} /> },
  { key: "legalEntityId", header: "Legal entity", render: (row) => <EntityName entity="legalEntity" id={row.legalEntityId} /> },
  { key: "limit", header: "Credit limit", align: "end", render: (row) => money(row.limit, row.currency) },
  { key: "limitType", header: "Limit type", render: (row) => sentence(row.limitType) },
  { key: "openAr", header: "Open AR", align: "end", render: (row) => money(row.exposure.openAr ?? "0", row.currency) },
  { key: "mandatoryCheck", header: "Mandatory", render: (row) => (row.mandatoryCheck ? <Badge tone="info">Mandatory</Badge> : dash) },
  { key: "reviewDate", header: "Review date", mono: true, render: (row) => row.reviewDate ?? dash },
];

const filters: ListFilterDef[] = [
  { kind: "entity", key: "legalEntityId", label: "Legal entity", entity: "legalEntity" },
  { kind: "dateRange", key: "review", label: "Review date" },
];

/**
 * The profiles of the filter. A review date "to" asks the service for the profiles whose review is due by then, oldest
 * first (past it, the check holds orders: CR-4); a "from" keeps the later ones of those in the browser.
 */
function useProfileRows(query: ListQuery): ListData<CreditProfile> {
  const { legalEntityId = "", reviewFrom = "", reviewTo = "" } = query.filters;
  const state = useLoad((signal) => listCreditProfiles(legalEntityId, signal, reviewTo), `${legalEntityId}|${reviewTo}`);
  const rows = reviewFrom ? (state.data ?? []).filter((p) => (p.reviewDate ?? "") >= reviewFrom) : state.data;
  return loadedRows({ ...state, data: rows });
}

/** Credit profiles per customer and legal entity (PRD §5.27.14 "Credit exposure by customer"). */
export function CreditProfilesPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="sales-credit-profiles"
      title="Credit profiles"
      description="Each customer's credit limit, tolerances and exposure per legal entity. The credit check at confirmation uses these values; filter by review date to see the reviews coming due."
      actions={
        <Link className={buttonClassName("primary")} to="/sales/credit-profiles/new">
          New credit profile
        </Link>
      }
      noun="credit profiles"
      singular="credit profile"
      columns={columns}
      filters={filters}
      useRows={useProfileRows}
      getRowKey={(row) => `${row.legalEntityId}/${row.customerId}`}
      onRowOpen={(row) => navigate(`/sales/credit-profiles/${row.legalEntityId}/${row.customerId}`)}
      empty={{ title: "No credit profiles yet", description: "Without a profile, orders confirm without a credit limit unless the check is mandatory." }}
    />
  );
}
