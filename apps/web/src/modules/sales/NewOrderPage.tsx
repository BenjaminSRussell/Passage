import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, useFormBaseline, useUnsavedChanges } from "../../shell/ui";
import { createOrder, describeProblem, getCustomerGroup, newKey, ORDER_TYPES, type NewOrder, type OrderType, type Problem, type Proposal } from "./api/client";
import { sentence, today, UUID } from "./format";
import { emptyLine, validateLines, type LineValues } from "./lines";
import { emptyOneTime, OneTimeFields, validateOneTime, type OneTimeValues } from "./OneTimeFields";
import { LinesEditor } from "./LinesEditor";
import { OrderProposals } from "./OrderProposals";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export { emptyLine, type LineValues } from "./lines";

export interface OrderValues {
  legalEntityId: string;
  customerId: string;
  invoiceAccountId: string;
  /** Empty: the legal entity's default order type (Sales parameters) applies. */
  orderType: OrderType | "";
  currency: string;
  requestedShipDate: string;
  externalRef: string;
  listingOverrideReason: string;
  /** Present for a one-time customer order (FR-27.22): the walk-in buyer. */
  oneTime?: OneTimeValues;
  lines: LineValues[];
}

type Errors = Record<string, string>;

const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** Checks that mirror the service (api/orders.go buildOrder). Keys are the service's field paths. */
export function validateOrder(values: OrderValues): { errors: Errors; input?: NewOrder } {
  const errors: Errors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Choose the selling legal entity.";
  if (!UUID.test(values.customerId.trim())) errors.customerId = "Choose the customer.";
  const invoice = values.invoiceAccountId.trim() || values.customerId.trim();
  if (!UUID.test(invoice)) errors.invoiceAccountId = "Choose the invoice account, or leave it empty to invoice the customer.";
  if (!/^[A-Z]{3}$/.test(values.currency)) errors.currency = "Enter a three-letter ISO currency code, such as USD.";
  if (!DATE.test(values.requestedShipDate)) errors.requestedShipDate = "Choose the requested ship date.";
  const ref = values.externalRef.trim();
  if (ref.length > 100) errors.externalRef = "Use at most 100 characters.";
  const override = values.listingOverrideReason.trim();
  if (override.length > 500) errors.listingOverrideReason = "Use at most 500 characters.";
  const oneTime = values.oneTime ? validateOneTime(values.oneTime, errors) : undefined;
  const lines = validateLines(values.lines, errors);
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      legalEntityId: values.legalEntityId.trim(),
      customerId: values.customerId.trim(),
      invoiceAccountId: invoice,
      ...(values.orderType ? { orderType: values.orderType } : {}),
      currency: values.currency,
      requestedShipDate: values.requestedShipDate,
      ...(ref ? { externalRef: ref } : {}),
      ...(override ? { listingOverrideReason: override } : {}),
      ...(oneTime ? { oneTime } : {}),
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
    orderType: "",
    currency: "USD",
    requestedShipDate: start,
    externalRef: "",
    listingOverrideReason: "",
    lines: [emptyLine(start)],
  });
  // The customer's group, which pricing's group conditions key on; read once per customer for the price prefill.
  const [customerGroup, setCustomerGroup] = useState("");
  useEffect(() => {
    const customerId = values.customerId.trim();
    setCustomerGroup("");
    if (!UUID.test(customerId)) return;
    const controller = new AbortController();
    getCustomerGroup(customerId, controller.signal).then(
      (group) => {
        if (!controller.signal.aborted) setCustomerGroup(group);
      },
      () => undefined,
    );
    return () => controller.abort();
  }, [values.customerId]);
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

  const { prompt, release } = useUnsavedChanges(useFormBaseline(values));
  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateOrder(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createOrder(input, key.current);
      release();
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

  const header = (name: keyof Omit<OrderValues, "lines" | "orderType" | "oneTime">, label: string, hint?: string, extra: { type?: string; mono?: boolean; optional?: boolean } = {}) => (
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

  const lookup = (name: "legalEntityId" | "customerId" | "invoiceAccountId", entity: "legalEntity" | "customer", label: string, hint: string, scope: { legalEntityId?: string } = {}, optional = false) => (
    <FormField id={`order-${name}`} label={label} hint={hint} error={errors[name]} optional={optional}>
      {(control) => (
        <EntityLookup
          entity={entity}
          {...control}
          scope={scope}
          value={values[name]}
          onChange={(e) => update({ [name]: e.target.value })}
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
          {lookup("legalEntityId", "legalEntity", "Legal entity", "The selling legal entity.")}
          {lookup("customerId", "customer", "Customer", "Search by name or account number.", { legalEntityId: values.legalEntityId || undefined })}
          {lookup("invoiceAccountId", "customer", "Invoice account", "Leave empty to invoice the customer.", { legalEntityId: values.legalEntityId || undefined }, true)}
          <FormField id="order-orderType" label="Order type" hint="Legal entity default uses the type set in Sales parameters." error={errors.orderType}>
            {(control) => (
              <Select {...control} value={values.orderType} onChange={(e) => update({ orderType: e.target.value as OrderType | "" })}>
                <option value="">Legal entity default</option>
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
          {header("listingOverrideReason", "Listing override reason", "Needs the sales.order.approve duty. Accepts lines the product listing denies and logs the reason.", { optional: true })}
          <FormField id="order-oneTime" label="One-time customer" hint="Walk-in buyer sold to the shared one-time account, with the name and address on the invoice." optional>
            {(control) => (
              <Input
                {...control}
                type="checkbox"
                checked={values.oneTime !== undefined}
                onChange={(e) => update({ oneTime: e.target.checked ? emptyOneTime : undefined })}
              />
            )}
          </FormField>
          {values.oneTime ? <OneTimeFields values={values.oneTime} errors={errors} onChange={(oneTime) => update({ oneTime })} /> : null}
          <LinesEditor
            lines={values.lines}
            errors={errors}
            legalEntityId={values.legalEntityId}
            pricing={{ customerId: values.customerId, customerGroupId: customerGroup, currency: values.currency }}
            newLineDate={values.requestedShipDate}
            onChange={(lines) => update({ lines })}
            withDeliveryControl
          />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "SLS-0011" ? "A product is not listed for this customer" : failure.code === "SLS-0016" ? "The order is over the one-time order limit" : "Could not save the order"} live="alert">
                <ProblemMessage problem={failure} />
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
      {prompt}
      </Card>
      <OrderProposals legalEntityId={values.legalEntityId} customerId={values.customerId} lines={values.lines} onAccept={acceptProposal} />
    </div>
  );
}
