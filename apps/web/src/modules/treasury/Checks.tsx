import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Skeleton,
  StatusPill,
  type DataTableColumn,
} from "../../shell/ui";
import {
  describeProblem,
  getCheck,
  issueCheck,
  listChecks,
  newKey,
  runCheckAction,
  TreasuryApiError,
  type Check,
  type CheckAction,
  type CheckInput,
  type CheckStatus,
  type Problem,
} from "./api/client";
import { CURRENCY, dateTime, money, UUID, validAmount } from "./format";
import { CommandFailure, Detail, keepKeyAfter, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

type Errors = Record<string, string>;

const checkColumns: DataTableColumn<Check>[] = [
  { key: "checkNumber", header: "Number", mono: true, render: (row) => String(row.checkNumber) },
  { key: "payee", header: "Payee" },
  { key: "amount", header: "Amount", mono: true, align: "end", render: (row) => money(row.amount, row.currency) },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "updatedAt", header: "Last changed", mono: true, render: (row) => dateTime(row.updatedAt) },
];

/** The check register of one bank account (FR-36.6). The account lives in the URL so the view can be shared. */
export function ChecksPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const bankAccountId = params.get("bankAccountId") ?? "";
  const [draft, setDraft] = useState(bankAccountId);
  const [error, setError] = useState<string | undefined>();
  const chosen = UUID.test(bankAccountId);
  const state = useLoad((signal) => (chosen ? listChecks(bankAccountId, signal) : Promise.resolve([])), bankAccountId);

  function choose(event: FormEvent) {
    event.preventDefault();
    const value = draft.trim();
    if (!UUID.test(value)) {
      setError("Choose a bank account.");
      return;
    }
    setError(undefined);
    setParams({ bankAccountId: value });
  }

  const newLink = chosen ? `/treasury/checks/new?bankAccountId=${encodeURIComponent(bankAccountId)}` : "/treasury/checks/new";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Checks"
        description="Checks drawn on a bank account, newest number first. Numbers are gap-free: a void keeps its number and a reprint takes the next one."
        actions={
          <Link className={buttonClassName("primary")} to={newLink}>
            Issue check
          </Link>
        }
      />
      <Card>
        <form className={styles.toolbar} onSubmit={choose} noValidate>
          <FormField id="ck-account" label="Bank account" error={error}>
            {(c) => <EntityLookup {...c} entity="bankAccount" value={draft} onChange={(e) => setDraft(e.target.value)} invalid={Boolean(error)} />}
          </FormField>
          <Button type="submit">Show checks</Button>
        </form>
      </Card>
      {!chosen ? (
        <Card>
          <EmptyState icon="database" title="Choose a bank account" description="Checks are numbered per bank account. Enter its id to see the register." />
        </Card>
      ) : state.error ? (
        <LoadFailure title="Could not load the checks" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Checks"
          columns={checkColumns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No checks yet", description: "Issue the account's first check; it takes the account's next number." }}
          onRowOpen={(row) => navigate(`/treasury/checks/${row.id}`)}
        />
      )}
    </div>
  );
}

export interface CheckDraft {
  bankAccountId: string;
  payee: string;
  amount: string;
  currency: string;
}

/** The checks the service applies (COM-0001), so most mistakes are caught before the round trip. */
export function validateCheck(v: CheckDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.bankAccountId.trim())) errors.bankAccountId = "Choose the bank account.";
  const payee = v.payee.trim();
  if (payee.length < 1 || payee.length > 140) errors.payee = "Enter the payee, up to 140 characters.";
  const currency = v.currency.trim();
  if (!CURRENCY.test(currency)) errors.currency = "Enter the account's ISO 4217 currency, such as USD.";
  if (!validAmount(v.amount, currency)) errors.amount = "Enter a positive amount with no more decimals than the currency allows.";
  return errors;
}

export function toCheckInput(v: CheckDraft): CheckInput {
  return { bankAccountId: v.bankAccountId.trim(), payee: v.payee.trim(), amount: v.amount.trim(), currency: v.currency.trim() };
}

/** Issue a check. A retry after a network failure reuses the Idempotency-Key, so it cannot take two numbers. */
export function NewCheckPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [values, setValues] = useState<CheckDraft>({ bankAccountId: params.get("bankAccountId") ?? "", payee: "", amount: "", currency: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateCheck(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await issueCheck(toCheckInput(values), key.current);
      navigate(`/treasury/checks/${created.id}`);
    } catch (error) {
      if (!keepKeyAfter(error)) key.current = newKey();
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else if (problem.code === "TRS-0426") setErrors({ currency: problem.message });
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const field = (id: keyof CheckDraft, label: string, hint?: string, mono = true) => (
    <FormField id={`ck-${id}`} label={label} hint={hint} error={errors[id]}>
      {(c) => (
        <Input
          {...c}
          className={mono ? styles.mono : undefined}
          inputMode={id === "amount" ? "decimal" : undefined}
          value={values[id]}
          onChange={(e) => setValues((prev) => ({ ...prev, [id]: e.target.value }))}
          invalid={Boolean(errors[id])}
        />
      )}
    </FormField>
  );

  const back = UUID.test(values.bankAccountId.trim()) ? `/treasury/checks?bankAccountId=${encodeURIComponent(values.bankAccountId.trim())}` : "/treasury/checks";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Issue check"
        description="The check takes the account's next number and starts as Created. The account must be active and in the check's currency."
        actions={
          <Link className={buttonClassName("secondary")} to={back}>
            Checks
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <LookupFormField
            id="ck-bankAccountId"
            label="Bank account"
            entity="bankAccount"
            value={values.bankAccountId}
            error={errors.bankAccountId}
            onChange={(bankAccountId, picked) =>
              setValues((prev) => ({ ...prev, bankAccountId, currency: picked?.currency && prev.currency.trim() === "" ? picked.currency : prev.currency }))
            }
          />
          {field("payee", "Payee", undefined, false)}
          {field("amount", "Amount", "For example 1250.00.")}
          {field("currency", "Currency", "The bank account's currency.")}
          {failure ? (
            <div className={styles.formActions}>
              <CommandFailure title={failure.code === "TRS-0412" ? "The bank account is not active" : "Could not issue the check"} problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Issue check
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}

interface ActionSpec {
  action: CheckAction;
  label: string;
  danger?: boolean;
  /** Void and reprint record a reason. */
  reason?: boolean;
}

/** What a user may do next from each status (FR-36.6: Created -> Printed -> Sent -> Cleared / Voided / Stopped). */
export const CHECK_ACTIONS: Record<CheckStatus, ActionSpec[]> = {
  created: [
    { action: "print", label: "Mark printed" },
    { action: "void", label: "Void", danger: true, reason: true },
  ],
  printed: [
    { action: "send", label: "Mark sent" },
    { action: "reprint", label: "Reprint", reason: true },
    { action: "void", label: "Void", danger: true, reason: true },
  ],
  sent: [
    { action: "clear", label: "Mark cleared" },
    { action: "stop", label: "Stop payment" },
    { action: "reprint", label: "Reprint", reason: true },
    { action: "void", label: "Void", danger: true, reason: true },
  ],
  stopped: [
    { action: "reprint", label: "Reprint", reason: true },
    { action: "void", label: "Void", danger: true, reason: true },
  ],
  cleared: [],
  voided: [],
};

/** One check: details, lifecycle actions under If-Match, and void or reprint with a reason. */
export function CheckDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const check = useLoad((signal) => getCheck(id, signal), `check-${id}`);
  const [busy, setBusy] = useState<CheckAction | null>(null);
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState<string | undefined>();
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [done, setDone] = useState("");

  async function run(spec: ActionSpec, current: Check) {
    setFailure(null);
    setDone("");
    const text = reason.trim();
    if (spec.reason && (text.length < 1 || text.length > 200)) {
      setReasonError("Enter why the check is voided, up to 200 characters.");
      return;
    }
    setReasonError(undefined);
    setBusy(spec.action);
    try {
      const next = await runCheckAction(current.id, spec.action, current.version, spec.reason ? text : undefined);
      setReason("");
      if (spec.action === "reprint") {
        setDone(`Check ${current.checkNumber} is voided; check ${next.checkNumber} is printed with the same payee and amount.`);
        navigate(`/treasury/checks/${next.id}`);
        return;
      }
      setDone(`${spec.label}: check ${next.checkNumber} is now ${next.status}.`);
      check.reload();
    } catch (error) {
      const stale = error instanceof TreasuryApiError && error.status === 412;
      setFailure({
        title: stale ? "This check changed since you opened it" : `Could not ${spec.label.toLowerCase()}`,
        problem: describeProblem(error),
      });
      if (stale) check.reload();
    } finally {
      setBusy(null);
    }
  }

  if (check.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Check" />
        <LoadFailure title="Could not load the check" problem={check.error} retry={check.reload} />
      </div>
    );
  }
  const c = check.data;
  if (!c) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Check" />
        <Card>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }
  const actions = CHECK_ACTIONS[c.status] ?? [];
  const needsReason = actions.some((a) => a.reason);
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Check ${c.checkNumber}`}
        meta={<StatusPill status={c.status} />}
        actions={
          <Link className={buttonClassName("ghost")} to={`/treasury/checks?bankAccountId=${c.bankAccountId}`}>
            All checks
          </Link>
        }
      />
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? <CommandFailure title={failure.title} problem={failure.problem} /> : null}
      <Card title="Details" headingLevel={3}>
        <dl className={styles.details}>
          <Detail term="Payee">{c.payee}</Detail>
          <Detail term="Amount" mono>
            {money(c.amount, c.currency)}
          </Detail>
          <Detail term="Bank account" mono>
            <Link to={`/treasury/bank-accounts/${c.bankAccountId}`}>{c.bankAccountId}</Link>
          </Detail>
          {c.voidReason ? <Detail term="Void reason">{c.voidReason}</Detail> : null}
          {c.replacesId ? (
            <Detail term="Replaces" mono>
              <Link to={`/treasury/checks/${c.replacesId}`}>{c.replacesId}</Link>
            </Detail>
          ) : null}
          {c.replacedById ? (
            <Detail term="Replaced by" mono>
              <Link to={`/treasury/checks/${c.replacedById}`}>{c.replacedById}</Link>
            </Detail>
          ) : null}
          <Detail term="Version" mono>
            {String(c.version)}
          </Detail>
          <Detail term="Last changed" mono>
            {dateTime(c.updatedAt)}
          </Detail>
        </dl>
      </Card>
      <Card title="Actions" headingLevel={3}>
        {actions.length === 0 ? (
          <p className={styles.note}>
            {c.status === "cleared" ? "The bank paid this check; it can no longer be voided. Reverse the payment in accounts payable instead." : "A voided check is kept for the record; its number stays used."}
          </p>
        ) : (
          <div className={styles.stack}>
            {needsReason ? (
              <FormField id="ck-reason" label="Reason" hint="Needed to void or reprint, for example printer jam." error={reasonError}>
                {(f) => <Input {...f} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(reasonError)} />}
              </FormField>
            ) : null}
            {c.status === "sent" || c.status === "stopped" ? (
              <p className={styles.note}>The bank already knows this check, so a void also sends a void record to positive pay.</p>
            ) : null}
            <div className={styles.actions}>
              {actions.map((a) => (
                <Button key={a.action} variant={a.danger ? "danger" : "secondary"} loading={busy === a.action} onClick={() => void run(a, c)}>
                  {a.label}
                </Button>
              ))}
            </div>
          </div>
        )}
      </Card>
    </div>
  );
}
