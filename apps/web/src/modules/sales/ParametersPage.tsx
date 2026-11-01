import { useEffect, useMemo, useState, type FormEvent } from "react";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, Card, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import {
  DEFAULT_ORDER_TYPES,
  describeProblem,
  etagOf,
  getSalesParameters,
  putSalesParameters,
  type DefaultOrderType,
  type Problem,
  type SalesParametersInput,
} from "./api/client";
import { formatDateTime, sentence, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Errors = Record<string, string>;

/** Saving parameters needs this duty (enforced by the service; seeding is #313). */
export const DUTY_PARAMETER_MAINTAIN = "sales.parameter.maintain";

export interface ParameterValues {
  quotationExpiryDays: string;
  quotationProbabilityDefault: string;
  creditReleaseValidityDays: string;
  creditHoldSlaBusinessDays: string;
  defaultOrderType: DefaultOrderType;
  /** Optional: empty means the defaults (5,000 USD, 4,500 EUR). */
  oneTimeOrderLimit?: string;
  oneTimeOrderLimitCurrency?: string;
}

export function parameterValues(p: SalesParametersInput): ParameterValues {
  return {
    quotationExpiryDays: String(p.quotationExpiryDays),
    quotationProbabilityDefault: String(p.quotationProbabilityDefault),
    creditReleaseValidityDays: String(p.creditReleaseValidityDays),
    creditHoldSlaBusinessDays: String(p.creditHoldSlaBusinessDays),
    defaultOrderType: p.defaultOrderType,
    oneTimeOrderLimit: p.oneTimeOrderLimit ?? "",
    oneTimeOrderLimitCurrency: p.oneTimeOrderLimitCurrency ?? "",
  };
}

const WHOLE = /^\d{1,3}$/;

function inRange(raw: string, min: number, max: number): boolean {
  return WHOLE.test(raw) && Number(raw) >= min && Number(raw) <= max;
}

/** Checks that mirror the service (domain.Parameters.Validate). Keys are the service's field names. */
export function validateParameters(v: ParameterValues): { errors: Errors; input?: SalesParametersInput } {
  const errors: Errors = {};
  if (!inRange(v.quotationExpiryDays, 1, 365)) errors.quotationExpiryDays = "Enter a whole number of days from 1 to 365.";
  if (!inRange(v.quotationProbabilityDefault, 0, 100)) errors.quotationProbabilityDefault = "Enter a whole percentage from 0 to 100.";
  if (!inRange(v.creditReleaseValidityDays, 1, 30)) errors.creditReleaseValidityDays = "Enter a whole number of days from 1 to 30.";
  if (!inRange(v.creditHoldSlaBusinessDays, 1, 10)) errors.creditHoldSlaBusinessDays = "Enter a whole number of business days from 1 to 10.";
  const limit = (v.oneTimeOrderLimit ?? "").trim();
  const limitCurrency = (v.oneTimeOrderLimitCurrency ?? "").trim();
  if ((limit === "") !== (limitCurrency === "")) errors.oneTimeOrderLimit = "Enter the limit and its currency together, or leave both empty.";
  else if (limit !== "") {
    if (!/^\d+(\.\d{1,2})?$/.test(limit)) errors.oneTimeOrderLimit = "Enter an amount of 0 or more with at most 2 decimal places.";
    if (!/^[A-Z]{3}$/.test(limitCurrency)) errors.oneTimeOrderLimitCurrency = "Enter a three-letter ISO currency code, such as USD.";
  }
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      ...(limit !== "" ? { oneTimeOrderLimit: limit, oneTimeOrderLimitCurrency: limitCurrency } : {}),
      quotationExpiryDays: Number(v.quotationExpiryDays),
      quotationProbabilityDefault: Number(v.quotationProbabilityDefault),
      creditReleaseValidityDays: Number(v.creditReleaseValidityDays),
      creditHoldSlaBusinessDays: Number(v.creditHoldSlaBusinessDays),
      defaultOrderType: v.defaultOrderType,
    },
  };
}

interface FormProps {
  legalEntityId: string;
  initial: ParameterValues;
  etag: string;
  saved: boolean;
  updatedAt?: string;
  canSave: boolean;
}

function ParametersForm({ legalEntityId, initial, etag, saved, updatedAt, canSave }: FormProps) {
  const [values, setValues] = useState(initial);
  const [current, setCurrent] = useState({ etag, saved, updatedAt });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setValues(initial);
    setCurrent({ etag, saved, updatedAt });
  }, [initial, etag, saved, updatedAt]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateParameters(values);
    setErrors(found);
    setFailure(null);
    setDone("");
    if (!input) return;
    setBusy(true);
    try {
      const res = await putSalesParameters(legalEntityId, input, current.etag);
      setCurrent({ etag: res.etag || etagOf(res.value.version), saved: true, updatedAt: res.value.updatedAt });
      setDone("Sales parameters saved.");
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const whole = (name: Exclude<keyof ParameterValues, "defaultOrderType" | "oneTimeOrderLimit" | "oneTimeOrderLimitCurrency">, label: string, hint: string) => (
    <FormField id={`param-${name}`} label={label} hint={hint} error={errors[name]}>
      {(control) => (
        <Input {...control} inputMode="numeric" value={values[name]} readOnly={!canSave} onChange={(e) => setValues({ ...values, [name]: e.target.value })} invalid={Boolean(errors[name])} />
      )}
    </FormField>
  );

  return (
    <form className={styles.stack} onSubmit={submit} noValidate aria-label="Sales parameters">
      <Card title="Parameters">
        {!current.saved ? <p className={styles.note}>This legal entity has not saved parameters; the defaults below apply until it does.</p> : null}
        <div className={styles.form}>
          {whole("quotationExpiryDays", "Quotation expiry, days", "Validity of a quotation created without a valid-until date, 1 to 365.")}
          {whole("quotationProbabilityDefault", "Default win probability, %", "Applies to a quotation created without a probability, 0 to 100.")}
          {whole("creditReleaseValidityDays", "Credit release validity, days", "How long a released CREDIT hold keeps covering its order, 1 to 30 (BR-27.4).")}
          {whole("creditHoldSlaBusinessDays", "Credit hold SLA, business days", "The release queue marks a hold waiting past this many Monday-to-Friday days, 1 to 10.")}
          <FormField id="param-oneTimeOrderLimit" label="One-time order limit" hint="The most a one-time customer order may be worth. Empty: 5,000 USD and 4,500 EUR; other currencies then cannot place one-time orders." error={errors.oneTimeOrderLimit} optional>
            {(control) => (
              <Input {...control} inputMode="decimal" value={values.oneTimeOrderLimit ?? ""} readOnly={!canSave} onChange={(e) => setValues({ ...values, oneTimeOrderLimit: e.target.value })} invalid={Boolean(errors.oneTimeOrderLimit)} />
            )}
          </FormField>
          <FormField id="param-oneTimeOrderLimitCurrency" label="One-time limit currency" error={errors.oneTimeOrderLimitCurrency} optional>
            {(control) => (
              <Input {...control} value={values.oneTimeOrderLimitCurrency ?? ""} readOnly={!canSave} onChange={(e) => setValues({ ...values, oneTimeOrderLimitCurrency: e.target.value.toUpperCase() })} invalid={Boolean(errors.oneTimeOrderLimitCurrency)} />
            )}
          </FormField>
          <FormField id="param-defaultOrderType" label="Default order type" hint="Used by an order created without a type." error={errors.defaultOrderType}>
            {(control) => (
              <Select {...control} value={values.defaultOrderType} disabled={!canSave} onChange={(e) => setValues({ ...values, defaultOrderType: e.target.value as DefaultOrderType })}>
                {DEFAULT_ORDER_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
      </Card>
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failure.code === "SLS-0006" ? "Someone else changed these parameters" : "Could not save the parameters"} live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <div className={styles.actions}>
        {canSave ? (
          <Button type="submit" variant="primary" loading={busy}>
            Save parameters
          </Button>
        ) : (
          <span className={styles.muted}>Changing the parameters needs the {DUTY_PARAMETER_MAINTAIN} duty.</span>
        )}
        {current.updatedAt ? <span className={styles.muted}>Last changed {formatDateTime(current.updatedAt)}</span> : null}
      </div>
    </form>
  );
}

/** Sales parameters of one legal entity (PRD §5.27.13): open a legal entity, review its values and save them. */
export function ParametersPage() {
  const canSave = holdsDuty(usePrincipalDuties(), DUTY_PARAMETER_MAINTAIN);
  const [draft, setDraft] = useState("");
  const [legalEntityId, setLegalEntityId] = useState("");
  const invalid = draft.trim() !== "" && !UUID.test(draft.trim()) ? "Enter a legal entity UUID." : undefined;
  const state = useLoad(
    (signal) => (legalEntityId ? getSalesParameters(legalEntityId, signal) : Promise.resolve(null)),
    legalEntityId,
  );
  const initial = useMemo(() => (state.data ? parameterValues(state.data.value) : null), [state.data]);

  return (
    <div className={styles.stack}>
      <PageHeader title="Sales parameters" description="Per legal entity settings the quotation, order and credit release flows read." />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          if (!invalid && draft.trim()) setLegalEntityId(draft.trim());
        }}
      >
        <FormField id="param-le" label="Legal entity" error={invalid}>
          {(control) => <Input {...control} className={styles.mono} value={draft} onChange={(e) => setDraft(e.target.value)} invalid={Boolean(invalid)} />}
        </FormField>
        <Button type="submit">Open</Button>
      </form>
      {!legalEntityId ? (
        <Alert tone="info" title="Choose a legal entity">
          Enter the legal entity's UUID to review and change its sales parameters.
        </Alert>
      ) : state.error ? (
        <Alert tone="danger" title="Could not load the sales parameters" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : !state.data || !initial ? (
        <div aria-busy="true">
          <span className="visually-hidden">Loading the sales parameters…</span>
          <Skeleton height="tile" />
        </div>
      ) : (
        <ParametersForm
          legalEntityId={legalEntityId}
          initial={initial}
          etag={state.data.etag || etagOf(state.data.value.version)}
          saved={state.data.value.version > 0}
          updatedAt={state.data.value.updatedAt}
          canSave={canSave}
        />
      )}
    </div>
  );
}
