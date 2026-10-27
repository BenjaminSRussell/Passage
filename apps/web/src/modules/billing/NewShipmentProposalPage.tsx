import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  BillingApiError,
  createShipmentProposals,
  describeProblem,
  newKey,
  type Invoice,
  type Problem,
  type ShipmentProposalInput,
} from "./api/client";
import { ACCOUNT, CURRENCY, DAY, DECIMAL, invoiceName, STATUS_LABELS, TAX_CODE, today, TYPE_LABELS, UUID } from "./format";
import styles from "./pages.module.css";

type Errors = Record<string, string>;

export interface SlipLineValues {
  id: string;
  orderLineId: string;
  productId: string;
  description: string;
  qty: string;
  unitPrice: string;
  discountPct: string;
  revenueAccount: string;
  taxCode: string;
  priceExplainRef: string;
}

export interface SlipValues {
  id: string;
  orderId: string;
  date: string;
  chargeAmount: string;
  lines: SlipLineValues[];
}

export interface ShipmentValues {
  legalEntityId: string;
  customerId: string;
  invoiceAccountId: string;
  currency: string;
  invoiceDate: string;
  paymentTermDays: string;
  consolidate: boolean;
  slips: SlipValues[];
}

const MAX_SLIPS = 50;
const MAX_LINES_PER_SLIP = 50;
const emptyLine: SlipLineValues = {
  id: "",
  orderLineId: "",
  productId: "",
  description: "",
  qty: "",
  unitPrice: "",
  discountPct: "",
  revenueAccount: "",
  taxCode: "",
  priceExplainRef: "",
};

function emptySlip(day: string): SlipValues {
  return { id: "", orderId: "", date: day, chargeAmount: "", lines: [{ ...emptyLine }] };
}

export function emptyShipment(day = today()): ShipmentValues {
  return {
    legalEntityId: "",
    customerId: "",
    invoiceAccountId: "",
    currency: "EUR",
    invoiceDate: day,
    paymentTermDays: "30",
    consolidate: false,
    slips: [emptySlip(day)],
  };
}

/** Client checks that mirror the service (FR-29.1, BR-29.4, BR-29.5); the service has the last word. */
export function validateShipment(v: ShipmentValues): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!UUID.test(v.customerId.trim())) errors.customerId = "Enter the customer id (a UUID).";
  if (!UUID.test(v.invoiceAccountId.trim())) errors.invoiceAccountId = "Enter the invoice account id (a UUID).";
  if (!CURRENCY.test(v.currency.trim())) errors.currency = "Enter a three-letter currency code such as EUR.";
  if (!DAY.test(v.invoiceDate)) errors.invoiceDate = "Enter the invoice date as YYYY-MM-DD.";
  const days = Number(v.paymentTermDays);
  if (!/^\d{1,3}$/.test(v.paymentTermDays.trim()) || days > 365) errors.paymentTermDays = "Enter 0 to 365 days.";
  const slipIds = new Set<string>();
  const lineIds = new Set<string>();
  v.slips.forEach((s, i) => {
    const at = (f: string) => `slips[${i}].${f}`;
    const id = s.id.trim().toLowerCase();
    if (!UUID.test(id)) errors[at("id")] = "Enter the packing slip id (a UUID).";
    else if (slipIds.has(id)) errors[at("id")] = "This packing slip is already in the list.";
    slipIds.add(id);
    if (!UUID.test(s.orderId.trim())) errors[at("orderId")] = "Enter the sales order id (a UUID).";
    if (!DAY.test(s.date)) errors[at("date")] = "Enter the slip's posting date as YYYY-MM-DD.";
    else if (DAY.test(v.invoiceDate) && s.date > v.invoiceDate) errors[at("date")] = "The invoice date may not precede the slip date (BIL-0006).";
    if (s.chargeAmount.trim() !== "" && !DECIMAL.test(s.chargeAmount.trim())) errors[at("chargeAmount")] = "Enter the charge as a number such as 12.50, or leave it empty.";
    s.lines.forEach((l, j) => {
      const ln = (f: string) => `slips[${i}].lines[${j}].${f}`;
      const lineId = l.id.trim().toLowerCase();
      if (!UUID.test(lineId)) errors[ln("id")] = "Enter the packing slip line id (a UUID).";
      else if (lineIds.has(lineId)) errors[ln("id")] = "A packing slip line is invoiced once; this one is already listed.";
      lineIds.add(lineId);
      if (!UUID.test(l.orderLineId.trim())) errors[ln("orderLineId")] = "Enter the order line id (a UUID).";
      if (!UUID.test(l.productId.trim())) errors[ln("productId")] = "Enter the product id (a UUID).";
      if (l.description.trim() === "" || l.description.trim().length > 200) errors[ln("description")] = "Enter a description of up to 200 characters.";
      if (!DECIMAL.test(l.qty.trim()) || Number(l.qty) <= 0) errors[ln("qty")] = "Enter the shipped quantity, above zero.";
      if (!DECIMAL.test(l.unitPrice.trim())) errors[ln("unitPrice")] = "Enter the order line's unit price, such as 50.00.";
      if (l.discountPct.trim() !== "" && (!DECIMAL.test(l.discountPct.trim()) || Number(l.discountPct) > 100)) {
        errors[ln("discountPct")] = "Enter a discount from 0 to 100, or leave it empty.";
      }
      if (!ACCOUNT.test(l.revenueAccount.trim())) errors[ln("revenueAccount")] = "Enter the revenue account, for example 4000.";
      if (!TAX_CODE.test(l.taxCode.trim())) errors[ln("taxCode")] = "Enter the tax code in capitals, for example DE19.";
      if (l.priceExplainRef.trim().length > 200) errors[ln("priceExplainRef")] = "Use at most 200 characters.";
    });
  });
  return errors;
}

export function toShipmentInput(v: ShipmentValues): ShipmentProposalInput {
  const opt = (name: string, value: string) => (value.trim() ? { [name]: value.trim() } : {});
  return {
    legalEntityId: v.legalEntityId.trim(),
    customerId: v.customerId.trim(),
    invoiceAccountId: v.invoiceAccountId.trim(),
    currency: v.currency.trim(),
    invoiceDate: v.invoiceDate,
    paymentTermDays: Number(v.paymentTermDays),
    consolidate: v.consolidate,
    slips: v.slips.map((s) => ({
      id: s.id.trim(),
      orderId: s.orderId.trim(),
      date: s.date,
      ...opt("chargeAmount", s.chargeAmount),
      lines: s.lines.map((l) => ({
        id: l.id.trim(),
        orderLineId: l.orderLineId.trim(),
        productId: l.productId.trim(),
        description: l.description.trim(),
        qty: l.qty.trim(),
        unitPrice: l.unitPrice.trim(),
        ...opt("discountPct", l.discountPct),
        revenueAccount: l.revenueAccount.trim(),
        taxCode: l.taxCode.trim(),
        ...opt("priceExplainRef", l.priceExplainRef),
      })),
    })),
  };
}

/** Why a request was refused, in words the billing clerk can act on (§5.29.18). */
function failureTitle(code: string): string {
  switch (code) {
    case "BIL-0001":
      return "The tax engine is unavailable; nothing was saved";
    case "BIL-0006":
      return "The invoice date is before a packing slip date";
    case "BIL-0007":
      return "A packing slip has more lines than one invoice can hold";
    case "BIL-0104":
      return "A packing slip line is already invoiced";
    default:
      return "Could not create the invoices";
  }
}

const createdColumns: DataTableColumn<Invoice>[] = [
  { key: "number", header: "Invoice", mono: true, render: (row) => invoiceName(row) },
  { key: "type", header: "Type", render: (row) => TYPE_LABELS[row.type] ?? row.type },
  { key: "status", header: "Status", render: (row) => <StatusPill status={STATUS_LABELS[row.status] ?? row.status} /> },
  { key: "lines", header: "Lines", mono: true, align: "end", render: (row) => String(row.lines.length) },
  { key: "gross", header: "Gross", mono: true, align: "end", render: (row) => `${row.totals.gross} ${row.currency}` },
];

type HeaderField = "legalEntityId" | "customerId" | "invoiceAccountId" | "currency" | "invoiceDate" | "paymentTermDays";
type SlipField = Exclude<keyof SlipValues, "lines">;
type FieldOpts = { hint?: string; optional?: boolean; mono?: boolean; decimal?: boolean; type?: string };

/** Invoice shipments (FR-29.1, FR-29.2): turn posted packing slips into invoice proposals, one per order or consolidated. */
export function NewShipmentProposalPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<ShipmentValues>(() => emptyShipment());
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState<Invoice[] | null>(null);
  const key = useRef(newKey());

  function update(patch: Partial<ShipmentValues>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  function setSlip(i: number, patch: Partial<SlipValues>) {
    update({ slips: values.slips.map((s, k) => (k === i ? { ...s, ...patch } : s)) });
  }

  function setLine(i: number, j: number, patch: Partial<SlipLineValues>) {
    setSlip(i, { lines: values.slips[i].lines.map((l, k) => (k === j ? { ...l, ...patch } : l)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateShipment(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const invoices = await createShipmentProposals(toShipmentInput(values), key.current);
      if (invoices.length === 1) navigate(`/billing/invoices/${invoices[0].id}`);
      else setCreated(invoices);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      // A refused request is final; only a network or server failure may be retried with the same key.
      if (error instanceof BillingApiError && error.status < 500) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  function control(id: string, name: string, label: string, value: string, onChange: (v: string) => void, opts: FieldOpts = {}) {
    return (
      <FormField id={id} label={label} hint={opts.hint} optional={opts.optional} error={errors[name]}>
        {(c) => (
          <Input
            {...c}
            type={opts.type}
            className={opts.mono ? styles.mono : undefined}
            inputMode={opts.decimal ? "decimal" : undefined}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            invalid={Boolean(errors[name])}
          />
        )}
      </FormField>
    );
  }

  const header = (id: string, field: HeaderField, label: string, opts: FieldOpts = {}) =>
    control(id, field, label, values[field], (v) => update({ [field]: v } as Partial<ShipmentValues>), opts);
  const slip = (i: number, field: SlipField, label: string, opts: FieldOpts = {}) =>
    control(`sp-slip-${i}-${field}`, `slips[${i}].${field}`, label, values.slips[i][field], (v) => setSlip(i, { [field]: v }), opts);
  const line = (i: number, j: number, field: keyof SlipLineValues, label: string, opts: FieldOpts = {}) =>
    control(`sp-line-${i}-${j}-${field}`, `slips[${i}].lines[${j}].${field}`, label, values.slips[i].lines[j][field], (v) => setLine(i, j, { [field]: v }), opts);

  if (created) {
    return (
      <div className={styles.stack}>
        <PageHeader
          title="Invoice proposals created"
          description={`The packing slips became ${created.length} invoice proposals. Open each to review its tax and post it.`}
          actions={
            <Link className={buttonClassName("ghost")} to="/billing">
              All invoices
            </Link>
          }
        />
        <Alert tone="success" live="status">{`Created ${created.length} invoice proposals.`}</Alert>
        <DataTable
          label="Created invoice proposals"
          columns={createdColumns}
          rows={created}
          getRowKey={(row) => row.id}
          onRowOpen={(row) => navigate(`/billing/invoices/${row.id}`)}
        />
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Invoice shipments"
        description="Turn posted packing slips of one invoice account into invoice proposals: one per sales order, or one consolidated invoice. Each line keeps its packing slip, order line and price explanation for the audit trace."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {header("sp-legal-entity", "legalEntityId", "Legal entity id", { mono: true })}
          {header("sp-customer", "customerId", "Customer id", { mono: true })}
          {header("sp-account", "invoiceAccountId", "Invoice account id", { mono: true, hint: "Every slip in the list bills this account." })}
          {header("sp-currency", "currency", "Currency", { mono: true })}
          {header("sp-date", "invoiceDate", "Invoice date", { type: "date", mono: true, hint: "Not before any packing slip's date." })}
          {header("sp-terms", "paymentTermDays", "Payment terms (days)", { mono: true, hint: "Due date = invoice date + these days." })}
          <FormField id="sp-consolidate" label="Invoicing" hint="Consolidated puts every slip on one invoice; a very long invoice is split at slip boundaries.">
            {(c) => (
              <Select {...c} value={values.consolidate ? "yes" : "no"} onChange={(e) => update({ consolidate: e.target.value === "yes" })}>
                <option value="no">One invoice per sales order</option>
                <option value="yes">One consolidated invoice</option>
              </Select>
            )}
          </FormField>
          {values.slips.map((s, i) => (
            <fieldset key={i} className={styles.lineSet}>
              <legend className={styles.groupsLegend}>{`Packing slip ${i + 1}`}</legend>
              {slip(i, "id", "Packing slip id", { mono: true })}
              {slip(i, "orderId", "Sales order id", { mono: true })}
              {slip(i, "date", "Slip date", { type: "date", mono: true })}
              {slip(i, "chargeAmount", "Slip charge", { optional: true, mono: true, decimal: true, hint: "Freight or handling of this slip." })}
              {s.lines.map((_, j) => (
                <fieldset key={j} className={styles.lineSet}>
                  <legend className={styles.groupsLegend}>{`Slip ${i + 1}, line ${j + 1}`}</legend>
                  {line(i, j, "id", "Slip line id", { mono: true })}
                  {line(i, j, "orderLineId", "Order line id", { mono: true })}
                  {line(i, j, "productId", "Product id", { mono: true })}
                  {line(i, j, "description", "Description")}
                  {line(i, j, "qty", "Shipped quantity", { mono: true, decimal: true })}
                  {line(i, j, "unitPrice", "Unit price", { mono: true, decimal: true })}
                  {line(i, j, "discountPct", "Discount %", { optional: true, mono: true, decimal: true })}
                  {line(i, j, "revenueAccount", "Revenue account", { mono: true })}
                  {line(i, j, "taxCode", "Tax code", { mono: true })}
                  {line(i, j, "priceExplainRef", "Price explanation", { optional: true, mono: true, hint: "Reference from pricing, shown on the trace." })}
                  {s.lines.length > 1 ? (
                    <Button variant="ghost" size="sm" onClick={() => setSlip(i, { lines: s.lines.filter((_, k) => k !== j) })}>
                      {`Remove slip ${i + 1} line ${j + 1}`}
                    </Button>
                  ) : null}
                </fieldset>
              ))}
              <div className={styles.formActions}>
                {s.lines.length < MAX_LINES_PER_SLIP ? (
                  <Button variant="secondary" size="sm" onClick={() => setSlip(i, { lines: [...s.lines, { ...emptyLine }] })}>
                    {`Add line to slip ${i + 1}`}
                  </Button>
                ) : null}
                {values.slips.length > 1 ? (
                  <Button variant="ghost" size="sm" onClick={() => update({ slips: values.slips.filter((_, k) => k !== i) })}>
                    {`Remove packing slip ${i + 1}`}
                  </Button>
                ) : null}
              </div>
            </fieldset>
          ))}
          <div className={styles.formActions}>
            {values.slips.length < MAX_SLIPS ? (
              <Button variant="secondary" onClick={() => update({ slips: [...values.slips, emptySlip(values.invoiceDate)] })}>
                Add packing slip
              </Button>
            ) : null}
            <span className={styles.note}>A packing slip line is invoiced once. Net, tax and gross are calculated by the service with per-line rounding.</span>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failureTitle(failure.code)} live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create proposals
            </Button>
            <Link className={buttonClassName("ghost")} to="/billing">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
