import { useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Button, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import {
  listOpenOrdersByVendor,
  listOverdueLines,
  listPriceHistory,
  type InquiryFilters,
  type OverdueLine,
  type PriceHistoryEntry,
  type VendorOpenOrders,
} from "./api/client";
import { LoadError, ShortId, datePattern, useResource, uuidPattern } from "./parts";
import styles from "./pages.module.css";

type Field = "vendorId" | "productId" | "asOf" | "from" | "to";
type Draft = Record<Field, string>;
type Errors = Partial<Record<Field, string>>;

const emptyDraft: Draft = { vendorId: "", productId: "", asOf: "", from: "", to: "" };

const labels: Record<Field, string> = { vendorId: "Vendor id", productId: "Product id", asOf: "As of", from: "Ordered from", to: "Ordered to" };

/** Checks the filters before the round trip. productId is required where `needsProduct` is set. */
export function validateInquiry(d: Draft, needsProduct = false): Errors {
  const errors: Errors = {};
  for (const key of ["vendorId", "productId"] as const) {
    if (d[key].trim() !== "" && !uuidPattern.test(d[key].trim())) errors[key] = `${labels[key]} must be a UUID.`;
  }
  if (needsProduct && d.productId.trim() === "") errors.productId = "Product id is required.";
  for (const key of ["asOf", "from", "to"] as const) {
    if (d[key] !== "" && !datePattern.test(d[key])) errors[key] = "Enter a date.";
  }
  if (!errors.from && !errors.to && d.from !== "" && d.to !== "" && d.to < d.from) errors.to = "The end date is before the start date.";
  return errors;
}

const toFilters = (d: Draft): InquiryFilters => {
  const opt = (v: string) => (v.trim() === "" ? undefined : v.trim());
  return { vendorId: opt(d.vendorId), productId: opt(d.productId), asOf: opt(d.asOf), from: opt(d.from), to: opt(d.to) };
};

function InquiryNav() {
  return (
    <nav aria-label="Inquiries" className={styles.actions}>
      <Link className={styles.link} to="/purchasing/inquiries">
        Open orders by vendor
      </Link>
      <Link className={styles.link} to="/purchasing/inquiries/overdue">
        Overdue lines
      </Link>
      <Link className={styles.link} to="/purchasing/inquiries/price-history">
        Price history
      </Link>
    </nav>
  );
}

interface FrameProps<T> {
  title: string;
  description: string;
  fields: Field[];
  needsProduct?: boolean;
  tableLabel: string;
  columns: DataTableColumn<T>[];
  rowKey: (row: T) => string;
  empty: { title: string; description: string };
  load: (filters: InquiryFilters, cursor: string | undefined, signal: AbortSignal) => Promise<{ items: T[]; nextCursor?: string }>;
  what: string;
  /** When set, nothing is loaded until the filters validate (price history needs a product). */
  waitForFilters?: boolean;
  extra?: (rows: T[]) => ReactNode;
}

/** The filter form, table, paging and states every inquiry shares. */
function InquiryFrame<T>(p: FrameProps<T>) {
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [applied, setApplied] = useState<Draft | null>(p.waitForFilters ? null : emptyDraft);
  const [errors, setErrors] = useState<Errors>({});
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<T[]>([]);
  const page = useResource(
    async (signal) => {
      if (!applied) return { items: [] as T[] };
      const result = await p.load(toFilters(applied), cursor, signal);
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [applied, cursor],
  );
  const apply = () => {
    const found = validateInquiry(draft, p.needsProduct);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setCursor(undefined);
    setApplied(draft);
  };
  return (
    <div className={styles.stack}>
      <PageHeader title={p.title} description={p.description} />
      <InquiryNav />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          apply();
        }}
      >
        {p.fields.map((f) => (
          <div className={styles.filter} key={f}>
            <FormField id={`inq-${f}`} label={labels[f]} optional={!(f === "productId" && p.needsProduct)} error={errors[f]}>
              {(c) => (
                <Input
                  {...c}
                  type={f === "asOf" || f === "from" || f === "to" ? "date" : "text"}
                  value={draft[f]}
                  onChange={(e) => setDraft((d) => ({ ...d, [f]: e.target.value }))}
                />
              )}
            </FormField>
          </div>
        ))}
        <div className={styles.filter}>
          <Button type="submit">Show</Button>
        </div>
      </form>
      {page.error ? <LoadError what={p.what} error={page.error} onRetry={page.reload} /> : null}
      <DataTable
        label={p.tableLabel}
        columns={p.columns}
        rows={page.error ? [] : rows}
        getRowKey={p.rowKey}
        loading={page.loading && rows.length === 0}
        empty={p.empty}
      />
      {page.data?.nextCursor ? (
        <div className={styles.actions}>
          <Button loading={page.loading} onClick={() => setCursor(page.data?.nextCursor)}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

const money = (amount: string, currency: string) => `${amount} ${currency}`;

/** Open orders by vendor (PRD 3.17.14): what each vendor still owes, and how much of it is late. */
export function OpenOrdersByVendorPage() {
  const columns: DataTableColumn<VendorOpenOrders>[] = [
    { key: "vendorId", header: "Vendor", render: (v) => <ShortId id={v.vendorId} /> },
    { key: "orderCount", header: "Orders", align: "end" },
    { key: "lineCount", header: "Open lines", align: "end" },
    { key: "openValue", header: "Open value", align: "end", render: (v) => money(v.openValue, v.currency) },
    { key: "overdueLines", header: "Overdue lines", align: "end" },
    { key: "overdueValue", header: "Overdue value", align: "end", render: (v) => money(v.overdueValue, v.currency) },
    { key: "earliestDue", header: "Earliest due", mono: true },
  ];
  return (
    <InquiryFrame<VendorOpenOrders>
      title="Open orders by vendor"
      description="Confirmed purchase orders with quantity still to receive, by vendor and currency. A line is due on the date the vendor confirmed, else the requested date; overdue means due before the as-of date."
      fields={["vendorId", "asOf"]}
      tableLabel="Open orders by vendor"
      columns={columns}
      rowKey={(v) => `${v.vendorId}:${v.currency}`}
      empty={{ title: "No open orders", description: "Confirmed purchase orders appear here until they are received in full." }}
      load={(f, cursor, signal) => listOpenOrdersByVendor(f, { cursor, signal })}
      what="open orders"
    />
  );
}

/** Overdue purchase order lines (PRD 3.17.14), oldest first. */
export function OverdueLinesPage() {
  const columns: DataTableColumn<OverdueLine>[] = [
    {
      key: "po",
      header: "Purchase order",
      render: (l) => (
        <Link className={styles.link} to={`/purchasing/purchase-orders/${l.purchaseOrderId}`}>
          {l.number}
        </Link>
      ),
    },
    { key: "lineNo", header: "Line", align: "end" },
    { key: "vendorId", header: "Vendor", render: (l) => <ShortId id={l.vendorId} /> },
    { key: "item", header: "Item", render: (l) => l.description || <ShortId id={l.productId} /> },
    { key: "openQty", header: "Open quantity", align: "end", render: (l) => `${l.openQty} ${l.unit}` },
    { key: "openValue", header: "Open value", align: "end", render: (l) => money(l.openValue, l.currency) },
    { key: "dueDate", header: "Due", mono: true },
    { key: "daysLate", header: "Days late", align: "end" },
    { key: "deliveryDate", header: "Requested", mono: true },
    { key: "confirmedDate", header: "Vendor confirmed", mono: true, render: (l) => l.confirmedDate ?? "—" },
  ];
  return (
    <InquiryFrame<OverdueLine>
      title="Overdue lines"
      description="Open lines of confirmed purchase orders due before the as-of date, oldest first. A line is due on the date the vendor confirmed, else the requested date."
      fields={["vendorId", "productId", "asOf"]}
      tableLabel="Overdue order lines"
      columns={columns}
      rowKey={(l) => `${l.purchaseOrderId}:${l.lineNo}`}
      empty={{ title: "Nothing overdue", description: "No open line is past its due date." }}
      load={(f, cursor, signal) => listOverdueLines(f, { cursor, signal })}
      what="overdue lines"
    />
  );
}

/** Order price history of a product (PRD 3.17.14, ME1P). */
export function PriceHistoryPage() {
  const columns: DataTableColumn<PriceHistoryEntry>[] = [
    { key: "orderDate", header: "Ordered", mono: true },
    {
      key: "po",
      header: "Purchase order",
      render: (e) => (
        <Link className={styles.link} to={`/purchasing/purchase-orders/${e.purchaseOrderId}`}>
          {e.number}
        </Link>
      ),
    },
    { key: "vendorId", header: "Vendor", render: (e) => <ShortId id={e.vendorId} /> },
    { key: "qty", header: "Quantity", align: "end", render: (e) => `${e.qty} ${e.unit}` },
    { key: "unitPrice", header: "Unit price", align: "end", render: (e) => `${money(e.unitPrice, e.currency)} per ${e.priceUnit}` },
    { key: "discountPct", header: "Discount %", align: "end" },
    { key: "priceSource", header: "Price source" },
  ];
  return (
    <InquiryFrame<PriceHistoryEntry>
      title="Order price history"
      description="What a product was ordered at on approved purchase orders, newest first."
      fields={["productId", "vendorId", "from", "to"]}
      needsProduct
      waitForFilters
      tableLabel="Order price history"
      columns={columns}
      rowKey={(e) => `${e.purchaseOrderId}:${e.lineNo}`}
      empty={{ title: "No price history", description: "Enter a product id to see the prices it was ordered at." }}
      load={(f, cursor, signal) => listPriceHistory(f, { cursor, signal })}
      what="price history"
    />
  );
}
