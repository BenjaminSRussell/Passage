import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import { ACCOUNT_TYPES, fetchAccount, LedgerApiError, putAccount, RECON_TYPES, type Account, type AccountInput, type AccountType, type ReconType } from "./api/client";
import { CHART_CODE, DUTY_ACCOUNT_WRITE, typeLabel } from "./accountCommon";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const ACCOUNT_NUMBER = /^[A-Za-z0-9._-]{1,20}$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const NOT_POSTABLE: readonly string[] = ["profit_and_loss_total", "balance_sheet_total", "reporting"];

export interface AccountDraft {
  chart: string;
  number: string;
  name: string;
  accountType: AccountType;
  isPostable: boolean;
  suspendedFrom: string;
  reconciliationAccountType: ReconType;
}

type Errors = Record<string, string>;

export const emptyAccount = (chart: string): AccountDraft => ({
  chart,
  number: "",
  name: "",
  accountType: "expense",
  isPostable: true,
  suspendedFrom: "",
  reconciliationAccountType: "none",
});

/** Client checks that mirror PUT /charts/{chart}/accounts/{number}; the service has the last word. */
export function validateAccount(v: AccountDraft): Errors {
  const errors: Errors = {};
  if (!CHART_CODE.test(v.chart)) errors.chart = "Use 1 to 10 capital letters or digits.";
  if (!ACCOUNT_NUMBER.test(v.number)) errors.number = "Use 1 to 20 letters, digits, dots, underscores or dashes.";
  const name = v.name.trim();
  if (name === "" || [...name].length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (NOT_POSTABLE.includes(v.accountType) && v.isPostable) errors.isPostable = "Totals and reporting accounts are not postable.";
  if (v.suspendedFrom !== "" && !DATE.test(v.suspendedFrom)) errors.suspendedFrom = "Use a date as YYYY-MM-DD.";
  return errors;
}

export function toAccountInput(v: AccountDraft): AccountInput {
  const input: AccountInput = {
    name: v.name.trim(),
    accountType: v.accountType,
    isPostable: v.isPostable,
    reconciliationAccountType: v.reconciliationAccountType,
  };
  if (v.suspendedFrom !== "") input.suspendedFrom = v.suspendedFrom;
  return input;
}

function AccountForm({ initial, existing, lockVersion }: { initial: AccountDraft; existing: boolean; lockVersion?: number }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_ACCOUNT_WRITE);
  const [values, setValues] = useState<AccountDraft>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<ReactNode>(null);
  const [busy, setBusy] = useState(false);
  const update = (patch: Partial<AccountDraft>) => setValues((prev) => ({ ...prev, ...patch }));
  const back = `/ledger/accounts?chart=${encodeURIComponent(values.chart)}`;

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateAccount(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await putAccount(values.chart, values.number, toAccountInput(values), lockVersion);
      await queryClient.invalidateQueries({ queryKey: ["ledger", "accounts"] });
      navigate(back);
    } catch (error) {
      // A create sends no If-Match, so an existing number answers 428 (slice 22): the account is there already.
      setFailure(
        error instanceof LedgerApiError && !existing && error.code === "LEDGER_PRECONDITION_REQUIRED"
          ? `Account ${values.number} already exists in chart ${values.chart}. Open it from the chart of accounts to change it.`
          : error instanceof LedgerApiError && (error.code === "LEDGER_VERSION_CONFLICT" || error.code === "LEDGER_PRECONDITION_REQUIRED")
            ? "Someone changed this account since you opened it. Go back and open it again."
            : error instanceof LedgerApiError
              ? <ProblemMessage problem={error} />
              : (error as Error).message,
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={submit} noValidate aria-label={existing ? `Edit account ${initial.number}` : "New account"}>
        <FormField id="acct-chart" label="Chart code" error={errors.chart}>
          {(c) => <Input {...c} className={styles.mono} value={values.chart} readOnly={existing} maxLength={10} onChange={(e) => update({ chart: e.target.value.toUpperCase() })} invalid={Boolean(errors.chart)} />}
        </FormField>
        <FormField id="acct-number" label="Account number" hint={existing ? "The number can't be changed." : "Up to 20 letters, digits, dots, underscores or dashes."} error={errors.number}>
          {(c) => <Input {...c} className={styles.mono} value={values.number} readOnly={existing} maxLength={20} onChange={(e) => update({ number: e.target.value })} invalid={Boolean(errors.number)} />}
        </FormField>
        <FormField id="acct-name" label="Name" error={errors.name}>
          {(c) => <Input {...c} value={values.name} maxLength={120} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
        </FormField>
        <FormField id="acct-type" label="Account type">
          {(c) => (
            <Select {...c} value={values.accountType} onChange={(e) => update({ accountType: e.target.value as AccountType })}>
              {ACCOUNT_TYPES.map((t) => (
                <option key={t} value={t}>
                  {typeLabel(t)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="acct-recon" label="Reconciliation account type" hint="Customer, vendor, bank, inventory and fixed asset accounts take postings from their subledger only.">
          {(c) => (
            <Select {...c} value={values.reconciliationAccountType} onChange={(e) => update({ reconciliationAccountType: e.target.value as ReconType })}>
              {RECON_TYPES.map((t) => (
                <option key={t} value={t}>
                  {typeLabel(t)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="acct-suspended" label="Suspended from" optional hint="From this date postings to the account are refused." error={errors.suspendedFrom}>
          {(c) => <Input {...c} placeholder="2026-12-31" value={values.suspendedFrom} onChange={(e) => update({ suspendedFrom: e.target.value })} invalid={Boolean(errors.suspendedFrom)} />}
        </FormField>
        <div className={styles.formActions}>
          <span className={styles.check}>
            <Input id="acct-postable" type="checkbox" checked={values.isPostable} onChange={(e) => update({ isPostable: e.target.checked })} />
            <label htmlFor="acct-postable">Postable</label>
          </span>
          <span className={styles.note}>{errors.isPostable ?? "Totals and reporting accounts can't take postings."}</span>
        </div>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not save the account" live="alert">
              {failure}
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          {canWrite ? (
            <Button type="submit" variant="primary" loading={busy}>
              Save account
            </Button>
          ) : (
            <span className={styles.note}>You can view this account. Changing it needs the maintain accounts duty.</span>
          )}
          <Link className={buttonClassName("ghost")} to={back}>
            Cancel
          </Link>
        </div>
      </form>
    </Card>
  );
}

/** Create an account in a chart. */
export function NewAccountPage() {
  const [params] = useSearchParams();
  return (
    <div className={styles.stack}>
      <PageHeader title="New account" description="Add a main account to the chart. Accounts are never deleted: suspend one to stop postings." />
      <AccountForm initial={emptyAccount(params.get("chart") ?? "")} existing={false} />
    </div>
  );
}

function toDraft(a: Account): AccountDraft {
  return {
    chart: a.chartCode,
    number: a.accountNumber,
    name: a.name,
    accountType: (ACCOUNT_TYPES as readonly string[]).includes(a.accountType) ? (a.accountType as AccountType) : "expense",
    isPostable: a.isPostable,
    suspendedFrom: a.suspendedFrom ?? "",
    reconciliationAccountType: (RECON_TYPES as readonly string[]).includes(a.reconciliationAccountType) ? (a.reconciliationAccountType as ReconType) : "none",
  };
}

/** Edit an account; the save sends the lockVersion it was read at, so a change made meanwhile is refused. */
export function EditAccountPage() {
  const { chart = "", number = "" } = useParams();
  const result = useQuery({ queryKey: ["ledger", "accounts", chart, number], queryFn: () => fetchAccount(chart, number) });

  return (
    <div className={styles.stack}>
      <PageHeader title={`Account ${number}`} description={`Account of chart ${chart}. Changes apply to future postings; posted lines keep the account they were posted to.`} />
      {result.isError ? (
        <Alert tone="danger" title="Could not load the account" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : result.isPending ? (
        <Card>
          <div aria-busy="true">
            <span className="visually-hidden">Loading the account…</span>
            <Skeleton height="control" />
            <Skeleton height="control" width="1/2" />
          </div>
        </Card>
      ) : (
        <AccountForm key={result.data.lockVersion} initial={toDraft(result.data)} existing lockVersion={result.data.lockVersion} />
      )}
    </div>
  );
}
