import { useInfiniteQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { Alert, Badge, Button, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { fetchGrir, GRIR_STATUSES, type GrirItem, type GrirStatus } from "./api/client";
import { subtractDecimal, UUID, type InvoiceNavState } from "./parts";
import styles from "../receiving/pages.module.css";

const statusTone: Record<GrirStatus, BadgeTone> = { OPEN: "warning", PARTIAL: "info", CLEARED: "success", WRITTEN_OFF: "neutral" };
const statusLabel = (s: GrirStatus) => s.charAt(0) + s.slice(1).toLowerCase().replace("_", " ");

export interface GrirFilters {
  vendorId: string;
  status: GrirStatus | "";
  ageGt: string;
}

export function validateFilters(f: GrirFilters): Record<string, string> {
  const errors: Record<string, string> = {};
  if (f.vendorId !== "" && !UUID.test(f.vendorId)) errors.vendorId = "Enter a vendor UUID.";
  if (f.ageGt !== "" && (!/^\d+$/.test(f.ageGt) || Number(f.ageGt) > 3650)) errors.ageGt = "Enter a number of days from 0 to 3650.";
  return errors;
}

const isInvoiceable = (g: GrirItem) => g.status === "OPEN" || g.status === "PARTIAL";

/** GR/IR open items: received goods not yet fully invoiced, with the open amount per currency. */
export function GrirPage() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<GrirFilters>({ vendorId: "", status: "", ageGt: "" });
  const [applied, setApplied] = useState<GrirFilters>(draft);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [selectionError, setSelectionError] = useState<string | null>(null);

  const result = useInfiniteQuery({
    queryKey: ["ap-invoices", "grir", applied],
    queryFn: ({ pageParam }) =>
      fetchGrir({ vendorId: applied.vendorId || undefined, status: applied.status || undefined, ageGt: applied.ageGt === "" ? undefined : Number(applied.ageGt), cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });
  const items = result.data?.pages.flatMap((p) => p.items) ?? [];
  const totals = result.data?.pages[0]?.totals;

  function apply(event: FormEvent) {
    event.preventDefault();
    const found = validateFilters(draft);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setSelected(new Set());
    setSelectionError(null);
    setApplied(draft);
  }

  function toggle(id: string, on: boolean) {
    setSelectionError(null);
    setSelected((prev) => {
      const next = new Set(prev);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  }

  function invoiceSelected() {
    const chosen = items.filter((g) => selected.has(g.id));
    if (new Set(chosen.map((g) => g.vendorId)).size > 1 || new Set(chosen.map((g) => g.currency)).size > 1) {
      setSelectionError("Select items from one vendor and one currency to put on the same invoice.");
      return;
    }
    const state: InvoiceNavState = {
      items: chosen.map((g) => ({
        vendorId: g.vendorId,
        currency: g.currency,
        receiptId: g.receiptId,
        receiptLineNo: g.lineNo,
        qty: subtractDecimal(g.receivedQty, g.invoicedQty),
        unitPrice: g.unitPrice,
      })),
    };
    navigate("/ap-invoices/post", { state });
  }

  const columns: DataTableColumn<GrirItem>[] = [
    {
      key: "select",
      header: "Select",
      render: (g) => (
        <Input type="checkbox" aria-label={`Select ${g.receiptNumber} line ${g.lineNo}`} checked={selected.has(g.id)} disabled={!isInvoiceable(g)} onChange={(e) => toggle(g.id, e.target.checked)} />
      ),
    },
    { key: "receiptNumber", header: "Receipt", mono: true },
    { key: "lineNo", header: "Line", align: "end" },
    { key: "vendorId", header: "Vendor", mono: true },
    { key: "receivedQty", header: "Received", align: "end" },
    { key: "invoicedQty", header: "Invoiced", align: "end" },
    { key: "openAmount", header: "Open amount", align: "end", render: (g) => `${g.openAmount} ${g.currency}` },
    { key: "ageDays", header: "Age (days)", align: "end" },
    { key: "status", header: "Status", render: (g) => <Badge tone={statusTone[g.status]}>{statusLabel(g.status)}</Badge> },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader title="GR/IR open items" description="Goods received and not yet invoiced. Select items from one vendor to put them on a vendor invoice." />
      <form className={styles.filters} onSubmit={apply} noValidate aria-label="Filter GR/IR items">
        <FormField id="grir-vendor" label="Vendor ID" optional error={errors.vendorId}>
          {(c) => <Input {...c} className={styles.mono} value={draft.vendorId} onChange={(e) => setDraft({ ...draft, vendorId: e.target.value })} invalid={Boolean(errors.vendorId)} />}
        </FormField>
        <FormField id="grir-status" label="Status" optional>
          {(c) => (
            <Select {...c} value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as GrirStatus | "" })}>
              <option value="">All statuses</option>
              {GRIR_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {statusLabel(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="grir-age" label="Older than (days)" optional error={errors.ageGt}>
          {(c) => <Input {...c} inputMode="numeric" value={draft.ageGt} onChange={(e) => setDraft({ ...draft, ageGt: e.target.value })} invalid={Boolean(errors.ageGt)} />}
        </FormField>
        <Button type="submit">Apply filters</Button>
      </form>
      {totals ? (
        <StatGroup label="Open GR/IR totals">
          <StatTile label="Items" value={totals.count} />
          {totals.openAmount.map((t) => (
            <StatTile key={t.currency} label={`Open ${t.currency}`} value={t.amount} />
          ))}
        </StatGroup>
      ) : null}
      {result.isError ? (
        <Alert tone="danger" title="Could not load the GR/IR items" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : (
        <DataTable
          label="GR/IR open items"
          columns={columns}
          rows={items}
          getRowKey={(g) => g.id}
          loading={result.isPending}
          empty={{ title: "No GR/IR items", description: "Items appear here when a receipt is posted, and clear as vendor invoices are posted against them." }}
        />
      )}
      {selectionError ? (
        <Alert tone="warning" live="alert">
          {selectionError}
        </Alert>
      ) : null}
      <div className={styles.filters}>
        <Button variant="primary" disabled={selected.size === 0} onClick={invoiceSelected}>
          Invoice selected items
        </Button>
        {result.hasNextPage ? (
          <Button loading={result.isFetchingNextPage} onClick={() => void result.fetchNextPage()}>
            Load more items
          </Button>
        ) : null}
      </div>
    </div>
  );
}
