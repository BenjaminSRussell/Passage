import { useInfiniteQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { EntityName } from "../../shell/lookup/EntityName";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { ApiError, CAPTURE_STATUSES, fetchCaptures, fetchInvoices, INVOICE_STATUSES, type Capture, type CaptureStatus, type Invoice, type InvoiceStatus } from "./api/client";
import { captureStatusLabel, captureStatusTone, invoiceStatusLabel, invoiceStatusTone, reviewReasonLabel } from "./parts";
import { dueDateText, useOpenItemsForInvoices, type OpenItemLookup } from "./payment";

type StatusFilter = InvoiceStatus | CaptureStatus | "";

interface Filters {
  vendorId: string;
  status: StatusFilter;
}

const isCaptureStatus = (s: StatusFilter): s is CaptureStatus => (CAPTURE_STATUSES as readonly string[]).includes(s);
const isInvoiceStatus = (s: StatusFilter): s is InvoiceStatus => (INVOICE_STATUSES as readonly string[]).includes(s);
const statusLabel = (s: InvoiceStatus | CaptureStatus) => (isCaptureStatus(s) ? captureStatusLabel[s] : invoiceStatusLabel[s]);

function invoiceColumns(lookup: OpenItemLookup): DataTableColumn<Invoice>[] {
  return [
    { key: "invoiceNumber", header: "Invoice", render: (i) => <Link to={`/ap-invoices/${encodeURIComponent(i.id)}`}>{i.invoiceNumber}</Link> },
    { key: "vendorId", header: "Vendor", render: (i) => <EntityName entity="vendor" id={i.vendorId} fallback="Unknown vendor" /> },
    { key: "invoiceDate", header: "Invoice date" },
    { key: "dueDate", header: "Due date", render: (i) => dueDateText(i, lookup) },
    { key: "total", header: "Amount", align: "end", render: (i) => `${i.total} ${i.currency}` },
    { key: "status", header: "Status", render: (i) => <Badge tone={invoiceStatusTone[i.status]}>{invoiceStatusLabel[i.status]}</Badge> },
  ];
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

export function InvoicesPage() {
  const [draft, setDraft] = useState<Filters>({ vendorId: "", status: "" });
  const [applied, setApplied] = useState<Filters>(draft);
  const showInvoices = applied.status === "" || isInvoiceStatus(applied.status);
  const showCaptures = applied.vendorId === "" && (applied.status === "" || isCaptureStatus(applied.status));

  const result = useInfiniteQuery({
    queryKey: ["ap-invoices", "invoices", applied],
    queryFn: ({ pageParam }) => fetchInvoices({ vendorId: applied.vendorId || undefined, status: isInvoiceStatus(applied.status) ? applied.status : undefined, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
    enabled: showInvoices,
  });
  const captures = useInfiniteQuery({
    queryKey: ["ap-invoices", "captures", applied.status],
    queryFn: ({ pageParam }) => fetchCaptures({ status: isCaptureStatus(applied.status) ? applied.status : undefined, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
    enabled: showCaptures,
  });
  const items = result.data?.pages.flatMap((p) => p.items) ?? [];
  const captured = captures.data?.pages.flatMap((p) => p.items) ?? [];
  const lookup = useOpenItemsForInvoices(items);
  const forbidden = result.error instanceof ApiError && result.error.status === 403;
  const capturesForbidden = captures.error instanceof ApiError && captures.error.status === 403;

  const filtered = applied.vendorId !== "" || applied.status !== "";
  const clearFilters = () => {
    const none: Filters = { vendorId: "", status: "" };
    setDraft(none);
    setApplied(none);
  };

  function apply(event: FormEvent) {
    event.preventDefault();
    setApplied(draft);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Vendor invoices"
        description="Vendor invoices that have been posted, held for audit or rejected after audit, and documents captured but not yet posted."
        actions={
          <Link className={buttonClassName("primary")} to="/ap-invoices/post">
            Post vendor invoice
          </Link>
        }
      />
      <form className={styles.filters} onSubmit={apply} noValidate aria-label="Filter vendor invoices">
        <FormField id="invoices-vendor" label="Vendor" optional>
          {(c) => <EntityLookup entity="vendor" {...c} value={draft.vendorId} onChange={(e) => setDraft({ ...draft, vendorId: e.target.value })} />}
        </FormField>
        <FormField id="invoices-status" label="Status" optional>
          {(c) => (
            <Select {...c} value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as StatusFilter })}>
              <option value="">All statuses</option>
              {[...INVOICE_STATUSES, ...CAPTURE_STATUSES].map((s) => (
                <option key={s} value={s}>
                  {statusLabel(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button type="submit">Apply filters</Button>
      </form>
      {!showInvoices ? null : result.isError ? (
        <Alert
          tone="danger"
          title={forbidden ? "You can't view vendor invoices" : "Could not load the vendor invoices"}
          live="alert"
          action={forbidden ? undefined : <Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}
        >
          <ProblemMessage problem={result.error} />
        </Alert>
      ) : !result.isPending && items.length === 0 ? (
        showCaptures && captured.length > 0 ? null : (
          <EmptyState
            title={filtered ? "No vendor invoices match these filters" : "No vendor invoices"}
            description={filtered ? "Try a different vendor or status." : "Invoices appear here once they are posted against goods you have received."}
            action={
              filtered ? (
                <Button onClick={clearFilters}>Clear filters</Button>
              ) : (
                <Link className={buttonClassName("primary")} to="/ap-invoices/grir">
                  See goods waiting for an invoice
                </Link>
              )
            }
          />
        )
      ) : (
        <DataTable label="Vendor invoices" columns={invoiceColumns(lookup)} rows={items} getRowKey={(i) => i.id} loading={result.isPending} />
      )}
      {showInvoices && result.hasNextPage ? (
        <div className={styles.filters}>
          <Button loading={result.isFetchingNextPage} onClick={() => void result.fetchNextPage()}>
            Load more invoices
          </Button>
        </div>
      ) : null}
      {showCaptures && captures.isError && !capturesForbidden ? (
        <Alert
          tone="warning"
          title="Could not load the captured invoices"
          live="alert"
          action={<Button size="sm" onClick={() => void captures.refetch()}>Try loading captured invoices again</Button>}
        >
          <ProblemMessage problem={captures.error} />
        </Alert>
      ) : null}
      {showCaptures && captured.length > 0 ? (
        <Card title="Captured, not yet posted">
          <p>These documents were read but posted nothing. Check them, then post them as vendor invoices.</p>
          <DataTable label="Captured vendor invoices" columns={captureColumns} rows={captured} getRowKey={(c) => c.id} />
          {captures.hasNextPage ? (
            <Button loading={captures.isFetchingNextPage} onClick={() => void captures.fetchNextPage()}>
              Load more captured invoices
            </Button>
          ) : null}
        </Card>
      ) : null}
      {!showInvoices && !showCaptures ? (
        <EmptyState title="Captured invoices have no vendor yet" description="A captured document is not tied to a vendor until it is posted. Clear the vendor filter to see it." action={<Button onClick={clearFilters}>Clear filters</Button>} />
      ) : null}
      {!showInvoices && showCaptures && !captures.isPending && !captures.isError && captured.length === 0 ? (
        <EmptyState title="No captured invoices match this filter" description="Try a different status." action={<Button onClick={clearFilters}>Clear filters</Button>} />
      ) : null}
    </div>
  );
}
