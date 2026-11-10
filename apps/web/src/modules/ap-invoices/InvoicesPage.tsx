import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { EntityName } from "../../shell/lookup/EntityName";
import { Badge, buttonClassName, entityText, ListPage, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { ApiError, fetchInvoices, INVOICE_STATUSES, type Invoice, type InvoiceStatus } from "./api/client";
import { invoiceStatusLabel, invoiceStatusTone } from "./parts";

const columns: ListColumn<Invoice>[] = [
  { key: "invoiceNumber", header: "Invoice", render: (i) => <Link to={`/ap-invoices/${encodeURIComponent(i.id)}`}>{i.invoiceNumber}</Link> },
  {
    key: "vendorId",
    header: "Vendor",
    sortable: false,
    render: (i) => <EntityName entity="vendor" id={i.vendorId} fallback="Unknown vendor" />,
    csv: (i) => entityText("vendor", i.vendorId),
  },
  { key: "invoiceDate", header: "Invoice date" },
  { key: "total", header: "Amount", align: "end", render: (i) => `${i.total} ${i.currency}`, csv: (i) => i.total },
  { key: "currency", header: "Currency", defaultHidden: true },
  {
    key: "status",
    header: "Status",
    render: (i) => <Badge tone={invoiceStatusTone[i.status]}>{invoiceStatusLabel[i.status]}</Badge>,
    sortValue: (i) => invoiceStatusLabel[i.status],
    csv: (i) => invoiceStatusLabel[i.status],
  },
];

const filters: ListFilterDef[] = [
  { kind: "entity", key: "vendorId", label: "Vendor", entity: "vendor" },
  { kind: "select", key: "status", label: "Status", anyLabel: "All statuses", options: INVOICE_STATUSES.map((s) => ({ value: s, label: invoiceStatusLabel[s] })) },
];

function useInvoiceRows(query: ListQuery): ListData<Invoice> {
  const { vendorId, status } = query.filters;
  const result = useInfiniteQuery({
    queryKey: ["ap-invoices", "invoices", { vendorId: vendorId ?? "", status: status ?? "" }],
    queryFn: ({ pageParam }) => fetchInvoices({ vendorId: vendorId || undefined, status: (status as InvoiceStatus | undefined) || undefined, cursor: pageParam }),
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

export function InvoicesPage() {
  return (
    <ListPage
      id="ap-invoices"
      title="Vendor invoices"
      description="Vendor invoices that have been posted, held for audit or rejected after audit."
      noun="vendor invoices"
      actions={
        <Link className={buttonClassName("primary")} to="/ap-invoices/post">
          Post vendor invoice
        </Link>
      }
      columns={columns}
      filters={filters}
      useRows={useInvoiceRows}
      getRowKey={(i) => i.id}
      empty={{
        title: "No vendor invoices",
        description: "Invoices appear here once they are posted against goods you have received.",
        action: (
          <Link className={buttonClassName("primary")} to="/ap-invoices/grir">
            See goods waiting for an invoice
          </Link>
        ),
      }}
    />
  );
}
