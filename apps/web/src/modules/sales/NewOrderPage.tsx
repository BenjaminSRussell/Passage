import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createOrder, describeProblem, newKey, ORDER_TYPES, type NewOrder, type OrderType, type Problem } from "./api/client";
import { DECIMAL, sentence, today, UUID } from "./format";
import styles from "./pages.module.css";

export interface LineValues {
  productId: string;
  quantity: string;
  unit: string;
  siteId: string;
  warehouseId: string;
  unitPrice: string;
  discPct: string;
  requestedShipDate: string;
}

export interface OrderValues {
  legalEntityId: string;
  customerId: string;
  invoiceAccountId: string;
  orderType: OrderType;
  currency: string;
  requestedShipDate: string;
  externalRef: string;
  lines: LineValues[];
}

type Errors = Record<string, string>;

const DATE = /^\d{4}-\d{2}-\d{2}$/;

export function emptyLine(date: string): LineValues {
  return { productId: "", quantity: "1", unit: "EA", siteId: "", warehouseId: "", unitPrice: "", discPct: "0", requestedShipDate: date };
}

/** Checks that mirror the service (api/orders.go buildOrder). Keys are the service's field paths. */
export function validateOrder(values: OrderValues): { errors: Errors; input?: NewOrder } {
  const errors: Errors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (!UUID.test(values.customerId.trim())) errors.customerId = "Enter the customer's UUID.";
  const invoice = values.invoiceAccountId.trim() || values.customerId.trim();
  if (!UUID.test(invoice)) errors.invoiceAccountId = "Enter the invoice account's UUID, or leave it empty to invoice the customer.";
  if (!/^[A-Z]{3}$/.test(values.currency)) errors.currency = "Enter a three-letter ISO currency code, such as USD.";
  if (!DATE.test(values.requestedShipDate)) errors.requestedShipDate = "Choose the requested ship date.";
  const ref = values.externalRef.trim();
  if (ref.length > 100) errors.externalRef = "Use at most 100 characters.";
  if (values.lines.length === 0) errors.lines = "Add at least one line.";
  values.lines.forEach((l, i) => {
    const at = (name: string) => `lines[${i}].${name}`;
    if (!UUID.test(l.productId.trim())) errors[at("productId")] = "Enter the product's UUID.";
    if (!DECIMAL.test(l.quantity) || Number(l.quantity) <= 0) errors[at("quantity")] = "Enter a quantity above 0 with up to 6 decimals.";
    if (l.unit.trim().length < 1 || l.unit.trim().length > 10) errors[at("unit")] = "Enter a unit of up to 10 characters.";
    if (!UUID.test(l.siteId.trim())) errors[at("siteId")] = "Enter the site's UUID.";
    if (!UUID.test(l.warehouseId.trim())) errors[at("warehouseId")] = "Enter the warehouse's UUID.";
    if (!DECIMAL.test(l.unitPrice)) errors[at("unitPrice")] = "Enter a price of 0 or more.";
    if (!DECIMAL.test(l.discPct) || Number(l.discPct) > 100) errors[at("discPct")] = "Enter a discount from 0 to 100.";
    if (!DATE.test(l.requestedShipDate)) errors[at("requestedShipDate")] = "Choose the line's requested ship date.";
  });
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      legalEntityId: values.legalEntityId.trim(),
      customerId: values.customerId.trim(),
      invoiceAccountId: invoice,
      orderType: values.orderType,
      currency: values.currency,
      requestedShipDate: values.requestedShipDate,
      ...(ref ? { externalRef: ref } : {}),
      lines: values.lines.map((l) => ({
        productId: l.productId.trim(),
        quantity: l.quantity,
        unit: l.unit.trim(),
        siteId: l.siteId.trim(),
        warehouseId: l.warehouseId.trim(),
        unitPrice: l.unitPrice,
        discPct: l.discPct,
        requestedShipDate: l.requestedShipDate,
      })),
    },
  };
}

const lineFields: { name: keyof LineValues; label: string; mono?: boolean; type?: string; inputMode?: "decimal" }[] = [
  { name: "productId", label: "Product", mono: true },
  { name: "quantity", label: "Quantity", inputMode: "decimal" },
  { name: "unit", label: "Unit" },
  { name: "unitPrice", label: "Unit price", inputMode: "decimal" },
  { name: "discPct", label: "Discount %", inputMode: "decimal" },
  { name: "siteId", label: "Site", mono: true },
  { name: "warehouseId", label: "Warehouse", mono: true },
  { name: "requestedShipDate", label: "Requested ship date", type: "date" },
];

/** Order entry: header and lines, stored as a DRAFT order (FR-27.2). */
export function NewOrderPage() {
  const navigate = useNavigate();
  const start = today();
  const [values, setValues] = useState<OrderValues>({
    legalEntityId: "",
    customerId: "",
    invoiceAccountId: "",
    orderType: "ORDER",
    currency: "USD",
    requestedShipDate: start,
    externalRef: "",
    lines: [emptyLine(start)],
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<OrderValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  function updateLine(i: number, patch: Partial<LineValues>) {
    update({ lines: values.lines.map((l, idx) => (idx === i ? { ...l, ...patch } : l)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateOrder(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createOrder(input, key.current);
      navigate(`/sales/orders/${created.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const header = (name: keyof Omit<OrderValues, "lines" | "orderType">, label: string, hint?: string, extra: { type?: string; mono?: boolean; optional?: boolean } = {}) => (
    <FormField id={`order-${name}`} label={label} hint={hint} error={errors[name]} optional={extra.optional}>
      {(control) => (
        <Input
          {...control}
          type={extra.type ?? "text"}
          className={extra.mono ? styles.mono : undefined}
          value={values[name]}
          onChange={(e) => update({ [name]: name === "currency" ? e.target.value.toUpperCase() : e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New sales order"
        description="The order is saved as a draft. Confirm it from the order page; confirmation runs the credit check."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New sales order">
          {header("legalEntityId", "Legal entity", "UUID of the selling legal entity.", { mono: true })}
          {header("customerId", "Customer", "UUID of the customer party.", { mono: true })}
          {header("invoiceAccountId", "Invoice account", "Leave empty to invoice the customer.", { mono: true, optional: true })}
          <FormField id="order-orderType" label="Order type" error={errors.orderType}>
            {(control) => (
              <Select {...control} value={values.orderType} onChange={(e) => update({ orderType: e.target.value as OrderType })}>
                {ORDER_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {header("currency", "Currency")}
          {header("requestedShipDate", "Requested ship date", undefined, { type: "date" })}
          {header("externalRef", "Customer reference", "For example the customer's PO number.", { optional: true })}
          <div className={`${styles.formActions} ${styles.lines}`}>
            {values.lines.map((line, i) => (
              <fieldset key={i} className={styles.lineCard}>
                <legend className={styles.lineLegend}>Line {i + 1}</legend>
                {lineFields.map((f) => {
                  const path = `lines[${i}].${f.name}`;
                  return (
                    <FormField key={f.name} id={`line-${i}-${f.name}`} label={f.label} error={errors[path]}>
                      {(control) => (
                        <Input
                          {...control}
                          type={f.type ?? "text"}
                          inputMode={f.inputMode}
                          className={f.mono ? styles.mono : undefined}
                          value={line[f.name]}
                          onChange={(e) => updateLine(i, { [f.name]: e.target.value })}
                          invalid={Boolean(errors[path])}
                        />
                      )}
                    </FormField>
                  );
                })}
                {values.lines.length > 1 ? (
                  <div className={styles.formActions}>
                    <Button size="sm" variant="ghost" onClick={() => update({ lines: values.lines.filter((_, idx) => idx !== i) })}>
                      Remove line {i + 1}
                    </Button>
                  </div>
                ) : null}
              </fieldset>
            ))}
            {errors.lines ? (
              <p role="alert" className={styles.fieldError}>
                {errors.lines}
              </p>
            ) : null}
            <div>
              <Button icon="box" onClick={() => update({ lines: [...values.lines, emptyLine(values.requestedShipDate)] })}>
                Add line
              </Button>
            </div>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the order" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save draft order
            </Button>
            <Link className={buttonClassName("ghost")} to="/sales/orders">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
