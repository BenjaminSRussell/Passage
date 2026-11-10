import { useState, type ReactNode } from "react";
import { Link, NavLink } from "react-router-dom";
import { Button, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import {
  listBackorderedLines,
  listChangeOrders,
  listExpiringAgreements,
  listNearLimitAgreements,
  listOpenOrdersByVendor,
  listOverdueLines,
  listPriceHistory,
  type BackorderedLine,
  type ChangeOrder,
  type ExpiringAgreement,
  type InquiryFilters,
  type NearLimitLine,
  type OverdueLine,
  type PriceHistoryEntry,
  type VendorOpenOrders,
} from "./api/client";
import { LoadError, datePattern, useResource, uuidPattern, Named } from "./parts";
import styles from "./pages.module.css";

type Field = "vendorId" | "productId" | "asOf" | "from" | "to" | "withinDays" | "thresholdPct";
type Draft = Record<Field, string>;
type Errors = Partial<Record<Field, string>>;

const emptyDraft: Draft = { vendorId: "", productId: "", asOf: "", from: "", to: "", withinDays: "", thresholdPct: "" };

const labels: Record<Field, string> = { vendorId: "Vendor id", productId: "Product id", asOf: "As of", from: "Ordered from", to: "Ordered to", withinDays: "Days ahead", thresholdPct: "Released at least (%)" };

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
  const days = d.withinDays.trim();
  if (days !== "" && !(/^\d{1,3}$/.test(days) && Number(days) <= 366)) {
    errors.withinDays = "Days ahead must be a whole number from 0 to 366.";
  }
  const pct = d.thresholdPct.trim();
  if (pct !== "" && !(/^\d{2,3}$/.test(pct) && Number(pct) >= 50 && Number(pct) <= 100)) {
    errors.thresholdPct = "Released at least must be a whole number from 50 to 100.";
  }
  if (!errors.from && !errors.to && d.from !== "" && d.to !== "" && d.to < d.from) errors.to = "The end date is before the start date.";
  return errors;
}

const toFilters = (d: Draft): InquiryFilters => {
  const opt = (v: string) => (v.trim() === "" ? undefined : v.trim());
  return { vendorId: opt(d.vendorId), productId: opt(d.productId), asOf: opt(d.asOf), from: opt(d.from), to: opt(d.to), withinDays: opt(d.withinDays), thresholdPct: opt(d.thresholdPct) };
};

/** The inquiry sub-navigation; NavLink marks the open inquiry with aria-current="page". */
function InquiryNav() {
  return (
    <nav aria-label="Inquiries" className={styles.actions}>
      <NavLink className={styles.link} to="/purchasing/inquiries" end>
        Open orders by vendor
      </NavLink>
      <NavLink className={styles.link} to="/purchasing/inquiries/overdue">
        Overdue lines
      </NavLink>
      <NavLink className={styles.link} to="/purchasing/inquiries/backordered">
        Backordered lines
      </NavLink>
      <NavLink className={styles.link} to="/purchasing/inquiries/expiring-agreements">
        Expiring agreements
      </NavLink>
      <NavLink className={styles.link} to="/purchasing/inquiries/near-limit-agreements">
        Agreements near limit
      </NavLink>
      <NavLink className={styles.link} to="/purchasing/inquiries/change-orders">
        Change orders
      </NavLink>
      <NavLink className={styles.link} to="/purchasing/inquiries/price-history">
        Price history
      </NavLink>
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
                  inputMode={f === "withinDays" || f === "thresholdPct" ? "numeric" : undefined}
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
    { key: "vendorId", header: "Vendor", render: (v) => <Named entity="vendor" id={v.vendorId} /> },
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
    { key: "vendorId", header: "Vendor", render: (l) => <Named entity="vendor" id={l.vendorId} /> },
    { key: "item", header: "Item", render: (l) => l.description || <Named entity="product" id={l.productId} /> },
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

/** Backordered purchase order lines (PRD 3.17.14): received in part, the rest still owed. */
export function BackorderedLinesPage() {
  const columns: DataTableColumn<BackorderedLine>[] = [
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
    { key: "vendorId", header: "Vendor", render: (l) => <Named entity="vendor" id={l.vendorId} /> },
    { key: "item", header: "Item", render: (l) => l.description || <Named entity="product" id={l.productId} /> },
    { key: "qty", header: "Ordered", align: "end", render: (l) => `${l.qty} ${l.unit}` },
    { key: "receivedQty", header: "Received", align: "end", render: (l) => `${l.receivedQty} ${l.unit}` },
    { key: "openQty", header: "Backordered", align: "end", render: (l) => `${l.openQty} ${l.unit}` },
    { key: "openValue", header: "Open value", align: "end", render: (l) => money(l.openValue, l.currency) },
    { key: "dueDate", header: "Due", mono: true },
    { key: "daysLate", header: "Days late", align: "end" },
  ];
  return (
    <InquiryFrame<BackorderedLine>
      title="Backordered lines"
      description="Open lines of confirmed purchase orders that were received in part, earliest due first. A line is due on the date the vendor confirmed, else the requested date."
      fields={["vendorId", "productId", "asOf"]}
      tableLabel="Backordered order lines"
      columns={columns}
      rowKey={(l) => `${l.purchaseOrderId}:${l.lineNo}`}
      empty={{ title: "Nothing backordered", description: "No open line has been received in part." }}
      load={(f, cursor, signal) => listBackorderedLines(f, { cursor, signal })}
      what="backordered lines"
    />
  );
}

/** Purchase agreements ending soon or already lapsed (PRD 3.17.14, FR-17.14), soonest first. */
export function ExpiringAgreementsPage() {
  const columns: DataTableColumn<ExpiringAgreement>[] = [
    {
      key: "number",
      header: "Agreement",
      render: (a) => (
        <Link className={styles.link} to={`/purchasing/agreements/${a.id}`}>
          {a.number}
        </Link>
      ),
    },
    { key: "vendorId", header: "Vendor", render: (a) => <Named entity="vendor" id={a.vendorId} /> },
    { key: "status", header: "Status" },
    { key: "validTo", header: "Ends", mono: true },
    { key: "daysLeft", header: "Days left", align: "end", render: (a) => (a.lapsed ? `Lapsed ${-a.daysLeft} ${a.daysLeft === -1 ? "day" : "days"} ago` : a.daysLeft) },
    { key: "committedAmount", header: "Committed", align: "end", render: (a) => money(a.committedAmount, a.currency) },
    { key: "releasedAmount", header: "Released", align: "end", render: (a) => money(a.releasedAmount, a.currency) },
    { key: "qtyLines", header: "Quantity lines", align: "end" },
  ];
  return (
    <InquiryFrame<ExpiringAgreement>
      title="Expiring agreements"
      description="Effective and on-hold purchase agreements that end within the look-ahead (30 days unless you change it), soonest first. An agreement past its end date that is still in force is shown as lapsed. Committed and released amounts add up the value lines; quantity lines are only counted."
      fields={["vendorId", "asOf", "withinDays"]}
      tableLabel="Expiring purchase agreements"
      columns={columns}
      rowKey={(a) => a.id}
      empty={{ title: "No agreement is about to end", description: "Agreements with an end date inside the look-ahead appear here." }}
      load={(f, cursor, signal) => listExpiringAgreements(f, { cursor, signal })}
      what="expiring agreements"
    />
  );
}

/** Agreement lines released to the threshold of their commitment (FR-17.14), most released first. */
export function NearLimitAgreementsPage() {
  const columns: DataTableColumn<NearLimitLine>[] = [
    {
      key: "number",
      header: "Agreement",
      render: (l) => (
        <Link className={styles.link} to={`/purchasing/agreements/${l.agreementId}`}>
          {l.number}
        </Link>
      ),
    },
    { key: "lineNo", header: "Line", align: "end" },
    { key: "vendorId", header: "Vendor", render: (l) => <Named entity="vendor" id={l.vendorId} /> },
    { key: "commitmentType", header: "Commitment", render: (l) => l.commitmentType.replace(/_/g, " ").toLowerCase() },
    { key: "item", header: "Item", render: (l) => l.category || (l.productId ? <Named entity="product" id={l.productId} /> : "Whole agreement") },
    { key: "maxEnforced", header: "Limit" },
    { key: "committed", header: "Committed", align: "end", render: (l) => measure(l, l.committed) },
    { key: "released", header: "Released", align: "end", render: (l) => measure(l, l.released) },
    { key: "remaining", header: "Remaining", align: "end", render: (l) => measure(l, l.remaining) },
    { key: "pctReleased", header: "Released %", align: "end", render: (l) => (l.overrun ? `${l.pctReleased} % (over)` : `${l.pctReleased} %`) },
  ];
  return (
    <InquiryFrame<NearLimitLine>
      title="Agreements near limit"
      description="Lines of effective purchase agreements released to at least the chosen share of what was committed (90 % unless you change it), most released first. A line past 100 % is marked over. Quantity lines are measured in quantity and the others in amount."
      fields={["vendorId", "thresholdPct"]}
      tableLabel="Agreement lines near their limit"
      columns={columns}
      rowKey={(l) => l.lineId}
      empty={{ title: "No agreement line is near its limit", description: "Lines appear here once releases reach the threshold of the commitment." }}
      load={(f, cursor, signal) => listNearLimitAgreements(f, { cursor, signal })}
      what="agreements near their limit"
    />
  );
}

const measure = (l: NearLimitLine, value: string) => (l.commitmentType === "QUANTITY" ? `${value} ${l.unit ?? ""}`.trim() : money(value, l.currency));

/** Confirmed purchase orders with a change waiting for re-approval (FR-17.8, FR-17.14), longest waiting first. */
export function ChangeOrdersPage() {
  const columns: DataTableColumn<ChangeOrder>[] = [
    {
      key: "po",
      header: "Purchase order",
      render: (c) => (
        <Link className={styles.link} to={`/purchasing/purchase-orders/${c.purchaseOrderId}`}>
          {c.number}
        </Link>
      ),
    },
    { key: "vendorId", header: "Vendor", render: (c) => <Named entity="vendor" id={c.vendorId} /> },
    { key: "versions", header: "Version", align: "end", render: (c) => `${c.confirmedVersion} to ${c.pendingVersion}` },
    { key: "changeCount", header: "Changes", align: "end" },
    { key: "confirmedTotal", header: "Confirmed total", align: "end", render: (c) => money(c.confirmedTotal, c.currency) },
    { key: "proposedTotal", header: "Proposed total", align: "end", render: (c) => money(c.proposedTotal, c.currency) },
    { key: "changedAt", header: "Changed", mono: true, render: (c) => c.changedAt.slice(0, 16).replace("T", " ") + " UTC" },
  ];
  return (
    <InquiryFrame<ChangeOrder>
      title="Change orders"
      description="Confirmed purchase orders whose latest change was held for re-approval, longest waiting first. The vendor still holds the confirmed version until the change is approved and confirmed."
      fields={["vendorId"]}
      tableLabel="Purchase order changes awaiting re-approval"
      columns={columns}
      rowKey={(c) => c.purchaseOrderId}
      empty={{ title: "No change is waiting", description: "A change that a re-approval rule holds back appears here while it waits for approval." }}
      load={(f, cursor, signal) => listChangeOrders(f, { cursor, signal })}
      what="change orders"
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
    { key: "vendorId", header: "Vendor", render: (e) => <Named entity="vendor" id={e.vendorId} /> },
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
