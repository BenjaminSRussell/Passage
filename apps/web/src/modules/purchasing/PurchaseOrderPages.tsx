import { useMemo, useState, type FormEvent } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  buttonClassName,
  type DataTableColumn,
} from "../../shell/ui";
import {
  createPurchaseOrder,
  describeProblem,
  getPurchaseOrder,
  listPurchaseOrders,
  newIdempotencyKey,
  type ApprovalStatus,
  type ChargeCalc,
  type PurchaseOrderInput,
  type PurchaseOrderLine,
  type PurchaseOrderSummary,
  type Warning,
} from "./api/client";
import {
  Details,
  LoadError,
  ShortId,
  StatusBadge,
  currencyPattern,
  datePattern,
  decimalPattern,
  money,
  statusLabel,
  todayIso,
  useResource,
  uuidPattern,
  type Failure,
} from "./parts";
import styles from "./pages.module.css";

const APPROVAL_STATUSES: ApprovalStatus[] = ["DRAFT", "IN_REVIEW", "APPROVED", "REJECTED", "CONFIRMED", "FINALIZED", "CANCELLED"];
const CALCS: { value: ChargeCalc; label: string }[] = [
  { value: "FIXED", label: "Fixed amount" },
  { value: "PER_UNIT", label: "Per unit" },
  { value: "PERCENT", label: "Percent of net lines" },
];

const listColumns: DataTableColumn<PurchaseOrderSummary>[] = [
  { key: "number", header: "Number", mono: true },
  { key: "approvalStatus", header: "Approval", render: (row) => <StatusBadge status={row.approvalStatus} /> },
  { key: "vendorId", header: "Vendor", render: (row) => <ShortId id={row.vendorId} /> },
  { key: "lineCount", header: "Lines", align: "end" },
  { key: "total", header: "Total", align: "end", render: (row) => money(row.total, row.currency) },
  { key: "createdAt", header: "Created", mono: true, render: (row) => row.createdAt.slice(0, 10) },
];

/** Purchase order list with approval-status filter and load-more paging. */
export function PurchaseOrdersPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<ApprovalStatus | "">("");
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<PurchaseOrderSummary[]>([]);
  const page = useResource(
    async (signal) => {
      const result = await listPurchaseOrders({ approvalStatus: status }, { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [status, cursor],
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Purchase orders"
        description="Orders to vendors with lines, charges and totals that always balance to the cent."
        actions={
          <Link className={buttonClassName("primary")} to="/purchasing/purchase-orders/new">
            New purchase order
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <div className={styles.filter}>
          <FormField id="po-status-filter" label="Approval status">
            {(control) => (
              <Select
                {...control}
                value={status}
                onChange={(e) => {
                  setCursor(undefined);
                  setStatus(e.target.value as ApprovalStatus | "");
                }}
              >
                <option value="">All statuses</option>
                {APPROVAL_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {statusLabel(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
      </div>
      {page.error ? <LoadError what="purchase orders" error={page.error} onRetry={page.reload} /> : null}
      <DataTable
        label="Purchase orders"
        columns={listColumns}
        rows={page.error ? [] : rows}
        getRowKey={(row) => row.id}
        loading={page.loading && rows.length === 0}
        empty={{ title: "No purchase orders yet", description: "Create one here, or consolidate approved requisition lines." }}
        onRowOpen={(row) => navigate(`/purchasing/purchase-orders/${row.id}`)}
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

interface LineDraft {
  key: string;
  productId: string;
  description: string;
  qty: string;
  unit: string;
  unitPrice: string;
  priceUnit: string;
  discountPct: string;
  taxAmount: string;
  deliveryDate: string;
  agreementLineId: string;
}

interface ChargeDraft {
  key: string;
  calc: ChargeCalc;
  value: string;
}

function emptyLine(): LineDraft {
  return {
    key: newIdempotencyKey(),
    productId: "",
    description: "",
    qty: "1",
    unit: "EA",
    unitPrice: "",
    priceUnit: "1",
    discountPct: "",
    taxAmount: "",
    deliveryDate: todayIso(),
    agreementLineId: "",
  };
}

type Errors = Record<string, string>;

export function validateOrder(legalEntityId: string, vendorId: string, currency: string, lines: LineDraft[], charges: ChargeDraft[]): Errors {
  const errors: Errors = {};
  if (!uuidPattern.test(legalEntityId)) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!uuidPattern.test(vendorId)) errors.vendorId = "Enter the vendor id (a UUID).";
  if (!currencyPattern.test(currency)) errors.currency = "Use a three-letter ISO code such as USD.";
  lines.forEach((l, i) => {
    const f = (name: string) => `lines[${i}].${name}`;
    if (l.productId !== "" && !uuidPattern.test(l.productId)) errors[f("productId")] = "Product id must be a UUID.";
    if (l.productId === "" && l.description.trim() === "") errors[f("description")] = "Enter a product id or a description.";
    if (l.productId === "" && l.unitPrice === "") errors[f("unitPrice")] = "A line without a product needs a unit price.";
    if (!decimalPattern.test(l.qty) || Number(l.qty) <= 0) errors[f("qty")] = "Enter a positive quantity.";
    if (l.unit.trim() === "") errors[f("unit")] = "Enter a unit.";
    if (l.unitPrice !== "" && !decimalPattern.test(l.unitPrice)) errors[f("unitPrice")] = "Enter a decimal price.";
    if (!decimalPattern.test(l.priceUnit) || Number(l.priceUnit) <= 0) errors[f("priceUnit")] = "Enter a positive price unit.";
    if (l.discountPct !== "" && (!decimalPattern.test(l.discountPct) || Number(l.discountPct) > 100)) {
      errors[f("discountPct")] = "Enter a percent from 0 to 100.";
    }
    if (l.taxAmount !== "" && !decimalPattern.test(l.taxAmount)) errors[f("taxAmount")] = "Enter a decimal tax amount.";
    if (!datePattern.test(l.deliveryDate)) errors[f("deliveryDate")] = "Enter a date.";
    if (l.agreementLineId !== "" && !uuidPattern.test(l.agreementLineId.trim())) errors[f("agreementLineId")] = "Agreement line id must be a UUID.";
  });
  charges.forEach((c, i) => {
    if (!decimalPattern.test(c.value)) errors[`charges[${i}].value`] = "Enter a decimal value.";
  });
  return errors;
}

function toInput(legalEntityId: string, vendorId: string, currency: string, lines: LineDraft[], charges: ChargeDraft[]): PurchaseOrderInput {
  const opt = (v: string) => (v.trim() === "" ? undefined : v.trim());
  return {
    legalEntityId,
    vendorId,
    currency,
    lines: lines.map((l) => ({
      productId: opt(l.productId),
      description: opt(l.description),
      qty: l.qty.trim(),
      unit: l.unit.trim(),
      unitPrice: opt(l.unitPrice),
      priceUnit: opt(l.priceUnit),
      discountPct: opt(l.discountPct),
      taxAmount: opt(l.taxAmount),
      deliveryDate: l.deliveryDate,
      agreementLineId: opt(l.agreementLineId),
    })),
    charges: charges.length > 0 ? charges.map((c) => ({ calc: c.calc, value: c.value.trim() })) : undefined,
  };
}

/** New purchase order: header, lines (blank price = trade agreement) and header charges. */
export function PurchaseOrderCreatePage() {
  const navigate = useNavigate();
  const [legalEntityId, setLegalEntityId] = useState("");
  const [vendorId, setVendorId] = useState("");
  const [currency, setCurrency] = useState("USD");
  const [lines, setLines] = useState<LineDraft[]>(() => [emptyLine()]);
  const [charges, setCharges] = useState<ChargeDraft[]>([]);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useMemo(() => newIdempotencyKey(), [legalEntityId, vendorId, currency, lines, charges]);

  const update = (index: number, patch: Partial<LineDraft>) => setLines((ls) => ls.map((l, i) => (i === index ? { ...l, ...patch } : l)));
  const updateCharge = (index: number, patch: Partial<ChargeDraft>) =>
    setCharges((cs) => cs.map((c, i) => (i === index ? { ...c, ...patch } : c)));

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const le = legalEntityId.trim();
    const vendor = vendorId.trim();
    const found = validateOrder(le, vendor, currency, lines, charges);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await createPurchaseOrder(toInput(le, vendor, currency, lines, charges), key);
      navigate(`/purchasing/purchase-orders/${body.id}`, { state: { warnings: body.warnings ?? [] } });
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New purchase order"
        description="Leave a unit price blank to take the agreement line's price when it overrides, else the most specific trade agreement price for the vendor, product and quantity."
      />
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <Card title="Order">
          <div className={styles.grid}>
            <FormField id="po-legal-entity" label="Legal entity id" error={errors.legalEntityId}>
              {(c) => <Input {...c} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} />}
            </FormField>
            <FormField id="po-vendor" label="Vendor id" error={errors.vendorId}>
              {(c) => <Input {...c} value={vendorId} onChange={(e) => setVendorId(e.target.value)} />}
            </FormField>
            <FormField id="po-currency" label="Currency" error={errors.currency}>
              {(c) => <Input {...c} value={currency} maxLength={3} onChange={(e) => setCurrency(e.target.value.toUpperCase())} />}
            </FormField>
          </div>
        </Card>
        <Card
          title="Lines"
          actions={
            <Button size="sm" onClick={() => setLines((ls) => [...ls, emptyLine()])}>
              Add line
            </Button>
          }
        >
          {lines.map((line, i) => {
            const id = (name: string) => `po-line-${i}-${name}`;
            const err = (name: string) => errors[`lines[${i}].${name}`];
            return (
              <fieldset key={line.key} className={styles.lineCard} aria-label={`Line ${i + 1}`}>
                <div className={styles.lineHead}>
                  <p className={styles.lineTitle}>Line {i + 1}</p>
                  {lines.length > 1 ? (
                    <Button size="sm" variant="ghost" onClick={() => setLines((ls) => ls.filter((_, j) => j !== i))}>
                      Remove line {i + 1}
                    </Button>
                  ) : null}
                </div>
                <div className={styles.grid}>
                  <FormField id={id("product")} label="Product id" optional error={err("productId")}>
                    {(c) => <Input {...c} value={line.productId} onChange={(e) => update(i, { productId: e.target.value })} />}
                  </FormField>
                  <FormField id={id("description")} label="Description" optional error={err("description")}>
                    {(c) => <Input {...c} value={line.description} onChange={(e) => update(i, { description: e.target.value })} />}
                  </FormField>
                  <FormField id={id("qty")} label="Quantity" error={err("qty")}>
                    {(c) => <Input {...c} inputMode="decimal" value={line.qty} onChange={(e) => update(i, { qty: e.target.value })} />}
                  </FormField>
                  <FormField id={id("unit")} label="Unit" error={err("unit")}>
                    {(c) => <Input {...c} value={line.unit} onChange={(e) => update(i, { unit: e.target.value })} />}
                  </FormField>
                  <FormField id={id("price")} label="Unit price" optional hint="Blank uses the trade agreement" error={err("unitPrice")}>
                    {(c) => <Input {...c} inputMode="decimal" value={line.unitPrice} onChange={(e) => update(i, { unitPrice: e.target.value })} />}
                  </FormField>
                  <FormField id={id("price-unit")} label="Price unit" error={err("priceUnit")}>
                    {(c) => <Input {...c} inputMode="decimal" value={line.priceUnit} onChange={(e) => update(i, { priceUnit: e.target.value })} />}
                  </FormField>
                  <FormField id={id("discount")} label="Discount %" optional error={err("discountPct")}>
                    {(c) => <Input {...c} inputMode="decimal" value={line.discountPct} onChange={(e) => update(i, { discountPct: e.target.value })} />}
                  </FormField>
                  <FormField id={id("tax")} label="Tax amount" optional error={err("taxAmount")}>
                    {(c) => <Input {...c} inputMode="decimal" value={line.taxAmount} onChange={(e) => update(i, { taxAmount: e.target.value })} />}
                  </FormField>
                  <FormField id={id("delivery")} label="Delivery date" error={err("deliveryDate")}>
                    {(c) => <Input {...c} type="date" value={line.deliveryDate} onChange={(e) => update(i, { deliveryDate: e.target.value })} />}
                  </FormField>
                  <FormField id={id("agreement")} label="Agreement line id" optional hint="Releases the line against a purchase agreement" error={err("agreementLineId")}>
                    {(c) => <Input {...c} value={line.agreementLineId} onChange={(e) => update(i, { agreementLineId: e.target.value })} />}
                  </FormField>
                </div>
              </fieldset>
            );
          })}
        </Card>
        <Card
          title="Charges"
          actions={
            <Button size="sm" onClick={() => setCharges((cs) => [...cs, { key: newIdempotencyKey(), calc: "FIXED", value: "" }])}>
              Add charge
            </Button>
          }
        >
          {charges.length === 0 ? <p className={styles.muted}>No header charges. Freight, handling and fees go here.</p> : null}
          {charges.map((charge, i) => (
            <fieldset key={charge.key} className={styles.lineCard} aria-label={`Charge ${i + 1}`}>
              <div className={styles.lineHead}>
                <p className={styles.lineTitle}>Charge {i + 1}</p>
                <Button size="sm" variant="ghost" onClick={() => setCharges((cs) => cs.filter((_, j) => j !== i))}>
                  Remove charge {i + 1}
                </Button>
              </div>
              <div className={styles.grid}>
                <FormField id={`po-charge-${i}-calc`} label="Calculation">
                  {(c) => (
                    <Select {...c} value={charge.calc} onChange={(e) => updateCharge(i, { calc: e.target.value as ChargeCalc })}>
                      {CALCS.map((o) => (
                        <option key={o.value} value={o.value}>
                          {o.label}
                        </option>
                      ))}
                    </Select>
                  )}
                </FormField>
                <FormField id={`po-charge-${i}-value`} label="Value" error={errors[`charges[${i}].value`]}>
                  {(c) => <Input {...c} inputMode="decimal" value={charge.value} onChange={(e) => updateCharge(i, { value: e.target.value })} />}
                </FormField>
              </div>
            </fieldset>
          ))}
        </Card>
        <Alert tone="danger" live="alert">
          {failure ? `${failure.message} (${failure.code})` : null}
        </Alert>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save purchase order
          </Button>
          <Link className={buttonClassName("ghost")} to="/purchasing/purchase-orders">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}

function PriceSource({ source }: { source: string }) {
  if (source.startsWith("TRADE_AGREEMENT:")) {
    const id = source.slice("TRADE_AGREEMENT:".length);
    return (
      <Link className={styles.link} to={`/purchasing/trade-agreements/${id}`} title={id}>
        Trade agreement
      </Link>
    );
  }
  if (source.startsWith("AGREEMENT:")) {
    return <Badge tone="info">Purchase agreement</Badge>;
  }
  return <Badge tone="neutral">{statusLabel(source)}</Badge>;
}

const lineColumns = (currency: string): DataTableColumn<PurchaseOrderLine>[] => [
  { key: "lineNo", header: "Line", align: "end" },
  { key: "item", header: "Item", render: (l) => l.description || <ShortId id={l.productId} /> },
  { key: "qty", header: "Quantity", align: "end", render: (l) => `${l.qty} ${l.unit}` },
  { key: "unitPrice", header: "Unit price", align: "end", render: (l) => `${l.unitPrice} / ${l.priceUnit}` },
  { key: "priceSource", header: "Price source", render: (l) => <PriceSource source={l.priceSource} /> },
  { key: "discountAmount", header: "Discount", align: "end", render: (l) => money(l.discountAmount, currency) },
  { key: "lineAmount", header: "Net amount", align: "end", render: (l) => money(l.lineAmount, currency) },
  { key: "taxAmount", header: "Tax", align: "end", render: (l) => money(l.taxAmount, currency) },
  { key: "deliveryDate", header: "Delivery", mono: true },
];

/** Purchase order detail: header, totals panel, lines with price source, charges. */
export function PurchaseOrderDetailPage() {
  const { id = "" } = useParams();
  // Warnings (PRC-AGR-004) come back only on create; the create page hands them over in router state.
  const warnings = (useLocation().state as { warnings?: Warning[] } | null)?.warnings ?? [];
  const resource = useResource((signal) => getPurchaseOrder(id, signal), [id]);
  const po = resource.data?.body;

  if (resource.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Purchase order" />
        {resource.error.code === "PRC-PO-NOT-FOUND" ? (
          <Card>
            <EmptyState
              icon="receipt"
              title="Purchase order not found"
              description="It may belong to another legal entity, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/purchasing/purchase-orders">
                  Back to purchase orders
                </Link>
              }
            />
          </Card>
        ) : (
          <LoadError what="the purchase order" error={resource.error} onRetry={resource.reload} />
        )}
      </div>
    );
  }
  if (!po) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Purchase order" />
        <p className={styles.muted} role="status">
          Loading purchase order…
        </p>
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader title={`Purchase order ${po.number}`} meta={<StatusBadge status={po.approvalStatus} />} />
      {warnings.length > 0 ? (
        <Alert tone="warning" title="Agreement limits">
          <ul className={styles.cardLinks}>
            {warnings.map((w) => (
              <li key={`${w.code}-${w.lineNo}`}>
                Line {w.lineNo}: {w.detail} ({w.code})
              </li>
            ))}
          </ul>
        </Alert>
      ) : null}
      <Card title="Summary">
        <Details
          items={[
            { label: "Vendor", value: po.vendorId, mono: true },
            { label: "Legal entity", value: po.legalEntityId, mono: true },
            { label: "Document status", value: statusLabel(po.documentStatus) },
            { label: "Confirmed version", value: String(po.versionNo), mono: true },
            { label: "Change management", value: po.changeManagement ? "On" : "Off" },
            { label: "Created", value: po.createdAt.slice(0, 10), mono: true },
          ]}
        />
      </Card>
      <Card title="Totals">
        <dl className={styles.totals} aria-label="Totals">
          <dt>Subtotal</dt>
          <dd>{money(po.subtotal, po.currency)}</dd>
          <dt>Discount</dt>
          <dd>−{money(po.discountTotal, po.currency)}</dd>
          <dt>Charges</dt>
          <dd>{money(po.chargesTotal, po.currency)}</dd>
          <dt>Tax</dt>
          <dd>{money(po.taxTotal, po.currency)}</dd>
          <dt className={styles.grandTotal}>Total</dt>
          <dd className={styles.grandTotal}>{money(po.total, po.currency)}</dd>
        </dl>
      </Card>
      <DataTable label="Purchase order lines" columns={lineColumns(po.currency)} rows={po.lines} getRowKey={(l) => String(l.lineNo)} />
      <DataTable
        label="Charges"
        columns={[
          { key: "seq", header: "Charge", align: "end" },
          { key: "calc", header: "Calculation", render: (c) => statusLabel(c.calc) },
          { key: "value", header: "Value", align: "end", mono: true },
          { key: "amount", header: "Amount", align: "end", render: (c) => money(c.amount, po.currency) },
        ]}
        rows={po.charges}
        getRowKey={(c) => String(c.seq)}
        empty={{ title: "No charges", description: "This order has no header charges." }}
      />
    </div>
  );
}
