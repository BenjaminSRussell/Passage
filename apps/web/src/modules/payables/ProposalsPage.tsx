import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { useWorkContext } from "../../shell/lookup/workContextState";
import { usePrincipalDuties } from "../../shell/principal";
import { buttonClassName, ListPage, StatusPill, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { ApiError } from "../../shell/api/http";
import { listProposals, PROPOSAL_STATUSES, type ProposalStatus, type ProposalSummary } from "./api/client";
import { formatAmount, METHOD_LABELS, personLabel, PROPOSAL_LABELS } from "./format";

const baseColumns: ListColumn<ProposalSummary>[] = [
  { key: "runDate", header: "Payment date", mono: true },
  { key: "dueBy", header: "Invoices due by", mono: true },
  { key: "paymentMethod", header: "Method", render: (p) => METHOD_LABELS[p.paymentMethod], sortValue: (p) => METHOD_LABELS[p.paymentMethod], csv: (p) => METHOD_LABELS[p.paymentMethod] },
  { key: "status", header: "Status", render: (p) => <StatusPill status={PROPOSAL_LABELS[p.status]} />, sortValue: (p) => PROPOSAL_LABELS[p.status], csv: (p) => PROPOSAL_LABELS[p.status] },
  { key: "totalValued", header: "Total", align: "end", mono: true, render: (p) => formatAmount(p.totalValued, p.legalEntityCurrency), sortValue: (p) => Number(p.totalValued), csv: (p) => p.totalValued, csvWith: "legalEntityCurrency" },
];

const filters: ListFilterDef[] = [
  { kind: "select", key: "status", label: "Status", anyLabel: "Any status", options: PROPOSAL_STATUSES.map((s) => ({ value: s, label: PROPOSAL_LABELS[s] })) },
];

/** Proposals of the working legal entity, newest first, a page at a time. */
function useProposalRows(query: ListQuery): ListData<ProposalSummary> {
  const { legalEntityId } = useWorkContext();
  const status = (query.filters.status ?? "") as ProposalStatus | "";
  const result = useInfiniteQuery({
    queryKey: ["payables", "proposals", legalEntityId, status],
    queryFn: ({ pageParam }) => listProposals({ legalEntityId, status, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });
  return {
    rows: result.data?.pages.flatMap((p) => p.items) ?? [],
    loading: result.isPending,
    loadingMore: result.isFetchingNextPage,
    error: result.isError ? result.error : undefined,
    forbidden: result.error instanceof ApiError && result.error.status === 403,
    hasMore: Boolean(result.hasNextPage),
    loadMore: () => void result.fetchNextPage(),
    retry: () => void result.refetch(),
  };
}

export function ProposalsPage() {
  const navigate = useNavigate();
  const { identity } = useWorkContext();
  const columns: ListColumn<ProposalSummary>[] = [
    ...baseColumns,
    { key: "createdBy", header: "Created by", render: (p) => personLabel(p.createdBy, identity), sortValue: (p) => personLabel(p.createdBy, identity), csv: (p) => personLabel(p.createdBy, identity) },
  ];
  const canCreate = holdsDuty(usePrincipalDuties(), "payment-proposal.write");
  return (
    <ListPage
      id="payables-proposals"
      title="Payment proposals"
      description="Choose which vendor invoices to pay, have a second person approve the list, then pay."
      actions={
        <>
          <Link className={buttonClassName("secondary")} to="/payables/open-items">
            Vendor open items
          </Link>
          {canCreate ? (
            <Link className={buttonClassName("primary")} to="/payables/proposals/new">
              New payment proposal
            </Link>
          ) : null}
        </>
      }
      noun="payment proposals"
      singular="payment proposal"
      columns={columns}
      filters={filters}
      useRows={useProposalRows}
      getRowKey={(p) => p.id}
      onRowOpen={(p) => navigate(`/payables/proposals/${p.id}`)}
      empty={{ title: "No payment proposals yet", description: "Create a proposal to select the vendor invoices that are due and prepare a payment." }}
    />
  );
}
