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
  actOnPurchaseOrder,
  createPurchaseOrder,
  describeProblem,
  editPurchaseOrder,
  getPurchaseOrder,
  listInvoicingPlans,
  listPurchaseOrderVersions,
  listPurchaseOrders,
  newIdempotencyKey,
  type ApprovalStatus,
  type ChargeCalc,
  type InvoicingPlan,
  type PatchOp,
  type PurchaseOrder,
  type PurchaseOrderAction,
  type PurchaseOrderInput,
  type PurchaseOrderLine,
  type PurchaseOrderSummary,
  type PurchaseOrderVersion,
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
  /** Set on an edit for a line already on the order; blank price keeps its price. */
  lineNo?: number;
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
  /** Stored price and received quantity of an existing line, shown as hints. */
  keptPrice?: string;
  receivedQty?: string;
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

/** An existing line as an edit draft: the price is left blank so the line keeps its price and source. */
function draftOf(l: PurchaseOrderLine): LineDraft {
  return {
    key: newIdempotencyKey(),
    lineNo: l.lineNo,
    productId: l.productId ?? "",
    description: l.description ?? "",
    qty: l.qty,
    unit: l.unit,
    unitPrice: "",
    priceUnit: l.priceUnit,
    discountPct: l.discountPct,
    taxAmount: l.taxAmount,
    deliveryDate: l.deliveryDate,
    agreementLineId: l.agreementLineId ?? "",
    keptPrice: `${l.unitPrice} / ${l.priceUnit}`,
    receivedQty: l.receivedQty,
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
    if (l.lineNo === undefined && l.productId === "" && l.unitPrice === "") errors[f("unitPrice")] = "A line without a product needs a unit price.";
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

function toInput(
  header: { legalEntityId: string; vendorId: string; currency: string; siteId?: string; warehouseId?: string },
  lines: LineDraft[],
  charges: ChargeDraft[],
): PurchaseOrderInput {
  const opt = (v: string) => (v.trim() === "" ? undefined : v.trim());
  return {
    ...header,
    lines: lines.map((l) => ({
      lineNo: l.lineNo,
      productId: opt(l.productId),
      description: opt(l.description),
      qty: l.qty.trim(),
      unit: l.unit.trim(),
      unitPrice: opt(l.unitPrice),
      priceUnit: opt(l.priceUnit),
      discountPct: opt(l.discountPct),
      taxAmount: opt(l.taxAmount),
      deliveryDate: l.deliveryDate,
      // An existing line keeps its agreement link; the service refuses a changed one.
      agreementLineId: l.lineNo === undefined ? opt(l.agreementLineId) : undefined,
    })),
    charges: charges.length > 0 ? charges.map((c) => ({ calc: c.calc, value: c.value.trim() })) : undefined,
  };
}

/** The line fieldsets of the create and edit forms. */
function LinesCard({
  lines,
  setLines,
  errors,
  editing = false,
}: {
  lines: LineDraft[];
  setLines: (fn: (ls: LineDraft[]) => LineDraft[]) => void;
  errors: Errors;
  editing?: boolean;
}) {
  const update = (index: number, patch: Partial<LineDraft>) => setLines((ls) => ls.map((l, i) => (i === index ? { ...l, ...patch } : l)));
  return (
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
        const existing = line.lineNo !== undefined;
        const newCount = lines.slice(0, i + 1).filter((l) => l.lineNo === undefined).length;
        const title = existing ? `Line ${line.lineNo}` : editing ? `New line ${newCount}` : `Line ${i + 1}`;
        return (
          <fieldset key={line.key} className={styles.lineCard} aria-label={title}>
            <div className={styles.lineHead}>
              <p className={styles.lineTitle}>{title}</p>
              {lines.length > 1 ? (
                <Button size="sm" variant="ghost" onClick={() => setLines((ls) => ls.filter((_, j) => j !== i))}>
                  {existing ? `Cancel line ${line.lineNo}` : `Remove ${title.toLowerCase()}`}
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
              <FormField
                id={id("qty")}
                label="Quantity"
                hint={line.receivedQty && Number(line.receivedQty) > 0 ? `${line.receivedQty} received; cannot go lower` : undefined}
                error={err("qty")}
              >
                {(c) => <Input {...c} inputMode="decimal" value={line.qty} onChange={(e) => update(i, { qty: e.target.value })} />}
              </FormField>
              <FormField id={id("unit")} label="Unit" error={err("unit")}>
                {(c) => <Input {...c} value={line.unit} onChange={(e) => update(i, { unit: e.target.value })} />}
              </FormField>
              <FormField
                id={id("price")}
                label="Unit price"
                optional
                hint={existing ? `Blank keeps ${line.keptPrice} while product and unit stay the same` : "Blank uses the trade agreement"}
                error={err("unitPrice")}
              >
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
              {existing ? (
                line.agreementLineId ? (
                  <p className={styles.muted}>
                    Released against agreement line <ShortId id={line.agreementLineId} />; its quantity cannot change here.
                  </p>
                ) : null
              ) : (
                <FormField id={id("agreement")} label="Agreement line id" optional hint="Releases the line against a purchase agreement" error={err("agreementLineId")}>
                  {(c) => <Input {...c} value={line.agreementLineId} onChange={(e) => update(i, { agreementLineId: e.target.value })} />}
                </FormField>
              )}
            </div>
          </fieldset>
        );
      })}
    </Card>
  );
}

/** Header charges of the create and edit forms. */
function ChargesCard({ charges, setCharges, errors }: { charges: ChargeDraft[]; setCharges: (fn: (cs: ChargeDraft[]) => ChargeDraft[]) => void; errors: Errors }) {
  const updateCharge = (index: number, patch: Partial<ChargeDraft>) =>
    setCharges((cs) => cs.map((c, i) => (i === index ? { ...c, ...patch } : c)));
  return (
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
  );
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
      const { body } = await createPurchaseOrder(toInput({ legalEntityId: le, vendorId: vendor, currency }, lines, charges), key);
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
        <LinesCard lines={lines} setLines={setLines} errors={errors} />
        <ChargesCard charges={charges} setCharges={setCharges} errors={errors} />
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

/** Approval statuses in which PATCH is accepted (APPROVED is frozen until confirmed or rejected). */
const EDITABLE: ApprovalStatus[] = ["DRAFT", "IN_REVIEW", "REJECTED", "CONFIRMED"];

/** What saving an edit did, for the detail page's notice. */
export function editOutcome(before: PurchaseOrder, after: PurchaseOrder): string {
  if (before.versionNo === 0) return "Saved. The order is a draft again; submit it for approval.";
  if (after.approvalStatus === "IN_REVIEW") {
    return `Change saved as version ${before.versionNo + 1}. It needs re-approval before the vendor sees it.`;
  }
  if (after.versionNo > before.versionNo) return `Version ${after.versionNo} is confirmed and goes to the vendor.`;
  if (after.approvalStatus === "CONFIRMED" && before.approvalStatus !== "CONFIRMED") {
    return `The pending change is withdrawn; version ${after.versionNo} stands.`;
  }
  return "Nothing changed.";
}

function EditForm({ po }: { po: PurchaseOrder }) {
  const navigate = useNavigate();
  const [lines, setLines] = useState<LineDraft[]>(() => po.lines.filter((l) => l.status !== "CANCELLED").map(draftOf));
  const [charges, setCharges] = useState<ChargeDraft[]>(() =>
    po.charges.map((c) => ({ key: newIdempotencyKey(), calc: c.calc, value: c.value })),
  );
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found = validateOrder(po.legalEntityId, po.vendorId, po.currency, lines, charges);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const header = { legalEntityId: po.legalEntityId, vendorId: po.vendorId, currency: po.currency, siteId: po.siteId, warehouseId: po.warehouseId };
      const { body } = await editPurchaseOrder(po.id, toInput(header, lines, charges), po.rowVersion);
      navigate(`/purchasing/purchase-orders/${po.id}`, { state: { notice: editOutcome(po, body) } });
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors);
    } finally {
      setBusy(false);
    }
  }

  const confirmed = po.versionNo > 0;
  return (
    <form className={styles.form} onSubmit={onSubmit} noValidate>
      <Card title="Order">
        <Details
          items={[
            { label: "Vendor", value: po.vendorId, mono: true },
            { label: "Legal entity", value: po.legalEntityId, mono: true },
            { label: "Currency", value: po.currency, mono: true },
            { label: "Status", value: <StatusBadge status={po.approvalStatus} /> },
          ]}
        />
      </Card>
      <LinesCard lines={lines} setLines={setLines} errors={errors} editing />
      <ChargesCard charges={charges} setCharges={setCharges} errors={errors} />
      <Alert tone="danger" live="alert">
        {failure
          ? failure.code === "PRC-PO-009"
            ? `Someone else changed this order. Reload it and make the change again. (${failure.code})`
            : `${failure.message} (${failure.code})`
          : null}
      </Alert>
      <div className={styles.actions}>
        <Button type="submit" variant="primary" loading={busy}>
          {confirmed ? "Save change" : "Save purchase order"}
        </Button>
        <Link className={buttonClassName("ghost")} to={`/purchasing/purchase-orders/${po.id}`}>
          Discard
        </Link>
      </div>
    </form>
  );
}

/**
 * Edit a purchase order (FR-17.8). Before the first confirmation it is a plain
 * edit; after it, the change becomes the next version, confirmed at once or
 * held for re-approval by the change management rules.
 */
export function PurchaseOrderEditPage() {
  const { id = "" } = useParams();
  const resource = useResource((signal) => getPurchaseOrder(id, signal), [id]);
  const po = resource.data?.body;
  const title = po ? `${po.versionNo > 0 ? "Change" : "Edit"} purchase order ${po.number}` : "Edit purchase order";

  if (resource.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title={title} />
        <LoadError what="the purchase order" error={resource.error} onRetry={resource.reload} />
      </div>
    );
  }
  if (!po) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title={title} />
        <p className={styles.muted} role="status">
          Loading purchase order…
        </p>
      </div>
    );
  }
  if (!EDITABLE.includes(po.approvalStatus)) {
    return (
      <div className={styles.stack}>
        <PageHeader title={title} />
        <Card>
          <EmptyState
            icon="receipt"
            title={`This order is ${statusLabel(po.approvalStatus).toLowerCase()} and cannot be edited`}
            description={po.approvalStatus === "APPROVED" ? "Confirm it first, then change it as a new version." : "Finalized and cancelled orders are closed."}
            action={
              <Link className={buttonClassName("secondary")} to={`/purchasing/purchase-orders/${po.id}`}>
                Back to the order
              </Link>
            }
          />
        </Card>
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <PageHeader
        title={title}
        description={
          po.versionNo > 0
            ? "Leave a line out to cancel it. A quantity or price up more than 5 % or a new line needs re-approval; other changes are confirmed as the next version at once."
            : "Saving returns the order to draft; submit it for approval again."
        }
      />
      <EditForm po={po} />
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
  if (source.startsWith("RFQ_REPLY:")) {
    return <Badge tone="info">RFQ award</Badge>;
  }
  return <Badge tone="neutral">{statusLabel(source)}</Badge>;
}

/** The invoicing plan cell of a line: its plan, or a link to add one (FR-17.21). */
function PlanCell({ poId, line, plan, closed }: { poId: string; line: PurchaseOrderLine; plan?: InvoicingPlan; closed: boolean }) {
  const to = `/purchasing/purchase-orders/${poId}/lines/${line.lineNo}/invoicing-plan`;
  if (plan) {
    return (
      <Link className={styles.link} to={to} aria-label={`Invoicing plan of line ${line.lineNo}`}>
        {plan.kind === "PERIODIC" ? "Periodic" : "Milestones"}, {plan.rows.length} row{plan.rows.length === 1 ? "" : "s"}
      </Link>
    );
  }
  if (closed || line.status === "CANCELLED") return <>—</>;
  return (
    <Link className={styles.link} to={to} aria-label={`Add an invoicing plan to line ${line.lineNo}`}>
      Add plan
    </Link>
  );
}

const lineColumns = (
  currency: string,
  plan?: { poId: string; byLine: Map<number, InvoicingPlan>; closed: boolean },
): DataTableColumn<PurchaseOrderLine>[] => [
  { key: "lineNo", header: "Line", align: "end" },
  { key: "item", header: "Item", render: (l) => l.description || <ShortId id={l.productId} /> },
  { key: "qty", header: "Quantity", align: "end", render: (l) => `${l.qty} ${l.unit}` },
  { key: "receivedQty", header: "Received", align: "end", render: (l) => l.receivedQty ?? "0" },
  { key: "unitPrice", header: "Unit price", align: "end", render: (l) => `${l.unitPrice} / ${l.priceUnit}` },
  { key: "priceSource", header: "Price source", render: (l) => <PriceSource source={l.priceSource} /> },
  { key: "discountAmount", header: "Discount", align: "end", render: (l) => money(l.discountAmount, currency) },
  { key: "lineAmount", header: "Net amount", align: "end", render: (l) => money(l.lineAmount, currency) },
  { key: "taxAmount", header: "Tax", align: "end", render: (l) => money(l.taxAmount, currency) },
  { key: "deliveryDate", header: "Delivery", mono: true },
  { key: "status", header: "Status", render: (l) => <StatusBadge status={l.status} /> },
  ...(plan
    ? [
        {
          key: "invoicingPlan",
          header: "Invoicing plan",
          render: (l: PurchaseOrderLine) => <PlanCell poId={plan.poId} line={l} plan={plan.byLine.get(l.lineNo)} closed={plan.closed} />,
        },
      ]
    : []),
];

interface ActionChoice {
  action: PurchaseOrderAction;
  label: string;
  variant: "primary" | "secondary" | "danger";
}

/**
 * The custom methods each approval status allows (BR-17.2). Cancel is offered
 * until the order is finalized or cancelled; the service also refuses it once
 * anything is received or invoiced. Approve and reject need PurchaseOrderConfirm.
 */
export function purchaseOrderActions(status: ApprovalStatus): ActionChoice[] {
  const cancel: ActionChoice = { action: "cancel", label: "Cancel order", variant: "danger" };
  switch (status) {
    case "DRAFT":
    case "REJECTED":
      return [{ action: "submit", label: "Submit for approval", variant: "primary" }, cancel];
    case "IN_REVIEW":
      return [
        { action: "approve", label: "Approve", variant: "primary" },
        { action: "reject", label: "Reject", variant: "secondary" },
        cancel,
      ];
    case "APPROVED":
      return [{ action: "confirm", label: "Confirm and send", variant: "primary" }, cancel];
    case "CONFIRMED":
      return [cancel];
    default:
      return [];
  }
}

const actionDone: Record<PurchaseOrderAction, string> = {
  submit: "Submitted for approval.",
  approve: "Approved. Confirm it to send it to the vendor.",
  reject: "Rejected. Edit it and submit it again.",
  confirm: "Confirmed. The vendor receives this version.",
  cancel: "Cancelled. Agreement releases and requisition lines are returned.",
};

const fieldLabels: Record<string, string> = {
  qty: "quantity",
  unitPrice: "unit price",
  priceUnit: "price unit",
  discountPct: "discount %",
  lineAmount: "net amount",
  taxAmount: "tax",
  deliveryDate: "delivery date",
  status: "status",
  description: "description",
  productId: "product",
  unit: "unit",
};

const shown = (v: unknown) => (typeof v === "string" || typeof v === "number" ? String(v) : JSON.stringify(v));

/**
 * Reads one patch operation of a version in business terms: /lines/0/qty is
 * "Line 1 quantity → 110", using the version's snapshot to name the line.
 */
export function describeChange(op: PatchOp, version: PurchaseOrderVersion): string {
  const line = /^\/lines\/(\d+)\/(\w+)$/.exec(op.path);
  if (line) {
    const no = version.snapshot.lines[Number(line[1])]?.lineNo ?? Number(line[1]) + 1;
    const field = fieldLabels[line[2]] ?? line[2];
    return op.op === "remove" ? `Line ${no} ${field} removed` : `Line ${no} ${field} → ${shown(op.value)}`;
  }
  if (op.path === "/lines/-" && op.op === "add") {
    const added = op.value as { lineNo?: number } | undefined;
    return added?.lineNo !== undefined ? `Line ${added.lineNo} added` : "Line added";
  }
  if (op.path === "/total") return `Total → ${shown(op.value)} ${version.snapshot.currency}`;
  if (op.path.startsWith("/charges")) return "Charges changed";
  return `${op.op} ${op.path}`;
}

function versionColumns(): DataTableColumn<PurchaseOrderVersion>[] {
  return [
    { key: "versionNo", header: "Version", align: "end", mono: true },
    {
      key: "state",
      header: "State",
      render: (v) => (v.confirmedAt ? <Badge tone="success">Confirmed</Badge> : <Badge tone="info">Awaiting re-approval</Badge>),
    },
    { key: "confirmedAt", header: "Confirmed", mono: true, render: (v) => (v.confirmedAt ? v.confirmedAt.slice(0, 10) : "—") },
    { key: "reapprovalRequired", header: "Re-approval", render: (v) => (v.reapprovalRequired ? "Required" : "Not required") },
    {
      key: "patch",
      header: "Changes from the previous version",
      render: (v) =>
        v.versionNo === 1 ? (
          "First confirmed version"
        ) : (
          <ul className={styles.changes}>
            {v.patch.map((op, i) => (
              <li key={`${op.path}-${i}`}>{describeChange(op, v)}</li>
            ))}
          </ul>
        ),
    },
  ];
}

/** Purchase order detail: lifecycle actions, totals, lines with price source, charges and version history. */
export function PurchaseOrderDetailPage() {
  const { id = "" } = useParams();
  const state = useLocation().state as { warnings?: Warning[]; notice?: string } | null;
  // Warnings (PRC-AGR-004) come back only on create; the create page hands them over in router state.
  const warnings = state?.warnings ?? [];
  const resource = useResource((signal) => getPurchaseOrder(id, signal), [id]);
  const [changed, setChanged] = useState<PurchaseOrder | null>(null);
  const [done, setDone] = useState<string | null>(state?.notice ?? null);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState<PurchaseOrderAction | null>(null);
  const [confirmCancel, setConfirmCancel] = useState(false);
  const po = changed ?? resource.data?.body ?? null;
  const actionKey = useMemo(() => newIdempotencyKey(), [po?.rowVersion]);
  const versions = useResource((signal) => listPurchaseOrderVersions(id, signal), [id, po?.rowVersion]);
  const plans = useResource((signal) => listInvoicingPlans(id, signal), [id, po?.rowVersion]);

  async function run(action: PurchaseOrderAction) {
    if (!po) return;
    setBusy(action);
    setFailure(null);
    setDone(null);
    try {
      const { body } = await actOnPurchaseOrder(po.id, action, po.rowVersion, `${actionKey}-${action}`);
      setChanged(body);
      setDone(actionDone[action]);
      setConfirmCancel(false);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

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

  const actions = purchaseOrderActions(po.approvalStatus);
  const editable = EDITABLE.includes(po.approvalStatus);
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Purchase order ${po.number}`}
        meta={<StatusBadge status={po.approvalStatus} />}
        actions={
          actions.length > 0 || editable ? (
            <div className={styles.actions}>
              {editable ? (
                <Link className={buttonClassName("secondary")} to={`/purchasing/purchase-orders/${po.id}/edit`}>
                  {po.versionNo > 0 ? "Change order" : "Edit"}
                </Link>
              ) : null}
              {po.approvalStatus === "CONFIRMED" ? (
                <Link className={buttonClassName("secondary")} to={`/purchasing/purchase-orders/${po.id}/reprice`}>
                  Update prices
                </Link>
              ) : null}
              {actions.map((a) => (
                <Button
                  key={a.action}
                  variant={a.variant}
                  loading={busy === a.action}
                  disabled={busy !== null && busy !== a.action}
                  onClick={() => (a.action === "cancel" ? setConfirmCancel(true) : void run(a.action))}
                >
                  {a.label}
                </Button>
              ))}
            </div>
          ) : undefined
        }
      />
      {confirmCancel ? (
        <Alert
          tone="warning"
          title={`Cancel ${po.number}?`}
          action={
            <div className={styles.actions}>
              <Button size="sm" variant="danger" loading={busy === "cancel"} onClick={() => void run("cancel")}>
                Yes, cancel the order
              </Button>
              <Button size="sm" variant="ghost" disabled={busy === "cancel"} onClick={() => setConfirmCancel(false)}>
                Keep the order
              </Button>
            </div>
          }
        >
          Every open line is cancelled, agreement releases are given back and requisition lines can be ordered again. This cannot be undone.
        </Alert>
      ) : null}
      <Alert tone="danger" live="alert">
        {failure
          ? failure.code === "PRC-PO-009"
            ? `Someone else changed this order. Reload the page and try again. (${failure.code})`
            : `${failure.message} (${failure.code})`
          : null}
      </Alert>
      <Alert tone="success" live="status">
        {done}
      </Alert>
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
      {plans.error ? <LoadError what="the invoicing plans" error={plans.error} onRetry={plans.reload} /> : null}
      <DataTable
        label="Purchase order lines"
        columns={lineColumns(po.currency, {
          poId: po.id,
          byLine: new Map((plans.data ?? []).map((p) => [p.lineNo, p])),
          closed: po.approvalStatus === "CANCELLED" || po.approvalStatus === "FINALIZED",
        })}
        rows={po.lines}
        getRowKey={(l) => String(l.lineNo)}
      />
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
      {versions.error ? <LoadError what="the version history" error={versions.error} onRetry={versions.reload} /> : null}
      <DataTable
        label="Versions"
        columns={versionColumns()}
        rows={versions.error ? [] : (versions.data ?? [])}
        getRowKey={(v) => String(v.versionNo)}
        loading={versions.loading && versions.data === null}
        empty={{ title: "Not confirmed yet", description: "Version 1 is recorded when the order is confirmed; later changes become versions 2, 3 and on." }}
      />
    </div>
  );
}
