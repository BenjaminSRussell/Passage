import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { buttonClassName, entityText, ListPage, StatusPill, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { BillingApiError, CONTRACT_STATUSES, listContracts, METHODS, type ContractStatus, type RecognitionMethod, type RevenueContractSummary } from "./api/client";
import { CONTRACT_STATUS_LABELS, METHOD_LABELS } from "./format";
import { usePaged } from "./usePaged";
import { EntityName } from "../../shell/lookup/EntityName";

const LIMIT = 100;

const statusText = (c: RevenueContractSummary) => CONTRACT_STATUS_LABELS[c.status] ?? c.status;

const columns: ListColumn<RevenueContractSummary>[] = [
  { key: "contractDate", header: "Contract date", mono: true },
  { key: "status", header: "Status", render: (c) => <StatusPill status={statusText(c)} />, sortValue: statusText, csv: statusText },
  { key: "customerId", header: "Customer", sortable: false, render: (row) => <EntityName entity="customer" id={row.customerId} />, csv: (row) => entityText("customer", row.customerId) },
  { key: "orderId", header: "Order", mono: true, defaultHidden: true },
  { key: "transactionPrice", header: "Price", mono: true, align: "end", render: (c) => `${c.transactionPrice} ${c.currency}`, sortValue: (c) => c.transactionPrice, csv: (c) => c.transactionPrice, csvWith: "currency" },
  { key: "recognized", header: "Recognized", mono: true, align: "end", render: (c) => `${c.recognized} ${c.currency}`, sortValue: (c) => c.recognized, csv: (c) => c.recognized, csvWith: "currency" },
  { key: "remaining", header: "Remaining", mono: true, align: "end", render: (c) => `${c.remaining} ${c.currency}`, sortValue: (c) => c.remaining, csv: (c) => c.remaining, csvWith: "currency" },
  { key: "currency", header: "Currency", defaultHidden: true },
];

const filters: ListFilterDef[] = [
  { kind: "select", key: "status", label: "Status", anyLabel: "Any status", options: CONTRACT_STATUSES.map((s) => ({ value: s, label: CONTRACT_STATUS_LABELS[s] })) },
  { kind: "select", key: "method", label: "Obligation method", anyLabel: "Any method", options: METHODS.map((m) => ({ value: m, label: METHOD_LABELS[m] })) },
  { kind: "entity", key: "customerId", label: "Customer", entity: "customer" },
];

/** Contracts newest first over the service cursor; status, customer and obligation method filter on the service. */
function useContractRows(query: ListQuery): ListData<RevenueContractSummary> {
  const status = (query.filters.status ?? "") as ContractStatus | "";
  const method = (query.filters.method ?? "") as RecognitionMethod | "";
  const customerId = query.filters.customerId ?? "";
  const [forbidden, setForbidden] = useState(false);
  const state = usePaged(async (cursor, signal) => {
    try {
      const page = await listContracts({ status, customerId, method, limit: LIMIT, cursor }, signal);
      if (cursor === undefined && !signal?.aborted) setForbidden(false);
      return page;
    } catch (error) {
      if (cursor === undefined && !signal?.aborted) setForbidden(error instanceof BillingApiError && error.status === 403);
      throw error;
    }
  }, `${status}|${customerId}|${method}`);
  return {
    rows: state.error ? [] : state.rows,
    loading: state.loading,
    loadingMore: state.loadingMore,
    error: state.error ?? state.moreError ?? undefined,
    forbidden,
    hasMore: state.hasMore && !state.moreError,
    loadMore: state.loadMore,
    retry: state.error ? state.reload : state.loadMore,
  };
}

/**
 * Revenue contracts inquiry (§5.29.14): price, recognized and remaining per contract, newest first,
 * filtered by status, customer and obligation method and paged by the service cursor.
 */
export function RevenueContractsPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="billing-revenue-contracts"
      title="Revenue contracts"
      description="Contracts with their standalone selling price allocation and how much revenue is recognized. Open one to see its schedules and satisfy milestones."
      noun="revenue contracts"
      singular="revenue contract"
      actions={
        <>
          <Link className={buttonClassName("secondary")} to="/billing/deferred-revenue">
            Deferred revenue
          </Link>
          <Link className={buttonClassName("primary")} to="/billing/revenue-contracts/new">
            New revenue contract
          </Link>
        </>
      }
      columns={columns}
      filters={filters}
      useRows={useContractRows}
      getRowKey={(c) => c.id}
      onRowOpen={(c) => navigate(`/billing/revenue-contracts/${c.id}`)}
      empty={{
        title: "No revenue contracts yet",
        description: "Create a revenue contract to allocate a bundle's price across its obligations and plan when the revenue is recognized.",
        action: (
          <Link className={buttonClassName("secondary")} to="/billing/revenue-contracts/new">
            New revenue contract
          </Link>
        ),
      }}
    />
  );
}
