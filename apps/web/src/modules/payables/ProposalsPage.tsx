import { useInfiniteQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { holdsDuty } from "../../shell/dutyGate";
import { useWorkContext } from "../../shell/lookup/workContextState";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, DataTable, FormField, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { listProposals, PROPOSAL_STATUSES, type ProposalStatus, type ProposalSummary } from "./api/client";
import { formatAmount, METHOD_LABELS, personLabel, PROPOSAL_LABELS } from "./format";

const baseColumns: DataTableColumn<ProposalSummary>[] = [
  { key: "runDate", header: "Payment date", mono: true },
  { key: "dueBy", header: "Invoices due by", mono: true },
  { key: "paymentMethod", header: "Method", render: (p) => METHOD_LABELS[p.paymentMethod] },
  { key: "status", header: "Status", render: (p) => <StatusPill status={PROPOSAL_LABELS[p.status]} /> },
  { key: "totalValued", header: "Total", align: "end", mono: true, render: (p) => formatAmount(p.totalValued, p.legalEntityCurrency) },
  ];

export function ProposalsPage() {
  const navigate = useNavigate();
  const { legalEntityId, identity } = useWorkContext();
  const columns = [...baseColumns, { key: "createdBy", header: "Created by", render: (p: ProposalSummary) => personLabel(p.createdBy, identity) }];
  const canCreate = holdsDuty(usePrincipalDuties(), "payment-proposal.write");
  const [status, setStatus] = useState<ProposalStatus | "">("");

  const result = useInfiniteQuery({
    queryKey: ["payables", "proposals", legalEntityId, status],
    queryFn: ({ pageParam }) => listProposals({ legalEntityId, status, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });
  const items = result.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <div className={styles.stack}>
      <PageHeader
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
      />
      <div className={styles.filters} role="group" aria-label="Filter proposals">
        <FormField id="pp-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as ProposalStatus | "")}>
              <option value="">Any status</option>
              {PROPOSAL_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {PROPOSAL_LABELS[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {result.isError ? (
        <Alert tone="danger" title="Could not load the payment proposals" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          <ProblemMessage problem={result.error} />
        </Alert>
      ) : (
        <DataTable
          label="Payment proposals"
          columns={columns}
          rows={items}
          getRowKey={(p) => p.id}
          loading={result.isPending}
          empty={{ title: "No payment proposals yet", description: "Create a proposal to select the vendor invoices that are due and prepare a payment." }}
          onRowOpen={(p) => navigate(`/payables/proposals/${p.id}`)}
        />
      )}
      {result.hasNextPage ? (
        <div className={styles.filters}>
          <Button loading={result.isFetchingNextPage} onClick={() => void result.fetchNextPage()}>
            Load more proposals
          </Button>
        </div>
      ) : null}
    </div>
  );
}
