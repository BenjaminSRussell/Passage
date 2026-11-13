import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  FormField,
  Input,
  ListPage,
  loadedRows,
  PageHeader,
  Select,
  StatusPill,
  type DataTableColumn,
  type ListData,
  type ListFilterDef,
  type ListQuery,
  FormErrorSummary,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type FormFieldTarget,
  FormSection,
} from "../../shell/ui";
import {
  AGREEMENT_STATUSES,
  COMMITMENT_TYPES,
  createAgreement,
  describeProblem,
  listAgreements,
  newKey,
  SalesApiError,
  type Agreement,
  type AgreementFilter,
  type AgreementInput,
  type AgreementStatus,
  type CommitmentType,
  type Problem,
} from "./api/client";
import { commitmentText, dash, DECIMAL, money, PERCENT, sentence, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";

const DATE = /^\d{4}-\d{2}-\d{2}$/;
const PRICE = /^\d+(\.\d{1,4})?$/;
const PERCENT_USED = /^\d{1,4}(\.\d{1,2})?$/;

export interface AgreementValues {
  legalEntityId: string;
  customerId: string;
  invoiceAccountId: string;
  productId: string;
  unit: string;
  unitPrice: string;
  currency: string;
  /** QUANTITY when absent. */
  commitmentType?: CommitmentType;
  commitmentQty: string;
  /** Used when commitmentType is AMOUNT. */
  commitmentAmount?: string;
  validFrom: string;
  validTo: string;
  cumTolerance: string;
  overdeliveryPct: string;
}

type Errors = Record<string, string>;

/** Checks that mirror the service (api/agreements.go buildAgreement). */
export function validateAgreement(values: AgreementValues): { errors: Errors; input?: AgreementInput } {
  const errors: Errors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (!UUID.test(values.customerId.trim())) errors.customerId = "Enter the customer's UUID.";
  const invoice = values.invoiceAccountId.trim();
  if (invoice && !UUID.test(invoice)) errors.invoiceAccountId = "Enter the invoice account's UUID, or leave it empty to invoice the customer.";
  if (!UUID.test(values.productId.trim())) errors.productId = "Enter the product's UUID.";
  if (values.unit.trim().length < 1 || values.unit.trim().length > 10) errors.unit = "Enter a unit of up to 10 characters.";
  if (!PRICE.test(values.unitPrice)) errors.unitPrice = "Enter a price of 0 or more with up to 4 decimals.";
  if (!/^[A-Z]{3}$/.test(values.currency)) errors.currency = "Enter a three-letter ISO currency code, such as USD.";
  const byAmount = values.commitmentType === "AMOUNT";
  if (byAmount) {
    const amount = values.commitmentAmount ?? "";
    if (!PRICE.test(amount) || /^0+(\.0+)?$/.test(amount)) errors.commitmentAmount = "Enter an amount above 0 with up to 4 decimals.";
    if (/^0+(\.0+)?$/.test(values.unitPrice)) errors.unitPrice = "An amount commitment needs a price above 0.";
  } else if (!DECIMAL.test(values.commitmentQty) || /^0+(\.0+)?$/.test(values.commitmentQty)) errors.commitmentQty = "Enter a quantity above 0 with up to 6 decimals.";
  if (values.validFrom && !DATE.test(values.validFrom)) errors.validFrom = "Choose a date.";
  if (values.validTo && !DATE.test(values.validTo)) errors.validTo = "Choose a date.";
  if (values.cumTolerance !== "" && !DECIMAL.test(values.cumTolerance)) errors.cumTolerance = "Enter 0 or more with up to 6 decimals, or leave it empty for 0.";
  if (values.overdeliveryPct !== "" && !PERCENT.test(values.overdeliveryPct)) errors.overdeliveryPct = "Enter a percent from 0 to 100 with up to 2 decimals, or leave it empty for 0.";
  if (values.validFrom && values.validTo && values.validTo < values.validFrom) errors.validTo = "The end must not be before the start.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      legalEntityId: values.legalEntityId.trim(),
      customerId: values.customerId.trim(),
      ...(invoice ? { invoiceAccountId: invoice } : {}),
      productId: values.productId.trim(),
      unit: values.unit.trim(),
      unitPrice: values.unitPrice,
      currency: values.currency,
      ...(byAmount ? { commitmentType: "AMOUNT" as const, commitmentAmount: values.commitmentAmount } : { commitmentQty: values.commitmentQty }),
      ...(values.validFrom ? { validFrom: values.validFrom } : {}),
      ...(values.validTo ? { validTo: values.validTo } : {}),
      ...(values.cumTolerance !== "" ? { cumTolerance: values.cumTolerance } : {}),
      ...(values.overdeliveryPct !== "" ? { overdeliveryPct: values.overdeliveryPct } : {}),
    },
  };
}

const columns: DataTableColumn<Agreement>[] = [
  { key: "agreementNumber", header: "Agreement", mono: true },
  { key: "customerId", header: "Customer", render: (a) => <EntityName entity="customer" id={a.customerId} /> },
  { key: "productId", header: "Product", render: (a) => <EntityName entity="product" id={a.productId} /> },
  { key: "unitPrice", header: "Price", align: "end", render: (a) => `${money(a.unitPrice, a.currency)} / ${a.unit}` },
  { key: "commitmentQty", header: "Committed", align: "end", render: (a) => commitmentText(a, a.commitmentQty) },
  { key: "releasedQty", header: "Released", align: "end", render: (a) => commitmentText(a, a.releasedQty) },
  { key: "remainingQty", header: "Remaining", align: "end", render: (a) => commitmentText(a, a.remainingQty) },
  { key: "usedPct", header: "Used", align: "end", render: (a) => (a.usedPct === undefined ? dash : `${a.usedPct}%`) },
  { key: "validity", header: "Valid", render: (a) => (a.validFrom || a.validTo ? `${a.validFrom ?? "…"} to ${a.validTo ?? "…"}` : "Open-ended") },
  { key: "status", header: "Status", render: (a) => <StatusPill status={a.status} /> },
];

/** Sales agreements and releases (PRD §5.27.14): commitment, released and remaining per agreement (FR-27.10). */
const agreementFilters: ListFilterDef[] = [
  { kind: "entity", key: "legalEntityId", label: "Legal entity", entity: "legalEntity" },
  { kind: "entity", key: "customerId", label: "Customer", entity: "customer" },
  { kind: "select", key: "status", label: "Status", anyLabel: "Any status", options: AGREEMENT_STATUSES.map((s) => ({ value: s, label: sentence(s) })) },
  { kind: "text", key: "expiringBy", label: "Ending by (YYYY-MM-DD)" },
  { kind: "text", key: "minUsedPct", label: "Used at least (%)" },
];

function useAgreementRows(query: ListQuery): ListData<Agreement> {
  const f = query.filters;
  const filter: AgreementFilter = {
    legalEntityId: f.legalEntityId ?? "",
    customerId: f.customerId ?? "",
    status: (f.status ?? "") as AgreementStatus | "",
    expiringBy: f.expiringBy ?? "",
    minUsedPct: f.minUsedPct ?? "",
  };
  const state = useLoad(async (signal) => {
    if (filter.expiringBy && !DATE.test(filter.expiringBy)) throw new SalesApiError(400, "SAL-FILTER-DATE", "Enter the end date as YYYY-MM-DD.", {});
    if (filter.minUsedPct && !PERCENT_USED.test(filter.minUsedPct)) throw new SalesApiError(400, "SAL-FILTER-PERCENT", "Enter a percent such as 80 or 92.5.", {});
    return listAgreements(filter, signal);
  }, JSON.stringify(filter));
  return loadedRows(state);
}

export function AgreementsPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="sales-agreements"
      title="Sales agreements"
      description="Blanket commitments, by quantity or by amount, at an agreed price. Each release creates a draft order and draws down the commitment; a release beyond it and its over-delivery tolerance is refused."
      actions={
        <Link className={buttonClassName("primary")} to="/sales/agreements/new">
          New agreement
        </Link>
      }
      noun="sales agreements"
      singular="sales agreement"
      columns={columns}
      filters={agreementFilters}
      useRows={useAgreementRows}
      search={(a) => ({ number: a.agreementNumber })}
      getRowKey={(a) => a.id}
      onRowOpen={(a) => navigate(`/sales/agreements/${a.id}`)}
      empty={{ title: "No sales agreements yet", description: "Create an agreement to commit a customer to a quantity at an agreed price." }}
    />
  );
}

const NEW_AGREEMENT_FIELDS: Record<string, FormFieldTarget> = {
  currency: { id: "agreement-currency", label: "Currency" },
  legalEntityId: { id: "agreement-legalEntityId", label: "Legal entity" },
  customerId: { id: "agreement-customerId", label: "Customer" },
  invoiceAccountId: { id: "agreement-invoiceAccountId", label: "Invoice account" },
  productId: { id: "agreement-productId", label: "Product" },
  commitmentAmount: { id: "agreement-commitmentAmount", label: "Committed amount" },
  commitmentQty: { id: "agreement-commitmentQty", label: "Committed quantity" },
  unit: { id: "agreement-unit", label: "Unit" },
  unitPrice: { id: "agreement-unitPrice", label: "Unit price" },
  overdeliveryPct: { id: "agreement-overdeliveryPct", label: "Over-delivery tolerance (%)" },
  validFrom: { id: "agreement-validFrom", label: "Valid from" },
  validTo: { id: "agreement-validTo", label: "Valid to" },
  cumTolerance: { id: "agreement-cumTolerance", label: "Cumulative tolerance" },
};

/** Creates an ACTIVE agreement numbered SA- (duty sales.agreement.maintain). */
export function NewAgreementPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<AgreementValues>({
    legalEntityId: "",
    customerId: "",
    invoiceAccountId: "",
    productId: "",
    unit: "EA",
    unitPrice: "",
    currency: "USD",
    commitmentType: "QUANTITY",
    commitmentQty: "",
    commitmentAmount: "",
    validFrom: "",
    validTo: "",
    cumTolerance: "",
    overdeliveryPct: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([values]));

  function update(patch: Partial<AgreementValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateAgreement(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createAgreement(input, key.current);
      release();
      navigate(`/sales/agreements/${created.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const field = (name: Exclude<keyof AgreementValues, "commitmentType">, label: string, hint?: string, extra: { type?: string; mono?: boolean; optional?: boolean; decimal?: boolean } = {}) => (
    <FormField id={`agreement-${name}`} label={label} hint={hint} error={errors[name]} optional={extra.optional}>
      {(control) => (
        <Input
          {...control}
          type={extra.type ?? "text"}
          inputMode={extra.decimal ? "decimal" : undefined}
          className={extra.mono ? styles.mono : undefined}
          value={values[name] ?? ""}
          onChange={(e) => update({ [name]: name === "currency" ? e.target.value.toUpperCase() : e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New sales agreement" description="Commit a customer to a quantity or an amount of one product at an agreed price. Releases against it create orders at this price." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New sales agreement">
          <FormErrorSummary items={summaryItems(errors, NEW_AGREEMENT_FIELDS)} attempt={attempt} />
          {field("legalEntityId", "Legal entity", "UUID of the selling legal entity.", { mono: true })}
          {field("customerId", "Customer", "UUID of the customer party.", { mono: true })}
          {field("invoiceAccountId", "Invoice account", "Leave empty to invoice the customer.", { mono: true, optional: true })}
          {field("productId", "Product", "UUID of the product.", { mono: true })}
          <FormField id="agreement-commitmentType" label="Commitment by" hint="A quantity commits units; an amount commits money, and each release draws its line value (quantity times price).">
            {(control) => (
              <Select {...control} value={values.commitmentType ?? "QUANTITY"} onChange={(e) => update({ commitmentType: e.target.value as CommitmentType })}>
                {COMMITMENT_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {values.commitmentType === "AMOUNT"
            ? field("commitmentAmount", "Committed amount", "In the agreement currency, up to 4 decimals.", { decimal: true })
            : field("commitmentQty", "Committed quantity", undefined, { decimal: true })}
          {field("unit", "Unit")}
          {field("unitPrice", "Unit price", undefined, { decimal: true })}
          {field("currency", "Currency")}
          {field("overdeliveryPct", "Over-delivery tolerance (%)", "Releases may go this many percent above the committed quantity. Default 0.", { decimal: true, optional: true })}
          {field("validFrom", "Valid from", undefined, { type: "date", optional: true })}
          {field("validTo", "Valid to", undefined, { type: "date", optional: true })}
          <FormSection collapse="advanced">
            {field("cumTolerance", "Cumulative tolerance", "How far a scheduling release's cumulative received quantity may differ from shipped before it is held. Default 0.", { decimal: true, optional: true })}
          </FormSection>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the agreement" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save agreement
            </Button>
            <Link className={buttonClassName("ghost")} to="/sales/agreements">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      {prompt}
    </div>
  );
}
