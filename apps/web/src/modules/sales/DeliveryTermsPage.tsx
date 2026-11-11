import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { PlainName } from "./OrderNumber";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  createDeliveryTerms,
  describeProblem,
  getDeliveryTerms,
  listDeliveryTerms,
  newKey,
  replaceDeliveryTerms,
  type DeliveryTerms,
  type DeliveryTermsFilter,
  type Problem,
  type Versioned,
} from "./api/client";
import { dash, formatDateTime, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";

/** Changing delivery terms needs this duty (enforced by the service; the route rows are seeded under #313). */
export const DUTY_DELIVERY_TERMS_MAINTAIN = "sales.parameter.maintain";

/** ISO weekdays, 1 Monday to 7 Sunday (BR-27.2). */
export const WEEKDAYS: [number, string][] = [
  [1, "Monday"],
  [2, "Tuesday"],
  [3, "Wednesday"],
  [4, "Thursday"],
  [5, "Friday"],
  [6, "Saturday"],
  [7, "Sunday"],
];

const WORKDAYS = [1, 2, 3, 4, 5];

/** "Mon, Tue, Wed" for a list of ISO weekdays. */
export function weekdayNames(days: number[]): string {
  return days.length === 7 ? "Every day" : WEEKDAYS.filter(([n]) => days.includes(n)).map(([, name]) => name.slice(0, 3)).join(", ");
}

type Errors = Record<string, string>;

export interface TermsValues {
  legalEntityId: string;
  customerId: string;
  transportDays: string;
  receivingDays: number[];
}

/** Checks that mirror the service (domain.DeliveryTerms.Validate and api/delivery_terms.go). */
export function validateTerms(values: TermsValues, withParties: boolean): { errors: Errors; input?: { legalEntityId: string; customerId: string; transportDays: number; receivingDays: number[] } } {
  const errors: Errors = {};
  if (withParties) {
    if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
    if (!UUID.test(values.customerId.trim())) errors.customerId = "Enter the customer's UUID.";
  }
  if (!/^\d{1,2}$/.test(values.transportDays) || Number(values.transportDays) > 60) errors.transportDays = "Enter a whole number of days from 0 to 60.";
  if (values.receivingDays.length === 0) errors.receivingDays = "Choose at least one weekday the customer receives goods.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      legalEntityId: values.legalEntityId.trim(),
      customerId: values.customerId.trim(),
      transportDays: Number(values.transportDays),
      receivingDays: [...values.receivingDays].sort((a, b) => a - b),
    },
  };
}

interface WeekdayFieldsProps {
  values: TermsValues;
  errors: Errors;
  readOnly?: boolean;
  onChange(patch: Partial<TermsValues>): void;
}

function WeekdayFields({ values, errors, readOnly, onChange }: WeekdayFieldsProps) {
  return (
    <>
      <FormField id="terms-transportDays" label="Transport days" hint="Calendar days between shipping and receipt." error={errors.transportDays}>
        {(control) => <Input {...control} inputMode="numeric" value={values.transportDays} readOnly={readOnly} onChange={(e) => onChange({ transportDays: e.target.value })} invalid={Boolean(errors.transportDays)} />}
      </FormField>
      <fieldset className={styles.stack} aria-describedby="terms-receivingDays-hint">
        <legend>Receiving weekdays</legend>
        <p id="terms-receivingDays-hint" className={styles.note}>
          The days the customer receives goods. A receipt date that falls on another day rolls forward to the next receiving day.
        </p>
        {WEEKDAYS.map(([n, name]) => (
          <span key={n} className={styles.check}>
            <Input
              id={`terms-day-${n}`}
              type="checkbox"
              checked={values.receivingDays.includes(n)}
              disabled={readOnly}
              onChange={(e) => onChange({ receivingDays: e.target.checked ? [...values.receivingDays, n] : values.receivingDays.filter((d) => d !== n) })}
            />
            <label htmlFor={`terms-day-${n}`}>{name}</label>
          </span>
        ))}
        {errors.receivingDays ? <span role="alert">{errors.receivingDays}</span> : null}
      </fieldset>
    </>
  );
}

const columns: DataTableColumn<DeliveryTerms>[] = [
  { key: "customerId", header: "Customer", render: (t) => <Link to={`/sales/delivery-terms/${t.id}`}><PlainName short entity="customer" id={t.customerId} /></Link> },
  { key: "legalEntityId", header: "Legal entity", render: (t) => <EntityName entity="legalEntity" id={t.legalEntityId} /> },
  { key: "transportDays", header: "Transport days", align: "end" },
  { key: "receivingDays", header: "Receives", render: (t) => weekdayNames(t.receivingDays) },
  { key: "updatedAt", header: "Changed", render: (t) => formatDateTime(t.updatedAt) },
];

/** Customers' transport days and receiving weekdays (FR-27.3, BR-27.2). */
export function DeliveryTermsListPage() {
  const canMaintain = holdsDuty(usePrincipalDuties(), DUTY_DELIVERY_TERMS_MAINTAIN);
  const [draft, setDraft] = useState<DeliveryTermsFilter>({ legalEntityId: "", customerId: "" });
  const [filter, setFilter] = useState<DeliveryTermsFilter>({});
  const invalid: Errors = {};
  if (draft.legalEntityId?.trim() && !UUID.test(draft.legalEntityId.trim())) invalid.legalEntityId = "Enter a legal entity UUID.";
  if (draft.customerId?.trim() && !UUID.test(draft.customerId.trim())) invalid.customerId = "Enter a customer UUID.";
  const state = useLoad((signal) => listDeliveryTerms(filter, signal), JSON.stringify(filter));

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Delivery terms"
        description="How long each customer's goods are in transit and on which weekdays it receives them. Confirmed receipt dates on order lines are the confirmed ship date plus the transport days, rolled to the next receiving day. A customer without terms receives on the ship date."
        actions={
          canMaintain ? (
            <Link className={buttonClassName("primary")} to="/sales/delivery-terms/new">
              New delivery terms
            </Link>
          ) : undefined
        }
      />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          if (Object.keys(invalid).length === 0) setFilter({ legalEntityId: draft.legalEntityId?.trim(), customerId: draft.customerId?.trim() });
        }}
      >
        <FormField id="terms-filter-le" label="Legal entity" error={invalid.legalEntityId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })} invalid={Boolean(invalid.legalEntityId)} />}
        </FormField>
        <FormField id="terms-filter-customer" label="Customer" error={invalid.customerId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.customerId} onChange={(e) => setDraft({ ...draft, customerId: e.target.value })} invalid={Boolean(invalid.customerId)} />}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load delivery terms" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Delivery terms"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(t) => t.id}
          loading={state.loading}
          empty={{ title: "No delivery terms", description: "Customers without terms receive on the confirmed ship date." }}
        />
      )}
    </div>
  );
}

/** Sets a customer's delivery terms (FR-27.3, duty sales.parameter.maintain). */
export function NewDeliveryTermsPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<TermsValues>({ legalEntityId: "", customerId: "", transportDays: "0", receivingDays: WORKDAYS });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<TermsValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateTerms(values, true);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const saved = await createDeliveryTerms(input, key.current);
      navigate(`/sales/delivery-terms/${saved.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const party = (name: "legalEntityId" | "customerId", label: string, hint: string) => (
    <FormField id={`terms-${name}`} label={label} hint={hint} error={errors[name]}>
      {(control) => <Input {...control} className={styles.mono} value={values[name]} onChange={(e) => update({ [name]: e.target.value })} invalid={Boolean(errors[name])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New delivery terms" description="A customer has one set of terms per legal entity; change existing terms from their page." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New delivery terms">
          {party("legalEntityId", "Legal entity", "UUID of the selling legal entity.")}
          {party("customerId", "Customer", "UUID of the customer.")}
          <WeekdayFields values={values} errors={errors} onChange={update} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "SLS-0056" ? "This customer already has delivery terms" : "Could not save the delivery terms"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save delivery terms
            </Button>
            <Link className={buttonClassName("ghost")} to="/sales/delivery-terms">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

function valuesOf(t: DeliveryTerms): TermsValues {
  return { legalEntityId: t.legalEntityId, customerId: t.customerId, transportDays: String(t.transportDays), receivingDays: t.receivingDays };
}

/** One customer's terms, with the change form. */
export function DeliveryTermsDetailPage() {
  const { id = "" } = useParams();
  const canMaintain = holdsDuty(usePrincipalDuties(), DUTY_DELIVERY_TERMS_MAINTAIN);
  const state = useLoad((signal) => getDeliveryTerms(id, signal), id);
  const [latest, setLatest] = useState<Versioned<DeliveryTerms> | null>(null);
  const [draft, setDraft] = useState<TermsValues | null>(null);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  const current = latest ?? state.data;
  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Delivery terms" />
        <Alert tone="danger" title="Could not load the delivery terms" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Delivery terms" />
        <span className="visually-hidden">Loading the delivery terms…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: terms, etag } = current;
  const values = draft ?? valuesOf(terms);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setSaved(false);
    const { errors: found, input } = validateTerms(values, false);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    try {
      const res = await replaceDeliveryTerms(terms.id, etag, { transportDays: input.transportDays, receivingDays: input.receivingDays });
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

  const facts: [string, ReactNode][] = [
    ["Customer", <EntityName entity="customer" id={terms.customerId} />],
    ["Legal entity", <EntityName entity="legalEntity" id={terms.legalEntityId} />],
    ["Transport days", String(terms.transportDays)],
    ["Receives", weekdayNames(terms.receivingDays) || dash],
    ["Changed", `${formatDateTime(terms.updatedAt)} by ${terms.updatedBy}`],
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Delivery terms"
        actions={
          <Link className={buttonClassName("ghost")} to="/sales/delivery-terms">
            Back to delivery terms
          </Link>
        }
      />
      <Card>
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <Card title="Change the terms">
        <p className={styles.note}>A change dates the orders confirmed from now on. Orders that are already confirmed keep the terms they were confirmed with, so their receipt dates do not move.</p>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Change the delivery terms">
          <WeekdayFields values={values} errors={errors} readOnly={!canMaintain} onChange={(patch) => setDraft({ ...values, ...patch })} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "SLS-0006" ? "Someone else changed these terms" : "Could not change the delivery terms"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            {canMaintain ? (
              <Button type="submit" variant="primary" loading={busy}>
                Save changes
              </Button>
            ) : (
              <span className={styles.muted}>Changing delivery terms needs the {DUTY_DELIVERY_TERMS_MAINTAIN} duty.</span>
            )}
          </div>
        </form>
        <Alert tone="success" live="status">
          {saved ? "The delivery terms are saved." : null}
        </Alert>
      </Card>
    </div>
  );
}
