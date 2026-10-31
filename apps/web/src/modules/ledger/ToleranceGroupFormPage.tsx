import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, Card, FormField, Input, PageHeader, Skeleton } from "../../shell/ui";
import { fetchToleranceGroup, putToleranceGroup, TOLERANCE_CODE, type ToleranceGroup, type ToleranceGroupInput } from "./api/client";
import { ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

const MAX_NAME = 60;
const ACCOUNT = /^[A-Za-z0-9._-]{1,20}$/;
const DECIMAL = /^\d+(\.\d{1,6})?$/;

export interface ToleranceDraft extends ToleranceGroupInput {
  code: string;
}

export const emptyTolerance: ToleranceDraft = { code: "", name: "", debitLimit: "", creditLimit: "", percentLimit: "0", differenceAccount: "", active: true };

type Errors = Record<string, string>;

/** Client checks that mirror PUT .../tolerance-groups/{code}; the service has the last word. */
export function validateTolerance(v: ToleranceDraft): Errors {
  const errors: Errors = {};
  if (!TOLERANCE_CODE.test(v.code)) errors.code = "Use 1 to 10 capital letters, digits or underscores.";
  const name = v.name.trim();
  if (name === "" || name.length > MAX_NAME) errors.name = `Enter a name of up to ${MAX_NAME} characters.`;
  if (!DECIMAL.test(v.debitLimit)) errors.debitLimit = "Enter an amount such as 0.05, with at most 6 decimals.";
  if (!DECIMAL.test(v.creditLimit)) errors.creditLimit = "Enter an amount such as 0.05, with at most 6 decimals.";
  if (!DECIMAL.test(v.percentLimit) || Number(v.percentLimit) > 100) errors.percentLimit = "Enter a percentage from 0 to 100, with at most 6 decimals.";
  if (!ACCOUNT.test(v.differenceAccount)) errors.differenceAccount = "Enter the account number, up to 20 letters, digits, dots, underscores or dashes.";
  return errors;
}

function ToleranceForm({ ledgerId, initial, existing }: { ledgerId: string; initial: ToleranceDraft; existing?: ToleranceGroup }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [values, setValues] = useState(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const update = (patch: Partial<ToleranceDraft>) => setValues((prev) => ({ ...prev, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateTolerance(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { code, ...input } = values;
      await putToleranceGroup(ledgerId, code, { ...input, name: input.name.trim() }, existing?.version);
      await queryClient.invalidateQueries({ queryKey: ["ledger", "tolerance-groups"] });
      navigate(`/ledger/tolerance-groups${ledgerQuery(ledgerId)}`);
    } catch (e) {
      setFailure(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label={existing ? `Edit tolerance group ${initial.code}` : "New tolerance group"}>
        <FormField id="tg-code" label="Code" hint={existing ? "The code can't be changed." : "Up to 10 capital letters, digits or underscores."} error={errors.code}>
          {(c) => <Input {...c} className={styles.mono} value={values.code} readOnly={Boolean(existing)} maxLength={10} onChange={(e) => update({ code: e.target.value.toUpperCase() })} invalid={Boolean(errors.code)} />}
        </FormField>
        <FormField id="tg-name" label="Name" error={errors.name}>
          {(c) => <Input {...c} value={values.name} maxLength={MAX_NAME} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
        </FormField>
        <FormField id="tg-debit" label="Debit limit" hint="The largest debit residual (debits exceed credits) that may be written off, in the ledger currency." error={errors.debitLimit}>
          {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={values.debitLimit} onChange={(e) => update({ debitLimit: e.target.value })} invalid={Boolean(errors.debitLimit)} />}
        </FormField>
        <FormField id="tg-credit" label="Credit limit" hint="The largest credit residual (credits exceed debits) that may be written off, in the ledger currency." error={errors.creditLimit}>
          {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={values.creditLimit} onChange={(e) => update({ creditLimit: e.target.value })} invalid={Boolean(errors.creditLimit)} />}
        </FormField>
        <FormField id="tg-pct" label="Percent limit" hint="Also at most this share of the selection's total debits plus credits. 0 means no percentage limit." error={errors.percentLimit}>
          {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={values.percentLimit} onChange={(e) => update({ percentLimit: e.target.value })} invalid={Boolean(errors.percentLimit)} />}
        </FormField>
        <FormField id="tg-account" label="Difference account" hint="A postable account that receives the write-off." error={errors.differenceAccount}>
          {(c) => <Input {...c} className={styles.mono} value={values.differenceAccount} onChange={(e) => update({ differenceAccount: e.target.value })} invalid={Boolean(errors.differenceAccount)} />}
        </FormField>
        <span className={styles.check}>
          <Input id="tg-active" type="checkbox" checked={values.active} onChange={(e) => update({ active: e.target.checked })} />
          <label htmlFor="tg-active">Active</label>
        </span>
        {failure ? (
          <div className={styles.wide}>
            <Alert tone="danger" title="Could not save the tolerance group" live="alert">{failure}</Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>Save tolerance group</Button>
          <Link to={`/ledger/tolerance-groups${ledgerQuery(ledgerId)}`}>Cancel</Link>
        </div>
      </form>
    </Card>
  );
}

function NeedsLedger() {
  return <p className={styles.note}>Open this page from the tolerance groups list of a ledger so it knows which ledger to use.</p>;
}

export function NewToleranceGroupPage() {
  const [ledgerId] = useLedgerParam();
  return (
    <div className={styles.stack}>
      <PageHeader title="New tolerance group" />
      {ledgerId ? <ToleranceForm ledgerId={ledgerId} initial={emptyTolerance} /> : <NeedsLedger />}
    </div>
  );
}

export function EditToleranceGroupPage() {
  const { code = "" } = useParams();
  const [ledgerId] = useLedgerParam();
  const group = useQuery({ queryKey: ["ledger", "tolerance-groups", ledgerId, code], queryFn: () => fetchToleranceGroup(ledgerId, code), enabled: ledgerId !== "" });
  return (
    <div className={styles.stack}>
      <PageHeader title={`Tolerance group ${code}`} />
      {ledgerId === "" ? (
        <NeedsLedger />
      ) : group.isPending ? (
        <Skeleton height="tile" />
      ) : group.isError ? (
        <Alert tone="danger" title="Could not load the tolerance group" live="alert" action={<Button size="sm" onClick={() => void group.refetch()}>Try again</Button>}>
          {errorText(group.error)}
        </Alert>
      ) : (
        <ToleranceForm
          key={group.data.version}
          ledgerId={ledgerId}
          existing={group.data}
          initial={{
            code: group.data.code,
            name: group.data.name,
            debitLimit: group.data.debitLimit,
            creditLimit: group.data.creditLimit,
            percentLimit: group.data.percentLimit,
            differenceAccount: group.data.differenceAccount,
            active: group.data.active,
          }}
        />
      )}
    </div>
  );
}
