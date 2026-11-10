import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import {
  BillingApiError,
  createContract,
  describeProblem,
  METHODS,
  newKey,
  RATABLE_BASES,
  RESIDUAL_RULES,
  SSP_SOURCES,
  type ContractObligationInput,
  type Problem,
  type RatableBasis,
  type RecognitionMethod,
  type ResidualRule,
  type RevenueContractInput,
  type SspSource,
} from "./api/client";
import { BASIS_HINT, BASIS_LABELS, CURRENCY, DAY, DECIMAL, METHOD_LABELS, RESIDUAL_SSP_HINT, SSP_SOURCE_LABELS, today, UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

type Errors = Record<string, string>;

export interface ContractObligationValues {
  productId: string;
  ssp: string;
  sspSource: SspSource;
  method: RecognitionMethod;
  termStart: string;
  termEnd: string;
  satisfiedOn: string;
}

export interface ContractValues {
  legalEntityId: string;
  orderId: string;
  customerId: string;
  currency: string;
  transactionPrice: string;
  residualRule: ResidualRule;
  ratableBasis: RatableBasis;
  contractDate: string;
  obligations: ContractObligationValues[];
}

const MAX_OBLIGATIONS = 50;

function emptyObligation(day: string): ContractObligationValues {
  return { productId: "", ssp: "", sspSource: "EXPLICIT", method: "POINT_IN_TIME", termStart: "", termEnd: "", satisfiedOn: day };
}

export function emptyContract(day = today()): ContractValues {
  return {
    legalEntityId: "",
    orderId: "",
    customerId: "",
    currency: "USD",
    transactionPrice: "",
    residualRule: "LARGEST",
    ratableBasis: "DAILY",
    contractDate: day,
    obligations: [emptyObligation(day)],
  };
}

/** Client checks that mirror POST /revrec/contracts; the service has the last word. */
export function validateContract(v: ContractValues): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Choose a legal entity.";
  if (!UUID.test(v.orderId.trim())) errors.orderId = "Enter the sales order id (a UUID).";
  if (!UUID.test(v.customerId.trim())) errors.customerId = "Choose a customer.";
  if (!CURRENCY.test(v.currency.trim())) errors.currency = "Enter a three-letter currency code such as USD.";
  if (!DECIMAL.test(v.transactionPrice.trim())) errors.transactionPrice = "Enter the contract price as a number such as 10800.00.";
  if (!DAY.test(v.contractDate)) errors.contractDate = "Enter the contract date as YYYY-MM-DD.";
  let residual = 0;
  v.obligations.forEach((o, i) => {
    const at = (f: string) => `obligations[${i}].${f}`;
    if (!UUID.test(o.productId.trim())) errors[at("productId")] = "Choose a product.";
    // A residual SSP is derived by the service, so the form does not ask for it.
    if (o.sspSource === "RESIDUAL") residual += 1;
    else if (!DECIMAL.test(o.ssp.trim()) || Number(o.ssp) <= 0) errors[at("ssp")] = "Enter a standalone selling price above zero.";
    if (o.method === "RATABLE" && (!DAY.test(o.termStart) || !DAY.test(o.termEnd) || o.termEnd < o.termStart)) {
      errors[at("term")] = "A ratable obligation needs a term start and an end on or after it.";
    }
    if (o.method === "POINT_IN_TIME" && !DAY.test(o.satisfiedOn)) errors[at("satisfiedOn")] = "Enter the date control transferred, such as the shipment date.";
  });
  if (residual > 1) errors.obligations = "At most one obligation may take its price by the residual method.";
  return errors;
}

export function toContractInput(v: ContractValues): RevenueContractInput {
  return {
    legalEntityId: v.legalEntityId.trim(),
    orderId: v.orderId.trim(),
    customerId: v.customerId.trim(),
    currency: v.currency.trim(),
    transactionPrice: v.transactionPrice.trim(),
    residualRule: v.residualRule,
    ratableBasis: v.ratableBasis,
    contractDate: v.contractDate,
    obligations: v.obligations.map(
      (o): ContractObligationInput => ({
        productId: o.productId.trim(),
        ...(o.sspSource === "RESIDUAL" ? {} : { ssp: o.ssp.trim() }),
        sspSource: o.sspSource,
        method: o.method,
        ...(o.method === "RATABLE" ? { termStart: o.termStart, termEnd: o.termEnd } : {}),
        ...(o.method === "POINT_IN_TIME" ? { satisfiedOn: o.satisfiedOn } : {}),
      }),
    ),
  };
}

type HeaderField = Exclude<keyof ContractValues, "obligations" | "residualRule" | "ratableBasis">;
type ObligationText = "ssp" | "termStart" | "termEnd" | "satisfiedOn";

/** Revenue contract (FR-29.7): allocate a contract price across its obligations by SSP and plan their recognition. */
export function NewRevenueContractPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<ContractValues>(() => emptyContract());
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<ContractValues>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  function setObligation(i: number, patch: Partial<ContractObligationValues>) {
    update({ obligations: values.obligations.map((o, j) => (j === i ? { ...o, ...patch } : o)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateContract(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createContract(toContractInput(values), key.current);
      navigate(`/billing/revenue-contracts/${created.id}`);
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

  function header(id: string, field: HeaderField, label: string, opts: { hint?: string; mono?: boolean; decimal?: boolean; type?: string } = {}) {
    return (
      <FormField id={id} label={label} hint={opts.hint} error={errors[field]}>
        {(c) => (
          <Input
            {...c}
            type={opts.type}
            className={opts.mono ? styles.mono : undefined}
            inputMode={opts.decimal ? "decimal" : undefined}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<ContractValues>)}
            invalid={Boolean(errors[field])}
          />
        )}
      </FormField>
    );
  }

  function text(i: number, field: ObligationText, label: string, opts: { hint?: string; mono?: boolean; decimal?: boolean; type?: string; error?: string } = {}) {
    const error = opts.error ?? errors[`obligations[${i}].${field}`];
    return (
      <FormField id={`rc-ob-${i}-${field}`} label={label} hint={opts.hint} error={error}>
        {(c) => (
          <Input
            {...c}
            type={opts.type}
            className={opts.mono ? styles.mono : undefined}
            inputMode={opts.decimal ? "decimal" : undefined}
            value={values.obligations[i][field]}
            onChange={(e) => setObligation(i, { [field]: e.target.value })}
            invalid={Boolean(error)}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New revenue contract"
        description="Record a contract with several performance obligations. Its price is allocated by standalone selling price and each obligation gets its recognition schedule."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <LookupFormField id="rc-legal-entity" label="Legal entity" entity="legalEntity" value={values.legalEntityId} error={errors.legalEntityId} onChange={(legalEntityId) => update({ legalEntityId })} />
          {header("rc-order", "orderId", "Sales order id", { mono: true })}
          <LookupFormField
            id="rc-customer"
            label="Customer"
            entity="customer"
            scope={{ legalEntityId: values.legalEntityId || undefined }}
            value={values.customerId}
            error={errors.customerId}
            onChange={(customerId) => update({ customerId })}
          />
          {header("rc-currency", "currency", "Currency", { mono: true })}
          {header("rc-price", "transactionPrice", "Contract price", { mono: true, decimal: true, hint: "The transaction price after any bundle discount." })}
          {header("rc-date", "contractDate", "Contract date", { type: "date", mono: true, hint: "The invoice date; deferred revenue starts here." })}
          <FormField id="rc-residual" label="Rounding residual to" hint="Which obligation absorbs the cents left after rounding.">
            {(c) => (
              <Select {...c} value={values.residualRule} onChange={(e) => update({ residualRule: e.target.value as ResidualRule })}>
                {RESIDUAL_RULES.map((r) => (
                  <option key={r} value={r}>
                    {r === "LARGEST" ? "Largest obligation" : r === "FIRST" ? "First obligation" : "Last obligation"}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="rc-basis" label="Ratable basis" hint={BASIS_HINT} error={errors.ratableBasis}>
            {(c) => (
              <Select {...c} value={values.ratableBasis} onChange={(e) => update({ ratableBasis: e.target.value as RatableBasis })}>
                {RATABLE_BASES.map((b) => (
                  <option key={b} value={b}>
                    {BASIS_LABELS[b]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {values.obligations.map((o, i) => (
            <fieldset key={i} className={styles.lineSet}>
              <legend className={styles.groupsLegend}>{`Obligation ${i + 1}`}</legend>
              <LookupFormField
                id={`rc-ob-${i}-productId`}
                label="Product"
                entity="product"
                value={o.productId}
                error={errors[`obligations[${i}].productId`]}
                onChange={(productId) => setObligation(i, { productId })}
              />
              <FormField id={`rc-ob-${i}-source`} label="SSP source" hint={o.sspSource === "RESIDUAL" ? RESIDUAL_SSP_HINT : undefined}>
                {(c) => (
                  <Select {...c} value={o.sspSource} onChange={(e) => setObligation(i, { sspSource: e.target.value as SspSource })}>
                    {SSP_SOURCES.map((s) => (
                      <option key={s} value={s}>
                        {SSP_SOURCE_LABELS[s]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              {o.sspSource === "RESIDUAL" ? null : text(i, "ssp", "Standalone selling price", { mono: true, decimal: true })}
              <FormField id={`rc-ob-${i}-method`} label="Recognition">
                {(c) => (
                  <Select {...c} value={o.method} onChange={(e) => setObligation(i, { method: e.target.value as RecognitionMethod })}>
                    {METHODS.map((m) => (
                      <option key={m} value={m}>
                        {METHOD_LABELS[m]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              {o.method === "POINT_IN_TIME" ? text(i, "satisfiedOn", "Transfer date", { type: "date", mono: true, hint: "When control passed, such as the shipment date." }) : null}
              {o.method === "RATABLE" ? (
                <>
                  {text(i, "termStart", "Term start", { type: "date", mono: true, error: errors[`obligations[${i}].term`] })}
                  {text(i, "termEnd", "Term end", { type: "date", mono: true })}
                </>
              ) : null}
              {values.obligations.length > 1 ? (
                <Button variant="ghost" size="sm" onClick={() => update({ obligations: values.obligations.filter((_, j) => j !== i) })}>
                  {`Remove obligation ${i + 1}`}
                </Button>
              ) : null}
            </fieldset>
          ))}
          <div className={styles.formActions}>
            {values.obligations.length < MAX_OBLIGATIONS ? (
              <Button variant="secondary" onClick={() => update({ obligations: [...values.obligations, emptyObligation(values.contractDate)] })}>
                Add obligation
              </Button>
            ) : null}
            <span className={styles.note}>Milestones are satisfied from the contract page; usage obligations recognize as usage posts.</span>
          </div>
          {errors.obligations ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Check the obligations" live="alert">
                {errors.obligations}
              </Alert>
            </div>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the contract" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create contract
            </Button>
            <Link className={buttonClassName("ghost")} to="/billing/revenue-contracts">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
