import { useEffect, useMemo, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import {
  describeProblem,
  etagOf,
  getCreditProfile,
  LIMIT_TYPES,
  putCreditProfile,
  type CreditProfile,
  type CreditProfileInput,
  type LimitType,
  type Problem,
} from "./api/client";
import { DECIMAL, formatDateTime, sentence, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

const DATE = /^\d{4}-\d{2}-\d{2}$/;
const COUNT = /^\d{1,5}$/;

export interface ProfileValues {
  legalEntityId: string;
  customerId: string;
  limit: string;
  currency: string;
  limitType: LimitType;
  tolerancePct: string;
  toleranceAmount: string;
  oldestItemDaysMax: string;
  maxDunningLevel: string;
  reviewDate: string;
  customerSince: string;
  mandatoryCheck: boolean;
  openAr: string;
  unapplied: string;
  deliveredNotInvoiced: string;
  openOrders: string;
  uncountedDelivered: string;
  overdueAr: string;
  oldestItemDays: string;
  dunningLevel: string;
  /** Read-only: open orders in other currencies the check cannot count, as "4000 EUR, 120 GBP". */
  uncountedOpenOrders?: string;
}

/** PRD §5.27.13 defaults: BALANCE limit type, 5% / 1,000.00 tolerance. */
export const blankProfile: ProfileValues = {
  legalEntityId: "",
  customerId: "",
  limit: "",
  currency: "USD",
  limitType: "BALANCE",
  tolerancePct: "5",
  toleranceAmount: "1000",
  oldestItemDaysMax: "30",
  maxDunningLevel: "3",
  reviewDate: "",
  customerSince: "",
  mandatoryCheck: false,
  openAr: "0",
  unapplied: "0",
  deliveredNotInvoiced: "0",
  openOrders: "0",
  uncountedDelivered: "",
  overdueAr: "0",
  oldestItemDays: "0",
  dunningLevel: "0",
};

export function profileValues(p: CreditProfile): ProfileValues {
  const e = p.exposure;
  return {
    legalEntityId: p.legalEntityId,
    customerId: p.customerId,
    limit: p.limit,
    currency: p.currency,
    limitType: p.limitType,
    tolerancePct: p.tolerancePct,
    toleranceAmount: p.toleranceAmount,
    oldestItemDaysMax: String(p.oldestItemDaysMax),
    maxDunningLevel: String(p.maxDunningLevel),
    reviewDate: p.reviewDate ?? "",
    customerSince: p.customerSince ?? "",
    mandatoryCheck: p.mandatoryCheck,
    openAr: e.openAr ?? "0",
    unapplied: e.unapplied ?? "0",
    deliveredNotInvoiced: e.deliveredNotInvoiced ?? "0",
    openOrders: e.openOrders ?? "0",
    overdueAr: e.overdueAr ?? "0",
    oldestItemDays: String(e.oldestItemDays ?? 0),
    dunningLevel: String(e.dunningLevel ?? 0),
    uncountedDelivered: (e.deliveredNotInvoicedOtherCurrencies ?? []).map((o) => `${o.amount} ${o.currency}`).join(", "),
    uncountedOpenOrders: (e.openOrdersOtherCurrencies ?? []).map((o) => `${o.amount} ${o.currency}`).join(", "),
  };
}

const exposureAmounts = ["openAr", "unapplied", "deliveredNotInvoiced", "openOrders", "overdueAr"] as const;

/** Checks that mirror the service (api/credit.go buildCreditProfile). Keys are the service's field paths. */
export function validateProfile(v: ProfileValues): { errors: Errors; input?: CreditProfileInput } {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Choose the legal entity.";
  if (!UUID.test(v.customerId.trim())) errors.customerId = "Choose the customer.";
  if (!DECIMAL.test(v.limit)) errors.limit = "Enter a limit of 0 or more.";
  if (!/^[A-Z]{3}$/.test(v.currency)) errors.currency = "Enter a three-letter ISO currency code, such as USD.";
  if (!DECIMAL.test(v.tolerancePct) || Number(v.tolerancePct) > 100) errors.tolerancePct = "Enter a percentage from 0 to 100.";
  if (!DECIMAL.test(v.toleranceAmount)) errors.toleranceAmount = "Enter an amount of 0 or more.";
  if (!COUNT.test(v.oldestItemDaysMax)) errors.oldestItemDaysMax = "Enter a whole number of days.";
  if (!/^\d$/.test(v.maxDunningLevel)) errors.maxDunningLevel = "Enter a level from 0 to 9.";
  if (v.reviewDate !== "" && !DATE.test(v.reviewDate)) errors.reviewDate = "Choose a date.";
  if (v.customerSince !== "" && !DATE.test(v.customerSince)) errors.customerSince = "Choose a date.";
  const badExposure = exposureAmounts.some((k) => !DECIMAL.test(v[k])) || !COUNT.test(v.oldestItemDays) || !COUNT.test(v.dunningLevel);
  if (badExposure) errors.exposure = "Exposure amounts and counts must be 0 or more.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      limit: v.limit,
      currency: v.currency,
      limitType: v.limitType,
      tolerancePct: v.tolerancePct,
      toleranceAmount: v.toleranceAmount,
      oldestItemDaysMax: Number(v.oldestItemDaysMax),
      maxDunningLevel: Number(v.maxDunningLevel),
      ...(v.reviewDate ? { reviewDate: v.reviewDate } : {}),
      ...(v.customerSince ? { customerSince: v.customerSince } : {}),
      mandatoryCheck: v.mandatoryCheck,
      exposure: {
        openAr: v.openAr,
        unapplied: v.unapplied,
        deliveredNotInvoiced: v.deliveredNotInvoiced,
        openOrders: v.openOrders,
        overdueAr: v.overdueAr,
        oldestItemDays: Number(v.oldestItemDays),
        dunningLevel: Number(v.dunningLevel),
      },
    },
  };
}

type TextField = Exclude<keyof ProfileValues, "limitType" | "mandatoryCheck">;

interface FormProps {
  initial: ProfileValues;
  /** The ETag to send: "0" creates the profile. */
  etag: string;
  existing: boolean;
  updatedAt?: string;
}

function ProfileForm({ initial, etag, existing, updatedAt }: FormProps) {
  const navigate = useNavigate();
  const [values, setValues] = useState<ProfileValues>(initial);
  const [current, setCurrent] = useState({ etag, updatedAt });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setValues(initial);
    setCurrent({ etag, updatedAt });
  }, [initial, etag, updatedAt]);

  const update = (patch: Partial<ProfileValues>) => setValues((v) => ({ ...v, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateProfile(values);
    setErrors(found);
    setFailure(null);
    setSaved("");
    if (!input) return;
    setBusy(true);
    try {
      const res = await putCreditProfile(values.legalEntityId.trim(), values.customerId.trim(), input, current.etag);
      if (!existing) {
        navigate(`/sales/credit-profiles/${res.value.legalEntityId}/${res.value.customerId}`);
        return;
      }
      setCurrent({ etag: res.etag || etagOf(res.value.version), updatedAt: res.value.updatedAt });
      setSaved("Credit profile saved.");
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const text = (name: TextField, label: string, opts: { hint?: string; type?: string; mono?: boolean; optional?: boolean; readOnly?: boolean; decimal?: boolean } = {}) => (
    <FormField id={`profile-${name}`} label={label} hint={opts.hint} error={errors[name]} optional={opts.optional}>
      {(control) => (
        <Input
          {...control}
          type={opts.type ?? "text"}
          inputMode={opts.decimal ? "decimal" : undefined}
          className={opts.mono ? styles.mono : undefined}
          value={values[name]}
          readOnly={opts.readOnly}
          onChange={(e) => update({ [name]: name === "currency" ? e.target.value.toUpperCase() : e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  const pick = (name: "legalEntityId" | "customerId", entity: "legalEntity" | "customer", label: string) => (
    <FormField id={`profile-${name}`} label={label} error={errors[name]}>
      {(control) => (
        <EntityLookup
          entity={entity}
          {...control}
          scope={{ legalEntityId: values.legalEntityId || undefined }}
          value={values[name]}
          readOnly={existing}
          onChange={(e) => update({ [name]: e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <form className={styles.stack} onSubmit={submit} noValidate aria-label="Credit profile">
      <Card title="Customer and limit">
        <div className={styles.form}>
          {pick("legalEntityId", "legalEntity", "Legal entity")}
          {pick("customerId", "customer", "Customer")}
          {text("limit", "Credit limit", { decimal: true })}
          {text("currency", "Currency")}
          <FormField id="profile-limitType" label="Limit type" hint="What counts as exposure." error={errors.limitType}>
            {(control) => (
              <Select {...control} value={values.limitType} onChange={(e) => update({ limitType: e.target.value as LimitType })}>
                {LIMIT_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("tolerancePct", "Tolerance %", { decimal: true })}
          {text("toleranceAmount", "Tolerance amount", { decimal: true })}
          {text("oldestItemDaysMax", "Oldest open item, max days", { decimal: true })}
          {text("maxDunningLevel", "Max dunning level", { decimal: true })}
          {text("reviewDate", "Review date", { type: "date", optional: true })}
          {text("customerSince", "Customer since", { type: "date", optional: true })}
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input id="profile-mandatoryCheck" type="checkbox" checked={values.mandatoryCheck} onChange={(e) => update({ mandatoryCheck: e.target.checked })} />
              <label htmlFor="profile-mandatoryCheck">Mandatory credit check</label>
            </span>
            <span className={styles.note}>Orders fail CR-4 when the review date has passed.</span>
          </div>
        </div>
      </Card>
      <Card title="Exposure">
        <p className={styles.note}>Until receivables publishes open items, enter the customer's current position here. The confirm-time credit check reads it.</p>
        <div className={styles.form}>
          {text("openAr", "Open AR", { decimal: true })}
          {text("unapplied", "Unapplied cash", { decimal: true })}
          {text("deliveredNotInvoiced", "Delivered, not invoiced", { decimal: true, readOnly: true, hint: "Computed from each line's delivered less invoiced quantity on this customer's orders, in the profile currency; counted by limit types Balance with packing slips and Balance all." })}
          {text("openOrders", "Confirmed open orders", { decimal: true, readOnly: true, hint: "Computed from this customer's confirmed orders and the undelivered part of partly shipped orders, in the profile currency; counted by limit type Balance all." })}
          {text("overdueAr", "Overdue AR", { decimal: true })}
          {text("oldestItemDays", "Oldest item, days overdue", { decimal: true })}
          {text("dunningLevel", "Dunning level", { decimal: true })}
        </div>
        {values.uncountedOpenOrders ? (
          <Alert tone="warning" title="Open orders not counted">
            Orders in other currencies are left out of confirmed open orders until exchange rates are available: {values.uncountedOpenOrders}. A Balance-all credit check warns
            while they are open.
          </Alert>
        ) : null}
        {values.uncountedDelivered ? (
          <Alert tone="warning" title="Delivered goods not counted">
            Delivered, not invoiced goods on orders in other currencies are left out until exchange rates are available: {values.uncountedDelivered}. A credit check that counts
            them warns while they are open.
          </Alert>
        ) : null}
        {errors.exposure ? (
          <p role="alert" className={styles.fieldError}>
            {errors.exposure}
          </p>
        ) : null}
      </Card>
      <Alert tone="success" live="status">
        {saved}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failure.code === "SLS-0006" ? "Someone else changed this profile" : "Could not save the credit profile"} live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <div className={styles.actions}>
        <Button type="submit" variant="primary" loading={busy}>
          {existing ? "Save credit profile" : "Create credit profile"}
        </Button>
        <Link className={buttonClassName("ghost")} to="/sales/credit-profiles">
          Back to credit profiles
        </Link>
        {current.updatedAt ? <span className={styles.muted}>Last changed {formatDateTime(current.updatedAt)}</span> : null}
      </div>
    </form>
  );
}

/** New credit profile for a customer in a legal entity (FR-27.7). */
export function NewCreditProfilePage() {
  return (
    <div className={styles.stack}>
      <PageHeader title="New credit profile" description="Set the customer's credit limit and the rules the credit check applies at confirmation." />
      <ProfileForm initial={blankProfile} etag={etagOf(0)} existing={false} />
    </div>
  );
}

/** One credit profile, editable at its current version (If-Match). */
export function CreditProfilePage() {
  const { legalEntityId = "", customerId = "" } = useParams();
  const state = useLoad((signal) => getCreditProfile(legalEntityId, customerId, signal), `${legalEntityId}/${customerId}`);
  const initial = useMemo(() => (state.data ? profileValues(state.data.value) : null), [state.data]);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Credit profile" />
        <Alert tone="danger" title="Could not load the credit profile" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data || !initial) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Credit profile" />
        <span className="visually-hidden">Loading the credit profile…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <PageHeader title="Credit profile" description={`Customer ${customerId} in legal entity ${legalEntityId}.`} />
      <ProfileForm initial={initial} etag={state.data.etag || etagOf(state.data.value.version)} existing updatedAt={state.data.value.updatedAt} />
    </div>
  );
}
