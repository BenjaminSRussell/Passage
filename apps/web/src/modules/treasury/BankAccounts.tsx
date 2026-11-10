import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
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
  createBankAccount,
  describeProblem,
  getBankAccount,
  listBankAccounts,
  listStatements,
  newKey,
  transitionBankAccount,
  TreasuryApiError,
  type AccountAction,
  type BankAccount,
  type BankAccountInput,
  type Problem,
  type Statement,
} from "./api/client";
import { ACCOUNT_CODE, BIC, CURRENCY, dateTime, money, normalizeIban, STATEMENT_ACCOUNT, UUID, validIban } from "./format";
import { CommandFailure, Detail, keepKeyAfter, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Errors = Record<string, string>;

const accountColumns: DataTableColumn<BankAccount>[] = [
  { key: "accountCode", header: "Code", mono: true },
  { key: "ibanMasked", header: "IBAN", mono: true },
  { key: "bic", header: "BIC", mono: true, render: (row) => row.bic ?? "None" },
  { key: "currency", header: "Currency", mono: true },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

/** Bank accounts of one legal entity (FR-36.1). The legal entity lives in the URL so the view can be shared. */
export function BankAccountsPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const legalEntityId = params.get("legalEntityId") ?? "";
  const [draft, setDraft] = useState(legalEntityId);
  const [error, setError] = useState<string | undefined>();
  const chosen = UUID.test(legalEntityId);
  const state = useLoad((signal) => (chosen ? listBankAccounts(legalEntityId, signal) : Promise.resolve([])), legalEntityId);

  function choose(event: FormEvent) {
    event.preventDefault();
    const value = draft.trim();
    if (!UUID.test(value)) {
      setError("Choose a legal entity.");
      return;
    }
    setError(undefined);
    setParams({ legalEntityId: value });
  }

  const newLink = chosen ? `/treasury/bank-accounts/new?legalEntityId=${encodeURIComponent(legalEntityId)}` : "/treasury/bank-accounts/new";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Bank accounts"
        description="House bank accounts of a legal entity. New accounts start as Draft; an approver activates them before statements can be imported."
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("primary")} to={newLink}>
              New bank account
            </Link>
            <Link className={buttonClassName("secondary")} to="/treasury/statements/import">
              Import statement
            </Link>
          </div>
        }
      />
      <Card>
        <form className={styles.toolbar} onSubmit={choose} noValidate>
          <FormField id="ba-entity" label="Legal entity" error={error}>
            {(c) => <EntityLookup entity="legalEntity" {...c} value={draft} onChange={(e) => setDraft(e.target.value)} invalid={Boolean(error)} />}
          </FormField>
          <Button type="submit">Show accounts</Button>
        </form>
      </Card>
      {!chosen ? (
        <Card>
          <EmptyState icon="database" title="Choose a legal entity" description="Bank accounts belong to one legal entity. Enter its id to list them." />
        </Card>
      ) : state.error ? (
        <LoadFailure title="Could not load the bank accounts" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Bank accounts"
          columns={accountColumns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No bank accounts yet", description: "Create the legal entity's first house bank account." }}
          onRowOpen={(row) => navigate(`/treasury/bank-accounts/${row.id}`)}
        />
      )}
    </div>
  );
}

export interface AccountDraft {
  legalEntityId: string;
  accountCode: string;
  iban: string;
  statementAccount: string;
  bic: string;
  currency: string;
  mainAccountId: string;
}

/** The same checks the service applies (COM-0001, TRS-0401), so most mistakes are caught before the round trip. */
export function validateAccount(v: AccountDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Choose a legal entity.";
  if (!ACCOUNT_CODE.test(v.accountCode.trim())) errors.accountCode = "Use 1 to 20 characters: A to Z, 0 to 9 or a hyphen.";
  if (!validIban(v.iban)) errors.iban = "Enter a valid IBAN; the check digits do not match.";
  const statementAccount = normalizeIban(v.statementAccount);
  if (statementAccount !== "" && !STATEMENT_ACCOUNT.test(statementAccount))
    errors.statementAccount = "Use up to 35 characters: A to Z, 0 to 9, slash, dot or hyphen.";
  if (v.bic.trim() !== "" && !BIC.test(v.bic.trim().toUpperCase())) errors.bic = "Enter an 8 or 11 character BIC.";
  if (!CURRENCY.test(v.currency.trim())) errors.currency = "Enter an ISO 4217 code such as EUR.";
  if (!UUID.test(v.mainAccountId.trim())) errors.mainAccountId = "Enter the GL main account id (a UUID).";
  return errors;
}

export function toAccountInput(v: AccountDraft): BankAccountInput {
  const input: BankAccountInput = {
    legalEntityId: v.legalEntityId.trim(),
    accountCode: v.accountCode.trim(),
    iban: normalizeIban(v.iban),
    currency: v.currency.trim(),
    mainAccountId: v.mainAccountId.trim(),
  };
  if (v.bic.trim() !== "") input.bic = v.bic.trim().toUpperCase();
  const statementAccount = normalizeIban(v.statementAccount);
  if (statementAccount !== "") input.statementAccount = statementAccount;
  return input;
}

/** Create a Draft bank account. A retry after a network failure reuses the Idempotency-Key, so it cannot create two. */
export function NewBankAccountPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [values, setValues] = useState<AccountDraft>({
    legalEntityId: params.get("legalEntityId") ?? "",
    accountCode: "",
    iban: "",
    statementAccount: "",
    bic: "",
    currency: "",
    mainAccountId: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<AccountDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateAccount(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createBankAccount(toAccountInput(values), key.current);
      navigate(`/treasury/bank-accounts/${created.id}`);
    } catch (error) {
      if (!keepKeyAfter(error)) key.current = newKey();
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else if (problem.code === "TRS-0401") setErrors({ iban: problem.message });
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const field = (id: keyof AccountDraft, label: string, hint?: string, optional?: boolean) => (
    <FormField id={`ba-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(c) => <Input {...c} className={styles.mono} value={values[id]} onChange={(e) => update({ [id]: e.target.value })} invalid={Boolean(errors[id])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New bank account"
        description="The account is created as Draft. Only a masked IBAN, and the last four characters of the statement account number, are shown after saving."
        actions={
          <Link className={buttonClassName("secondary")} to="/treasury">
            Bank accounts
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {field("legalEntityId", "Legal entity id")}
          {field("accountCode", "Account code", "Unique per legal entity, for example OPS-01.")}
          {field("iban", "IBAN", "Spaces are allowed; the check digits are verified.")}
          {field(
            "statementAccount",
            "Statement account number",
            "The account number on the bank's BAI2 or MT940 files, if it is not the IBAN (for example a US account number).",
            true,
          )}
          {field("bic", "BIC", undefined, true)}
          {field("currency", "Currency", "ISO 4217, for example EUR.")}
          {field("mainAccountId", "GL main account id", "The ledger bank account this account posts to.")}
          {failure ? (
            <div className={styles.formActions}>
              <CommandFailure title={failure.code === "TRS-0404" ? "This account already exists" : "Could not create the bank account"} problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create bank account
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}

const statementColumns: DataTableColumn<Statement>[] = [
  { key: "statementId", header: "Statement", mono: true },
  { key: "format", header: "Format", mono: true },
  { key: "sequence", header: "Sequence", mono: true, align: "end", render: (row) => (row.sequence == null ? "None" : String(row.sequence)) },
  { key: "closingDate", header: "Closing date", mono: true },
  { key: "openingBalance", header: "Opening", mono: true, align: "end", render: (row) => money(row.openingBalance, row.currency) },
  { key: "closingBalance", header: "Closing", mono: true, align: "end", render: (row) => money(row.closingBalance, row.currency) },
  { key: "lineCount", header: "Lines", mono: true, align: "end" },
];

/** What a user may do next from each status (FR-36.1 lifecycle Draft -> Active -> Inactive -> Closed). */
export const ACTIONS: Record<BankAccount["status"], { action: AccountAction; label: string; danger?: boolean }[]> = {
  draft: [{ action: "activate", label: "Activate" }],
  active: [{ action: "deactivate", label: "Deactivate" }],
  inactive: [
    { action: "activate", label: "Reactivate" },
    { action: "close", label: "Close account", danger: true },
  ],
  closed: [],
};

/** One bank account: details, lifecycle actions under If-Match, and its imported statements. */
export function BankAccountDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const account = useLoad((signal) => getBankAccount(id, signal), `account-${id}`);
  const statements = useLoad((signal) => listStatements(id, signal), `statements-${id}`);
  const [busy, setBusy] = useState<AccountAction | null>(null);
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [done, setDone] = useState("");

  async function run(action: AccountAction, label: string, acct: BankAccount) {
    setBusy(action);
    setFailure(null);
    setDone("");
    try {
      const next = await transitionBankAccount(acct.id, action, acct.version);
      setDone(`${label}: the account is now ${next.status}.`);
      account.reload();
    } catch (error) {
      const stale = error instanceof TreasuryApiError && error.status === 412;
      setFailure({
        title: stale ? "This account changed since you opened it" : `Could not ${label.toLowerCase()}`,
        problem: describeProblem(error),
      });
      if (stale) account.reload();
    } finally {
      setBusy(null);
    }
  }

  if (account.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Bank account" />
        <LoadFailure title="Could not load the bank account" problem={account.error} retry={account.reload} />
      </div>
    );
  }
  const acct = account.data;
  if (!acct) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Bank account" />
        <Card>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Bank account ${acct.accountCode}`}
        meta={<StatusPill status={acct.status} />}
        actions={
          <div className={styles.actions}>
            {ACTIONS[acct.status].map((a) => (
              <Button key={a.action} variant={a.danger ? "danger" : "secondary"} loading={busy === a.action} onClick={() => void run(a.action, a.label, acct)}>
                {a.label}
              </Button>
            ))}
            {acct.status === "active" ? (
              <Link className={buttonClassName("primary")} to={`/treasury/statements/import?bankAccountId=${acct.id}`}>
                Import statement
              </Link>
            ) : null}
            <Link className={buttonClassName("secondary")} to={`/treasury/checks?bankAccountId=${acct.id}`}>
              Checks
            </Link>
            <Link className={buttonClassName("ghost")} to={`/treasury?legalEntityId=${acct.legalEntityId}`}>
              All accounts
            </Link>
          </div>
        }
      />
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? <CommandFailure title={failure.title} problem={failure.problem} /> : null}
      <Card title="Details" headingLevel={3}>
        <dl className={styles.details}>
          <Detail term="IBAN" mono>
            {acct.ibanMasked}
          </Detail>
          <Detail term="Statement account" mono>
            {acct.statementAccountLast4 ? `ending ${acct.statementAccountLast4}` : "IBAN only"}
          </Detail>
          <Detail term="BIC" mono>
            {acct.bic ?? "None"}
          </Detail>
          <Detail term="Currency" mono>
            {acct.currency}
          </Detail>
          <Detail term="Legal entity" mono>
            {acct.legalEntityId}
          </Detail>
          <Detail term="GL main account" mono>
            {acct.mainAccountId}
          </Detail>
          <Detail term="Version" mono>
            {String(acct.version)}
          </Detail>
          <Detail term="Last changed" mono>
            {dateTime(acct.updatedAt)}
          </Detail>
        </dl>
      </Card>
      <Card title="Statements" headingLevel={3}>
        {statements.error ? (
          <LoadFailure title="Could not load the statements" problem={statements.error} retry={statements.reload} />
        ) : (
          <DataTable
            label="Statements"
            columns={statementColumns}
            rows={statements.data ?? []}
            getRowKey={(row) => row.id}
            loading={statements.loading}
            empty={{
              title: "No statements imported",
              description: acct.status === "active" ? "Import the bank's camt.053 file for this account." : "Activate the account to import statements.",
            }}
            onRowOpen={(row) => navigate(`/treasury/statements/${row.id}`)}
          />
        )}
      </Card>
    </div>
  );
}
