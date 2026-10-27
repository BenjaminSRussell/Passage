import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { createPromise, describeProblem, listPromises, newKey, type PaymentPromise, type Problem, type PromiseInput } from "./api/client";
import { DAY, DECIMAL, money, shortId, today, UUID } from "./format";
import { CommandFailure, keepKeyAfter, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const PAGE = 500;
const GRACE_DAYS = 2;

/** A promise holds the item out of dunning until the promise date plus two days (BR-30.2). */
export function promiseHolds(promiseDate: string, asOf: string): boolean {
  const end = new Date(`${promiseDate}T00:00:00Z`);
  end.setUTCDate(end.getUTCDate() + GRACE_DAYS);
  return end.toISOString().slice(0, 10) >= asOf;
}

/** Promises to pay (FR-30.9): what the customer committed to pay, and by when. */
export function PromisesPage() {
  const [customer, setCustomer] = useState("");
  const valid = customer.trim() === "" || UUID.test(customer.trim());
  const key = valid ? customer.trim() : "";
  const state = useLoad((signal) => listPromises({ customer: key, limit: PAGE }, signal), key);
  const day = today();

  const columns: DataTableColumn<PaymentPromise>[] = [
    { key: "documentNumber", header: "Document", mono: true },
    { key: "customerId", header: "Customer", mono: true, render: (row) => <span title={row.customerId}>{shortId(row.customerId)}</span> },
    { key: "amount", header: "Promised", mono: true, align: "end", render: (row) => money(row.amount, row.currency) },
    { key: "promiseDate", header: "Promise date", mono: true },
    {
      key: "holds",
      header: "State",
      render: (row) => (promiseHolds(row.promiseDate, day) ? <Badge tone="info">Holds</Badge> : <Badge tone="warning">Past due</Badge>),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Promises to pay"
        description="Latest promise date first. A promise keeps its item out of dunning and interest until two days after the promise date."
        actions={
          <Link className={buttonClassName("primary")} to="/receivables/promises/new">
            Record promise
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="pr-customer" label="Customer id" optional error={valid ? undefined : "Enter a customer id (a UUID)."}>
          {(c) => <Input {...c} className={styles.mono} value={customer} onChange={(e) => setCustomer(e.target.value)} invalid={!valid} />}
        </FormField>
      </div>
      {state.error ? (
        <LoadFailure title="Could not load the promises" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Promises to pay"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No promises", description: "Record a promise when a customer commits to pay an item by a date." }}
        />
      )}
      {state.data && state.data.length >= PAGE ? <p className={styles.note}>Showing the latest {PAGE} promises.</p> : null}
    </div>
  );
}

type Errors = Record<string, string>;

export interface PromiseDraft {
  legalEntityId: string;
  openItemId: string;
  amount: string;
  promiseDate: string;
}

export function validatePromise(v: PromiseDraft, day: string = today()): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!UUID.test(v.openItemId.trim())) errors.openItemId = "Enter the open item id (a UUID).";
  if (!DECIMAL.test(v.amount.trim()) || /^0+(\.0+)?$/.test(v.amount.trim())) errors.amount = "Enter an amount above zero, such as 800.00.";
  if (!DAY.test(v.promiseDate)) errors.promiseDate = "Enter the promise date.";
  else if (v.promiseDate < day) errors.promiseDate = "The promise date cannot be in the past.";
  return errors;
}

/** Record a promise to pay on one open item. */
export function NewPromisePage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [values, setValues] = useState<PromiseDraft>({
    legalEntityId: params.get("legalEntity") ?? "",
    openItemId: params.get("openItem") ?? "",
    amount: params.get("amount") ?? "",
    promiseDate: today(),
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [created, setCreated] = useState<PaymentPromise | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<PromiseDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validatePromise(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    const input: PromiseInput = {
      legalEntityId: values.legalEntityId.trim(),
      openItemId: values.openItemId.trim(),
      amount: values.amount.trim(),
      promiseDate: values.promiseDate,
    };
    try {
      setCreated(await createPromise(input, key.current));
      key.current = newKey();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (!keepKeyAfter(error)) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Record promise" description="The item stays out of dunning and interest until two days after the promise date." />
      {created ? (
        <Alert
          tone="success"
          title="Promise recorded"
          live="status"
          action={
            <Button size="sm" onClick={() => navigate("/receivables/promises")}>
              All promises
            </Button>
          }
        >
          {money(created.amount, created.currency)} on {created.documentNumber} by {created.promiseDate}.
        </Alert>
      ) : null}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="np-entity" label="Legal entity id" error={errors.legalEntityId}>
            {(c) => <Input {...c} className={styles.mono} value={values.legalEntityId} onChange={(e) => update({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="np-item" label="Open item id" error={errors.openItemId}>
            {(c) => <Input {...c} className={styles.mono} value={values.openItemId} onChange={(e) => update({ openItemId: e.target.value })} invalid={Boolean(errors.openItemId)} />}
          </FormField>
          <FormField id="np-amount" label="Promised amount" hint="At most the item's open amount." error={errors.amount}>
            {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={values.amount} onChange={(e) => update({ amount: e.target.value })} invalid={Boolean(errors.amount)} />}
          </FormField>
          <FormField id="np-date" label="Promise date" error={errors.promiseDate}>
            {(c) => <Input {...c} type="date" value={values.promiseDate} onChange={(e) => update({ promiseDate: e.target.value })} invalid={Boolean(errors.promiseDate)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <CommandFailure title="Could not record the promise" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Record promise
            </Button>
            <Link className={buttonClassName("ghost")} to="/receivables/promises">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
