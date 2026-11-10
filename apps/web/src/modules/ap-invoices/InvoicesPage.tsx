import { useInfiniteQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { EntityName } from "../../shell/lookup/EntityName";
import { Alert, Badge, Button, buttonClassName, DataTable, EmptyState, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { ApiError, fetchInvoices, INVOICE_STATUSES, type Invoice, type InvoiceStatus } from "./api/client";
import { invoiceStatusLabel, invoiceStatusTone } from "./parts";

interface Filters {
  vendorId: string;
  status: InvoiceStatus | "";
}

const columns: DataTableColumn<Invoice>[] = [
  { key: "invoiceNumber", header: "Invoice", render: (i) => <Link to={`/ap-invoices/${encodeURIComponent(i.id)}`}>{i.invoiceNumber}</Link> },
  { key: "vendorId", header: "Vendor", render: (i) => <EntityName entity="vendor" id={i.vendorId} fallback="Unknown vendor" /> },
  { key: "invoiceDate", header: "Invoice date" },
  { key: "total", header: "Amount", align: "end", render: (i) => `${i.total} ${i.currency}` },
  { key: "status", header: "Status", render: (i) => <Badge tone={invoiceStatusTone[i.status]}>{invoiceStatusLabel[i.status]}</Badge> },
];

export function InvoicesPage() {
  const [draft, setDraft] = useState<Filters>({ vendorId: "", status: "" });
  const [applied, setApplied] = useState<Filters>(draft);

  const result = useInfiniteQuery({
    queryKey: ["ap-invoices", "invoices", applied],
    queryFn: ({ pageParam }) => fetchInvoices({ vendorId: applied.vendorId || undefined, status: applied.status || undefined, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });
  const items = result.data?.pages.flatMap((p) => p.items) ?? [];
  const forbidden = result.error instanceof ApiError && result.error.status === 403;

  function apply(event: FormEvent) {
    event.preventDefault();
    setApplied(draft);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Vendor invoices"
        description="Every vendor invoice that has been entered, with whether it is posted or held for audit."
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
            <Select {...c} value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as InvoiceStatus | "" })}>
              <option value="">All statuses</option>
              {INVOICE_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {invoiceStatusLabel[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button type="submit">Apply filters</Button>
      </form>
      {result.isError ? (
        <Alert
          tone="danger"
          title={forbidden ? "You can't view vendor invoices" : "Could not load the vendor invoices"}
          live="alert"
          action={forbidden ? undefined : <Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}
        >
          <ProblemMessage problem={result.error} />
        </Alert>
      ) : !result.isPending && items.length === 0 ? (
        <EmptyState
          title="No vendor invoices"
          description="Invoices appear here once they are posted against goods you have received."
          action={
            <Link className={buttonClassName("primary")} to="/ap-invoices/grir">
              See goods waiting for an invoice
            </Link>
          }
        />
      ) : (
        <DataTable label="Vendor invoices" columns={columns} rows={items} getRowKey={(i) => i.id} loading={result.isPending} />
      )}
      {result.hasNextPage ? (
        <div className={styles.filters}>
          <Button loading={result.isFetchingNextPage} onClick={() => void result.fetchNextPage()}>
            Load more invoices
          </Button>
        </div>
      ) : null}
    </div>
  );
}
