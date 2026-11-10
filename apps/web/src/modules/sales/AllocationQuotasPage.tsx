import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import {
  createAllocationQuota,
  describeProblem,
  listAllocationQuotas,
  newKey,
  type AllocationFilter,
  type AllocationQuota,
  type AllocationQuotaInput,
  type Problem,
} from "./api/client";
import { DECIMAL, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const DATE = /^\d{4}-\d{2}-\d{2}$/;

export interface QuotaValues {
  legalEntityId: string;
  productId: string;
  customerId: string;
  periodStart: string;
  periodEnd: string;
  quotaQty: string;
}

type Errors = Record<string, string>;

/** Checks that mirror the service (api/allocations.go buildAllocationQuota). */
export function validateQuota(values: QuotaValues): { errors: Errors; input?: AllocationQuotaInput } {
  const errors: Errors = {};
  const customer = values.customerId.trim();
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (!UUID.test(values.productId.trim())) errors.productId = "Enter the product's UUID.";
  if (customer && !UUID.test(customer)) errors.customerId = "Enter the customer's UUID, or leave it empty for every customer.";
  if (!DATE.test(values.periodStart)) errors.periodStart = "Choose the first day of the period.";
  if (!DATE.test(values.periodEnd)) errors.periodEnd = "Choose the last day of the period.";
  if (DATE.test(values.periodStart) && DATE.test(values.periodEnd) && values.periodEnd < values.periodStart) errors.periodEnd = "The end must not be before the start.";
  if (!DECIMAL.test(values.quotaQty) || /^0+(\.0+)?$/.test(values.quotaQty)) errors.quotaQty = "Enter a quantity above 0 with up to 6 decimals.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      legalEntityId: values.legalEntityId.trim(),
      productId: values.productId.trim(),
      ...(customer ? { customerId: customer } : {}),
      periodStart: values.periodStart,
      periodEnd: values.periodEnd,
      quotaQty: values.quotaQty,
    },
  };
}

/** Share of the quota drawn, for display only (quantities stay decimal strings everywhere else). */
function usedShare(q: AllocationQuota): string {
  const quota = Number(q.quotaQty);
  return quota > 0 ? `${((Number(q.consumedQty) / quota) * 100).toFixed(1)}%` : "0.0%";
}

const columns: DataTableColumn<AllocationQuota>[] = [
  { key: "productId", header: "Product", mono: true, render: (q) => <Link to={`/sales/allocation-quotas/${q.id}`}>{shortId(q.productId)}</Link> },
  { key: "customerId", header: "Customer", mono: true, render: (q) => (q.customerId ? shortId(q.customerId) : "Every customer") },
  { key: "period", header: "Period", render: (q) => `${q.periodStart} to ${q.periodEnd}` },
  { key: "quotaQty", header: "Quota", align: "end" },
  { key: "consumedQty", header: "Consumed", align: "end" },
  { key: "remainingQty", header: "Remaining", align: "end" },
  { key: "used", header: "Used", align: "end", render: usedShare },
];

/** Quota inquiry per product and period (PRD §5.27.14): quota, consumed and remaining (FR-27.16). */
export function AllocationQuotasPage() {
  const [draft, setDraft] = useState<AllocationFilter>({ legalEntityId: "", productId: "", customerId: "", onDate: "" });
  const [filter, setFilter] = useState<AllocationFilter>({});
  const invalid: Errors = {};
  if (draft.legalEntityId?.trim() && !UUID.test(draft.legalEntityId.trim())) invalid.legalEntityId = "Enter a legal entity UUID.";
  if (draft.productId?.trim() && !UUID.test(draft.productId.trim())) invalid.productId = "Enter a product UUID.";
  if (draft.customerId?.trim() && !UUID.test(draft.customerId.trim())) invalid.customerId = "Enter a customer UUID.";
  const state = useLoad((signal) => listAllocationQuotas(filter, signal), JSON.stringify(filter));

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Allocation quotas"
        description="How much of a product each customer, or every customer, may have confirmed in a period. Confirming an order draws on the quota of its requested period; when that period is used up, the line moves to the next period with quota left."
        actions={
          <Link className={buttonClassName("primary")} to="/sales/allocation-quotas/new">
            New quota
          </Link>
        }
      />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          if (Object.keys(invalid).length === 0) {
            setFilter({ legalEntityId: draft.legalEntityId?.trim(), productId: draft.productId?.trim(), customerId: draft.customerId?.trim(), onDate: draft.onDate });
          }
        }}
      >
        <FormField id="quotas-le" label="Legal entity" error={invalid.legalEntityId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })} invalid={Boolean(invalid.legalEntityId)} />}
        </FormField>
        <FormField id="quotas-product" label="Product" error={invalid.productId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.productId} onChange={(e) => setDraft({ ...draft, productId: e.target.value })} invalid={Boolean(invalid.productId)} />}
        </FormField>
        <FormField id="quotas-customer" label="Customer" error={invalid.customerId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.customerId} onChange={(e) => setDraft({ ...draft, customerId: e.target.value })} invalid={Boolean(invalid.customerId)} />}
        </FormField>
        <FormField id="quotas-date" label="Period containing" optional>
          {(control) => <Input {...control} type="date" value={draft.onDate} onChange={(e) => setDraft({ ...draft, onDate: e.target.value })} />}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load allocation quotas" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Allocation quotas"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(q) => q.id}
          loading={state.loading}
          empty={{ title: "No allocation quotas", description: "A product without a quota for the requested date is not limited." }}
        />
      )}
    </div>
  );
}

/** Sets one quota (FR-27.16, duty sales.allocation.maintain). */
export function NewAllocationQuotaPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<QuotaValues>({ legalEntityId: "", productId: "", customerId: "", periodStart: "", periodEnd: "", quotaQty: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<QuotaValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateQuota(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const saved = await createAllocationQuota(input, key.current);
      navigate(`/sales/allocation-quotas/${saved.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const field = (name: keyof QuotaValues, label: string, extra: { type?: string; mono?: boolean; decimal?: boolean; hint?: string; optional?: boolean } = {}) => (
    <FormField id={`quota-${name}`} label={label} hint={extra.hint} error={errors[name]} optional={extra.optional}>
      {(control) => (
        <Input
          {...control}
          type={extra.type ?? "text"}
          inputMode={extra.decimal ? "decimal" : undefined}
          className={extra.mono ? styles.mono : undefined}
          value={values[name]}
          onChange={(e) => update({ [name]: e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New allocation quota" description="Cap the quantity of a product that can be confirmed in a period. A customer's own quota replaces the quota for every customer." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New allocation quota">
          {field("legalEntityId", "Legal entity", { mono: true, hint: "UUID of the selling legal entity." })}
          {field("productId", "Product", { mono: true, hint: "UUID of the product." })}
          {field("customerId", "Customer", { mono: true, optional: true, hint: "Leave empty for a quota shared by every customer." })}
          {field("periodStart", "Period start", { type: "date" })}
          {field("periodEnd", "Period end", { type: "date", hint: "The last day of the period, inclusive." })}
          {field("quotaQty", "Quota quantity", { decimal: true })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "SLS-0035" ? "The period overlaps another quota" : "Could not save the quota"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save quota
            </Button>
            <Link className={buttonClassName("ghost")} to="/sales/allocation-quotas">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
