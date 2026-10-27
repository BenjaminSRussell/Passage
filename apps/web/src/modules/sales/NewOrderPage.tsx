import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createOrder, describeProblem, newKey, ORDER_TYPES, type NewOrder, type OrderType, type Problem, type Proposal } from "./api/client";
import { sentence, today, UUID } from "./format";
import { emptyLine, validateLines, type LineValues } from "./lines";
import { LinesEditor } from "./LinesEditor";
import { OrderProposals } from "./OrderProposals";
import styles from "./pages.module.css";

export { emptyLine, type LineValues } from "./lines";

export interface OrderValues {
  legalEntityId: string;
  customerId: string;
  invoiceAccountId: string;
  orderType: OrderType;
  currency: string;
  requestedShipDate: string;
  externalRef: string;
  customerGroup: string;
  listingOverrideReason: string;
  lines: LineValues[];
}

type Errors = Record<string, string>;

const DATE = /^\d{4}-\d{2}-\d{2}$/;

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
  const group = values.customerGroup.trim();
  if (group.length > 80) errors.customerGroup = "Use at most 80 characters.";
  const override = values.listingOverrideReason.trim();
  if (override.length > 500) errors.listingOverrideReason = "Use at most 500 characters.";
  const lines = validateLines(values.lines, errors);
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
      ...(group ? { customerGroup: group } : {}),
      ...(override ? { listingOverrideReason: override } : {}),
      lines,
    },
  };
}

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
    customerGroup: "",
    listingOverrideReason: "",
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

  /** An accepted proposal becomes a line for the proposed product, shipped with the line that ordered it; its price is entered by hand. */
  function acceptProposal(proposal: Proposal, ordered: LineValues) {
    const line: LineValues = { ...emptyLine(ordered.requestedShipDate), productId: proposal.proposedProductId, unit: ordered.unit, siteId: ordered.siteId, warehouseId: ordered.warehouseId };
    setValues((v) => ({ ...v, lines: [...v.lines, line] }));
    key.current = newKey();
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
      navigate(`/sales/orders/${created.value.id}`, { state: { warnings: created.value.warnings ?? [] } });
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
          {header("customerGroup", "Customer group", "Applies group listing entries, for example EU-RETAIL.", { optional: true })}
          {header("listingOverrideReason", "Listing override reason", "Needs the sales.order.approve duty. Accepts lines the product listing denies and logs the reason.", { optional: true })}
          <LinesEditor lines={values.lines} errors={errors} newLineDate={values.requestedShipDate} onChange={(lines) => update({ lines })} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "SLS-0011" ? "A product is not listed for this customer" : "Could not save the order"} live="alert">
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
      <OrderProposals legalEntityId={values.legalEntityId} customerId={values.customerId} lines={values.lines} onAccept={acceptProposal} />
    </div>
  );
}
