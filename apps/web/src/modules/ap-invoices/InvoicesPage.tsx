import { useInfiniteQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, entityText, FormField, ListPage, Select, type DataTableColumn, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { ApiError, CAPTURE_STATUSES, fetchCaptures, fetchInvoices, INVOICE_STATUSES, type Capture, type CaptureStatus, type Invoice, type InvoiceStatus } from "./api/client";
import { captureStatusLabel, captureStatusTone, invoiceStatusLabel, invoiceStatusTone, reviewReasonLabel } from "./parts";
import { dueDateText, useOpenItemsForInvoices } from "./payment";

type InvoiceRow = Invoice & { dueText: string };

const columns: ListColumn<InvoiceRow>[] = [
  { key: "invoiceNumber", header: "Invoice", render: (i) => <Link to={`/ap-invoices/${encodeURIComponent(i.id)}`}>{i.invoiceNumber}</Link> },
  {
    key: "vendorId",
    header: "Vendor",
    sortable: false,
    render: (i) => <EntityName entity="vendor" id={i.vendorId} fallback="Unknown vendor" />,
    csv: (i) => entityText("vendor", i.vendorId),
  },
  { key: "invoiceDate", header: "Invoice date" },
  { key: "dueText", header: "Due date", render: (i) => i.dueText, csv: (i) => i.dueText },
  { key: "total", header: "Amount", align: "end", render: (i) => `${i.total} ${i.currency}`, csv: (i) => i.total, csvWith: "currency" },
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

function useInvoiceRows(query: ListQuery): ListData<InvoiceRow> {
  const { vendorId, status } = query.filters;
  const result = useInfiniteQuery({
    queryKey: ["ap-invoices", "invoices", { vendorId: vendorId ?? "", status: status ?? "" }],
    queryFn: ({ pageParam }) => fetchInvoices({ vendorId: vendorId || undefined, status: (status as InvoiceStatus | undefined) || undefined, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });
  const items = result.data?.pages.flatMap((p) => p.items) ?? [];
  const lookup = useOpenItemsForInvoices(items);
  return {
    rows: items.map((i) => ({ ...i, dueText: dueDateText(i, lookup) })),
    loading: result.isPending,
    loadingMore: result.isFetchingNextPage,
    error: result.isError ? result.error : undefined,
    forbidden: result.error instanceof ApiError && result.error.status === 403,
    hasMore: Boolean(result.hasNextPage),
    loadMore: () => void result.fetchNextPage(),
    retry: () => void result.refetch(),
  };
}

const captureColumns: DataTableColumn<Capture>[] = [
  { key: "invoiceNumber", header: "Invoice" },
  { key: "vendorTaxId", header: "Vendor", render: (c) => (c.vendorTaxId ? `Tax number ${c.vendorTaxId}` : "Unknown vendor") },
  { key: "invoiceDate", header: "Invoice date" },
  { key: "dueDate", header: "Due date", render: () => "Not set until posted" },
  { key: "total", header: "Amount", align: "end", render: (c) => `${c.total} ${c.currency}` },
  {
    key: "status",
    header: "Status",
    render: (c) => (
      <>
        <Badge tone={captureStatusTone[c.status]}>{captureStatusLabel[c.status]}</Badge>
        {c.reviewReasons.length > 0 ? <div>{c.reviewReasons.map((r) => reviewReasonLabel[r]).join("; ")}</div> : null}
      </>
    ),
  },
];

function CapturedDocuments() {
  const [status, setStatus] = useState<CaptureStatus | "">("");
  const result = useInfiniteQuery({
    queryKey: ["ap-invoices", "captures", status],
    queryFn: ({ pageParam }) => fetchCaptures({ status: status || undefined, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });
  const captured = result.data?.pages.flatMap((p) => p.items) ?? [];
  const forbidden = result.error instanceof ApiError && result.error.status === 403;
  if (forbidden || result.isPending || (!result.isError && status === "" && captured.length === 0)) return null;
  return (
    <Card title="Captured, not yet posted">
      <p>These documents were read but posted nothing. Check them, then post them as vendor invoices.</p>
      <FormField id="captured-status" label="Show captured documents" optional>
        {(c) => (
          <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as CaptureStatus | "")}>
            <option value="">All captured documents</option>
            {CAPTURE_STATUSES.map((s) => (
              <option key={s} value={s}>
                {captureStatusLabel[s]}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      {result.isError ? (
        <Alert tone="warning" title="Could not load the captured invoices" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try loading captured invoices again</Button>}>
          <ProblemMessage problem={result.error} />
        </Alert>
      ) : captured.length === 0 ? (
        <EmptyState title="No captured invoices match this filter" description="Try a different status." action={<Button onClick={() => setStatus("")}>Show all captured documents</Button>} />
      ) : (
        <DataTable label="Captured vendor invoices" columns={captureColumns} rows={captured} getRowKey={(c) => c.id} />
      )}
      {result.hasNextPage ? (
        <Button loading={result.isFetchingNextPage} onClick={() => void result.fetchNextPage()}>
          Load more captured invoices
        </Button>
      ) : null}
    </Card>
  );
}

export function InvoicesPage() {
  return (
    <div className={styles.stack}>
      <ListPage
        id="ap-invoices"
        title="Vendor invoices"
        description="Vendor invoices that have been posted, held for audit or rejected after audit."
        noun="vendor invoices"
        singular="vendor invoice"
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
      <CapturedDocuments />
    </div>
  );
}
