import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, ConfirmDialog, DataTable, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  createDeliveryTolerance,
  deleteDeliveryTolerance,
  describeProblem,
  getDeliveryTolerance,
  listDeliveryTolerances,
  newKey,
  PARTIAL_DELIVERY_POLICIES,
  replaceDeliveryTolerance,
  resolveDeliveryTolerance,
  type DeliveryTolerance,
  type DeliveryToleranceFilter,
  type DeliveryToleranceFigures,
  type DeliveryToleranceInput,
  type PartialDelivery,
  type Problem,
  type ResolvedDeliveryTolerance,
  type Versioned,
} from "./api/client";
import { DECIMAL, PERCENT, dash, formatDateTime, sentence, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Changing a delivery tolerance needs this duty (enforced by the service; the route rows are seeded under #313). */
export const DUTY_DELIVERY_TOLERANCE_MAINTAIN = "sales.parameter.maintain";

type Errors = Record<string, string>;

export interface ToleranceValues {
  legalEntityId: string;
  productId: string;
  customerId: string;
  overdeliveryPct: string;
  underdeliveryPct: string;
  partialDelivery: PartialDelivery;
}

const POLICY_HELP: Record<PartialDelivery, string> = {
  ALLOWED: "The order may ship in several deliveries.",
  COMPLETE_ONLY: "Ship only when the whole order can go (the customer's complete delivery flag).",
  ONE_SHIPMENT: "Ship the order once; the rest of any short line is cancelled rather than backordered.",
};

/** "5" → "5%"; the service sends 2-place decimal strings. */
export function percentText(pct: string): string {
  return `${pct}%`;
}

/** Checks that mirror the service (domain.DeliveryTolerance.Validate and api/delivery_tolerances.go). */
export function validateTolerance(values: ToleranceValues, withKey: boolean): { errors: Errors; input?: DeliveryToleranceInput } {
  const errors: Errors = {};
  if (withKey) {
    if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
    if (!UUID.test(values.productId.trim())) errors.productId = "Enter the product's UUID.";
    if (values.customerId.trim() && !UUID.test(values.customerId.trim())) errors.customerId = "Enter the customer's UUID, or leave it empty for every customer.";
  }
  if (!PERCENT.test(values.overdeliveryPct.trim())) errors.overdeliveryPct = "Enter a percent from 0 to 100 with at most 2 decimal places.";
  if (!PERCENT.test(values.underdeliveryPct.trim())) errors.underdeliveryPct = "Enter a percent from 0 to 100 with at most 2 decimal places.";
  if (Object.keys(errors).length > 0) return { errors };
  const customerId = values.customerId.trim();
  return {
    errors,
    input: {
      legalEntityId: values.legalEntityId.trim(),
      productId: values.productId.trim(),
      ...(customerId ? { customerId } : {}),
      overdeliveryPct: values.overdeliveryPct.trim(),
      underdeliveryPct: values.underdeliveryPct.trim(),
      partialDelivery: values.partialDelivery,
    },
  };
}

interface FigureFieldsProps {
  values: ToleranceValues;
  errors: Errors;
  readOnly?: boolean;
  onChange(patch: Partial<ToleranceValues>): void;
}

function FigureFields({ values, errors, readOnly, onChange }: FigureFieldsProps) {
  return (
    <>
      <FormField id="tol-overdeliveryPct" label="Over-delivery tolerance (%)" hint="Most a shipment may exceed the ordered quantity, as a percent of it." error={errors.overdeliveryPct}>
        {(control) => <Input {...control} inputMode="decimal" value={values.overdeliveryPct} readOnly={readOnly} onChange={(e) => onChange({ overdeliveryPct: e.target.value })} invalid={Boolean(errors.overdeliveryPct)} />}
      </FormField>
      <FormField id="tol-underdeliveryPct" label="Under-delivery tolerance (%)" hint="Most a shipment may fall short of the ordered quantity and still complete the line." error={errors.underdeliveryPct}>
        {(control) => <Input {...control} inputMode="decimal" value={values.underdeliveryPct} readOnly={readOnly} onChange={(e) => onChange({ underdeliveryPct: e.target.value })} invalid={Boolean(errors.underdeliveryPct)} />}
      </FormField>
      <FormField id="tol-partialDelivery" label="Partial delivery" hint={POLICY_HELP[values.partialDelivery]}>
        {(control) => (
          <Select {...control} value={values.partialDelivery} disabled={readOnly} onChange={(e) => onChange({ partialDelivery: e.target.value as PartialDelivery })}>
            {PARTIAL_DELIVERY_POLICIES.map((p) => (
              <option key={p} value={p}>
                {sentence(p)}
              </option>
            ))}
          </Select>
        )}
      </FormField>
    </>
  );
}

const columns: DataTableColumn<DeliveryTolerance>[] = [
  { key: "productId", header: "Product", mono: true, render: (t) => <Link to={`/sales/delivery-tolerances/${t.id}`}>{shortId(t.productId)}</Link> },
  { key: "customerId", header: "Customer", mono: true, render: (t) => (t.customerId ? shortId(t.customerId) : "Every customer") },
  { key: "overdeliveryPct", header: "Over", align: "end", render: (t) => percentText(t.overdeliveryPct) },
  { key: "underdeliveryPct", header: "Under", align: "end", render: (t) => percentText(t.underdeliveryPct) },
  { key: "partialDelivery", header: "Partial delivery", render: (t) => sentence(t.partialDelivery) },
  { key: "legalEntityId", header: "Legal entity", mono: true, render: (t) => shortId(t.legalEntityId) },
  { key: "updatedAt", header: "Changed", render: (t) => formatDateTime(t.updatedAt) },
];

/** What a quantity may ship as, for one customer and product: the stateless resolve call (BR-28.7). */
function ResolveCard() {
  const [values, setValues] = useState({ legalEntityId: "", productId: "", customerId: "", orderedQty: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [result, setResult] = useState<ResolvedDeliveryTolerance | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Errors = {};
    if (!UUID.test(values.legalEntityId.trim())) found.legalEntityId = "Enter the legal entity's UUID.";
    if (!UUID.test(values.productId.trim())) found.productId = "Enter the product's UUID.";
    if (!UUID.test(values.customerId.trim())) found.customerId = "Enter the customer's UUID.";
    if (values.orderedQty.trim() && !DECIMAL.test(values.orderedQty.trim())) found.orderedQty = "Enter a quantity with at most 6 decimal places.";
    setErrors(found);
    setFailure(null);
    setResult(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setResult(
        await resolveDeliveryTolerance({
          legalEntityId: values.legalEntityId.trim(),
          productId: values.productId.trim(),
          customerId: values.customerId.trim(),
          orderedQty: values.orderedQty.trim() || undefined,
        }),
      );
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const field = (name: keyof typeof values, label: string, hint?: string) => (
    <FormField id={`resolve-${name}`} label={label} hint={hint} error={errors[name]} optional={name === "orderedQty"}>
      {(control) => <Input {...control} className={styles.mono} value={values[name]} onChange={(e) => setValues({ ...values, [name]: e.target.value })} invalid={Boolean(errors[name])} />}
    </FormField>
  );

  return (
    <Card title="Check what applies">
      <p className={styles.note}>The customer's own rule replaces the rule for every customer, which replaces the default of exact quantities and the legal entity's partial delivery default (set on the sales parameters page, partial delivery allowed until then).</p>
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Check the delivery tolerance">
        {field("legalEntityId", "Legal entity")}
        {field("productId", "Product")}
        {field("customerId", "Customer")}
        {field("orderedQty", "Ordered quantity", "Adds the least and most a shipment may carry.")}
        <div className={styles.formActions}>
          <Button type="submit" loading={busy}>
            Check
          </Button>
        </div>
      </form>
      {failure ? (
        <Alert tone="danger" title="Could not check the tolerance" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <div role="status">
        {result ? (
          <dl className={styles.details}>
            {(
              [
                ["Source", result.source === "DEFAULT" ? "Default (no rule; the legal entity partial delivery default)" : result.source === "CUSTOMER" ? "The customer's own rule" : "The rule for every customer"],
                ["Over-delivery", percentText(result.overdeliveryPct)],
                ["Under-delivery", percentText(result.underdeliveryPct)],
                ["Partial delivery", sentence(result.partialDelivery)],
                ...(result.minQty !== undefined && result.maxQty !== undefined ? [["A shipment may carry", `${result.minQty} to ${result.maxQty}`]] : []),
              ] as [string, string][]
            ).map(([term, value]) => (
              <div key={term} className={styles.entry}>
                <dt className={styles.term}>{term}</dt>
                <dd className={styles.value}>{value}</dd>
              </div>
            ))}
          </dl>
        ) : null}
      </div>
    </Card>
  );
}

/** Delivery tolerances and partial delivery policies per product and customer (BR-27.9, BR-28.7). */
export function DeliveryTolerancesListPage() {
  const canMaintain = holdsDuty(usePrincipalDuties(), DUTY_DELIVERY_TOLERANCE_MAINTAIN);
  const [draft, setDraft] = useState({ legalEntityId: "", productId: "", customerId: "" });
  const [filter, setFilter] = useState<DeliveryToleranceFilter>({});
  const invalid: Errors = {};
  if (draft.legalEntityId.trim() && !UUID.test(draft.legalEntityId.trim())) invalid.legalEntityId = "Enter a legal entity UUID.";
  if (draft.productId.trim() && !UUID.test(draft.productId.trim())) invalid.productId = "Enter a product UUID.";
  if (draft.customerId.trim() && !UUID.test(draft.customerId.trim())) invalid.customerId = "Enter a customer UUID.";
  const state = useLoad((signal) => listDeliveryTolerances(filter, signal), JSON.stringify(filter));

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Delivery tolerances"
        description="How far a shipment of a product may differ from the order for a customer, and whether the order may ship in parts. A product without a rule resolves to exact quantities, in parts if need be. Sales applies these rules when a delivery is recorded on an order (the delivery that completes a line may pass the order by the over-delivery figure; a short delivery marked as the line's last completes it within the under-delivery figure); fulfillment still checks over-picks against its own over-delivery tolerances."
        actions={
          canMaintain ? (
            <Link className={buttonClassName("primary")} to="/sales/delivery-tolerances/new">
              New delivery tolerance
            </Link>
          ) : undefined
        }
      />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          if (Object.keys(invalid).length === 0) setFilter({ legalEntityId: draft.legalEntityId.trim(), productId: draft.productId.trim(), customerId: draft.customerId.trim() });
        }}
      >
        <FormField id="tol-filter-le" label="Legal entity" error={invalid.legalEntityId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })} invalid={Boolean(invalid.legalEntityId)} />}
        </FormField>
        <FormField id="tol-filter-product" label="Product" error={invalid.productId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.productId} onChange={(e) => setDraft({ ...draft, productId: e.target.value })} invalid={Boolean(invalid.productId)} />}
        </FormField>
        <FormField id="tol-filter-customer" label="Customer" error={invalid.customerId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.customerId} onChange={(e) => setDraft({ ...draft, customerId: e.target.value })} invalid={Boolean(invalid.customerId)} />}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load delivery tolerances" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Delivery tolerances"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(t) => t.id}
          loading={state.loading}
          empty={{ title: "No delivery tolerances", description: "Products ship exact quantities, in parts if need be, until a rule says otherwise." }}
        />
      )}
      <ResolveCard />
    </div>
  );
}

/** Sets the tolerance of a product for a customer or for every customer (duty sales.parameter.maintain). */
export function NewDeliveryTolerancePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<ToleranceValues>({ legalEntityId: "", productId: "", customerId: "", overdeliveryPct: "0", underdeliveryPct: "0", partialDelivery: "ALLOWED" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<ToleranceValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateTolerance(values, true);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const saved = await createDeliveryTolerance(input, key.current);
      navigate(`/sales/delivery-tolerances/${saved.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const party = (name: "legalEntityId" | "productId" | "customerId", label: string, hint: string, optional = false) => (
    <FormField id={`tol-${name}`} label={label} hint={hint} error={errors[name]} optional={optional}>
      {(control) => <Input {...control} className={styles.mono} value={values[name]} onChange={(e) => update({ [name]: e.target.value })} invalid={Boolean(errors[name])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New delivery tolerance" description="A product has one rule per customer and one for every customer; change an existing rule from its page." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New delivery tolerance">
          {party("legalEntityId", "Legal entity", "UUID of the selling legal entity.")}
          {party("productId", "Product", "UUID of the product.")}
          {party("customerId", "Customer", "UUID of the customer; empty makes the rule apply to every customer.", true)}
          <FigureFields values={values} errors={errors} onChange={update} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "SLS-0126" ? "This product already has a rule for that customer" : "Could not save the delivery tolerance"} live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save delivery tolerance
            </Button>
            <Link className={buttonClassName("ghost")} to="/sales/delivery-tolerances">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

function valuesOf(t: DeliveryTolerance): ToleranceValues {
  return {
    legalEntityId: t.legalEntityId,
    productId: t.productId,
    customerId: t.customerId ?? "",
    overdeliveryPct: t.overdeliveryPct,
    underdeliveryPct: t.underdeliveryPct,
    partialDelivery: t.partialDelivery,
  };
}

/** One rule, with the change form and the removal. */
export function DeliveryToleranceDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const canMaintain = holdsDuty(usePrincipalDuties(), DUTY_DELIVERY_TOLERANCE_MAINTAIN);
  const state = useLoad((signal) => getDeliveryTolerance(id, signal), id);
  const [latest, setLatest] = useState<Versioned<DeliveryTolerance> | null>(null);
  const [draft, setDraft] = useState<ToleranceValues | null>(null);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState(false);

  const current = latest ?? state.data;
  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Delivery tolerance" />
        <Alert tone="danger" title="Could not load the delivery tolerance" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Delivery tolerance" />
        <span className="visually-hidden">Loading the delivery tolerance…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: rule, etag } = current;
  const values = draft ?? valuesOf(rule);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setSaved(false);
    const { errors: found, input } = validateTolerance(values, false);
    setErrors(found);
    if (!input) return;
    const figures: DeliveryToleranceFigures = { overdeliveryPct: input.overdeliveryPct, underdeliveryPct: input.underdeliveryPct, partialDelivery: input.partialDelivery };
    setBusy(true);
    try {
      const res = await replaceDeliveryTolerance(rule.id, etag, figures);
      setLatest(res);
      setDraft(null);
      setSaved(true);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    setBusy(true);
    setFailure(null);
    try {
      await deleteDeliveryTolerance(rule.id);
      navigate("/sales/delivery-tolerances");
    } catch (error) {
      setConfirming(false);
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const facts: [string, string][] = [
    ["Product", rule.productId],
    ["Customer", rule.customerId ?? "Every customer"],
    ["Legal entity", rule.legalEntityId],
    ["Over-delivery", percentText(rule.overdeliveryPct)],
    ["Under-delivery", percentText(rule.underdeliveryPct)],
    ["Partial delivery", sentence(rule.partialDelivery)],
    ["Changed", `${formatDateTime(rule.updatedAt)} by ${rule.updatedBy}`],
  ];
  const mono = new Set(["Product", "Legal entity"]);

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Delivery tolerance of product ${shortId(rule.productId)}`}
        description={rule.customerId ? undefined : "This rule applies to every customer without a rule of their own."}
        actions={
          <Link className={buttonClassName("ghost")} to="/sales/delivery-tolerances">
            Back to delivery tolerances
          </Link>
        }
      />
      <Card>
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={mono.has(term) || (term === "Customer" && rule.customerId) ? `${styles.value} ${styles.mono}` : styles.value}>{value === "" ? dash : value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <Card title="Change the tolerance">
        <p className={styles.note}>A change applies to what the rule resolves to from now on. Shipping does not read these rules yet, so no shipment changes.</p>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Change the delivery tolerance">
          <FigureFields values={values} errors={errors} readOnly={!canMaintain} onChange={(patch) => setDraft({ ...values, ...patch })} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "SLS-0006" ? "Someone else changed this tolerance" : "Could not change the delivery tolerance"} live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            {canMaintain ? (
              <>
                <Button type="submit" variant="primary" loading={busy}>
                  Save changes
                </Button>
                <Button type="button" variant="danger" onClick={() => setConfirming(true)}>
                  Remove tolerance
                </Button>
              </>
            ) : (
              <span className={styles.muted}>Changing a delivery tolerance needs the {DUTY_DELIVERY_TOLERANCE_MAINTAIN} duty.</span>
            )}
          </div>
        </form>
        <Alert tone="success" live="status">
          {saved ? "The delivery tolerance is saved." : null}
        </Alert>
      </Card>
      {confirming ? (
        <ConfirmDialog
          title="Remove this delivery tolerance?"
          description="The product falls back to the rule for every customer, or to exact quantities with partial delivery allowed."
          confirmLabel="Remove tolerance"
          cancelLabel="Keep it"
          onConfirm={remove}
          onCancel={() => setConfirming(false)}
        />
      ) : null}
    </div>
  );
}
