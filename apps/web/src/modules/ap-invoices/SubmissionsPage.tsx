import { useInfiniteQuery } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { EntityName } from "../../shell/lookup/EntityName";
import { Badge, entityText, ListPage, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { ApiError, fetchSubmissions, SUBMISSION_STATUSES, type Submission, type SubmissionStatus } from "./api/client";
import { submissionStatusLabel, submissionStatusTone } from "./parts";

const columns: ListColumn<Submission>[] = [
  { key: "invoiceNumber", header: "Vendor's invoice", mono: true },
  {
    key: "vendorId",
    header: "Vendor",
    sortable: false,
    render: (s) => <EntityName entity="vendor" id={s.vendorId ?? ""} fallback="Unknown vendor" />,
    csv: (s) => entityText("vendor", s.vendorId ?? ""),
  },
  { key: "invoiceDate", header: "Invoice date" },
  { key: "total", header: "Amount", align: "end", render: (s) => `${s.total} ${s.currency}`, csv: (s) => s.total, csvWith: "currency" },
  { key: "lines", header: "Lines", align: "end", sortable: false, render: (s) => String(s.lines.length), csv: (s) => s.lines.length },
  {
    key: "status",
    header: "Status",
    render: (s) => <Badge tone={submissionStatusTone[s.status]}>{submissionStatusLabel[s.status]}</Badge>,
    sortValue: (s) => submissionStatusLabel[s.status],
    csv: (s) => submissionStatusLabel[s.status],
  },
  { key: "createdAt", header: "Submitted", render: (s) => s.createdAt.slice(0, 10), csv: (s) => s.createdAt },
  { key: "currency", header: "Currency", defaultHidden: true },
];

const filters: ListFilterDef[] = [
  { kind: "entity", key: "vendorId", label: "Vendor", entity: "vendor" },
  { kind: "select", key: "status", label: "Status", anyLabel: "All statuses", options: SUBMISSION_STATUSES.map((s) => ({ value: s, label: submissionStatusLabel[s] })) },
];

function useSubmissionRows(query: ListQuery): ListData<Submission> {
  const { vendorId, status } = query.filters;
  const result = useInfiniteQuery({
    queryKey: ["ap-invoices", "submissions", { vendorId: vendorId ?? "", status: status ?? "" }],
    queryFn: ({ pageParam }) => fetchSubmissions({ vendorId: vendorId || undefined, status: (status as SubmissionStatus | undefined) || undefined, cursor: pageParam }),
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

/** Invoices vendors submitted against received lines in the supplier portal, for accounts payable to post or reject (#511). */
export function SubmissionsPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="ap-invoice-submissions"
      title="Invoice submissions"
      description="Invoices vendors submitted in the supplier portal against goods you received. Post each one as a vendor invoice, or reject it with a reason the vendor reads."
      noun="invoice submissions"
      singular="invoice submission"
      columns={columns}
      filters={filters}
      useRows={useSubmissionRows}
      search={(s) => ({ number: s.invoiceNumber })}
      getRowKey={(s) => s.id}
      onRowOpen={(s) => navigate(`/ap-invoices/submissions/${encodeURIComponent(s.id)}`)}
      empty={{ title: "No invoice submissions", description: "Invoices vendors submit in the supplier portal appear here." }}
    />
  );
}
