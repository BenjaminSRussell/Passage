import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  abandonLineTransaction,
  closeBankReconciliation,
  createBankReconciliation,
  createLineTransaction,
  describeProblem,
  excludeStatementLine,
  getBankAccount,
  getBankReconciliation,
  includeStatementLine,
  listBankAccounts,
  listBankReconciliations,
  listStatementLines,
  listStatements,
  newKey,
  RECON_STATES,
  reopenBankReconciliation,
  setReconBalances,
  TRANSACTION_KINDS,
  type BankAccount,
  type BankReconciliation,
  type Problem,
  type ReconState,
  type StatementLine,
  type TransactionKind,
} from "./api/client";
import { dateTime, money, SIGNED_DECIMAL, UUID } from "./format";
import { CommandFailure, Detail, keepKeyAfter, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Errors = Record<string, string>;

const KIND_LABELS: Record<TransactionKind, string> = { bank_fee: "Bank fee", interest: "Interest", unknown_receipt: "Unidentified receipt" };
const STATE_LABELS: Record<ReconState, string> = { open: "Open", closed: "Closed", reopened: "Reopened" };

/** What a person can do about the refusals the close, reopen and posting routes give. */
const FAILURES: Record<string, string> = {
  "TRS-0410": "The difference is not zero, so the period cannot close",
  "TRS-0504": "This statement already has a reconciliation",
  "TRS-0606": "This account has no opening balance from the migration",
  "TRS-0607": "Only the first reconciliation after the cutover starts from the opening balance",
  "TRS-0506": "The period is closed",
  "TRS-0507": "Some lines are neither matched nor excluded",
  "TRS-0508": "A later period of this account is closed",
  "TRS-0509": "You closed this period, so someone else must reopen it",
  "TRS-0512": "This line cannot be booked as that kind of transaction",
  "TRS-0517": "A bank transaction from a line is still being posted",
  "TRS-0518": "The ledger refused the transaction",
  "TRS-0519": "The ledger did not answer",
  "TRS-0520": "You stated the book balance, so someone else must close the period",
};

function accountLabel(a: BankAccount | undefined): string {
  return a ? `${a.accountCode} (${a.ibanMasked}, ${a.currency})` : "Bank account";
}

/** A signed decimal is valid for the balance fields; the service checks the currency's minor units. */
export function validateBalances(book: string, outstanding: string): Errors {
  const errors: Errors = {};
  if (!SIGNED_DECIMAL.test(book.trim())) errors.bookBalance = "Enter the balance as a number, such as 12500.00 or -300.50.";
  if (outstanding.trim() !== "" && !SIGNED_DECIMAL.test(outstanding.trim())) errors.outstandingAmount = "Enter a number, such as 1200.00. Leave it empty for none.";
  return errors;
}

/** Reconciliations of a legal entity by account and state (FR-36.5). */
export function ReconciliationsPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const legalEntityId = params.get("legalEntityId") ?? "";
  const bankAccountId = params.get("bankAccountId") ?? "";
  const state = (params.get("state") ?? "") as ReconState | "";
  const chosen = UUID.test(legalEntityId);
  const accounts = useLoad((signal) => (chosen ? listBankAccounts(legalEntityId, signal) : Promise.resolve([] as BankAccount[])), legalEntityId);
  const list = useLoad(
    (signal) => (chosen ? listBankReconciliations({ legalEntityId, bankAccountId: bankAccountId || undefined, state: state || undefined }, signal) : Promise.resolve({ items: [] as BankReconciliation[] })),
    `${legalEntityId}|${bankAccountId}|${state}`,
  );
  const byId = new Map((accounts.data ?? []).map((a) => [a.id, a]));

  function set(name: string, value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set(name, value);
    else next.delete(name);
    if (name === "legalEntityId") next.delete("bankAccountId");
    setParams(next);
  }

  const columns: DataTableColumn<BankReconciliation>[] = [
    { key: "periodTo", header: "Period", mono: true, render: (r) => `${r.periodFrom} to ${r.periodTo}` },
    { key: "bankAccountId", header: "Bank account", render: (r) => accountLabel(byId.get(r.bankAccountId)) },
    { key: "state", header: "State", render: (r) => <StatusPill status={r.state} /> },
    { key: "unmatched", header: "Unmatched lines", align: "end", mono: true, render: (r) => String(r.summary.unmatchedCount) },
    { key: "difference", header: "Difference", align: "end", mono: true, render: (r) => money(r.difference, r.currency) },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Bank reconciliations"
        description="Match each bank statement to the books. A period closes when every line is matched or excluded and the difference is zero."
        actions={
          <Link className={buttonClassName("primary")} to={`/treasury/reconciliations/new${chosen ? `?legalEntityId=${encodeURIComponent(legalEntityId)}` : ""}`}>
            Open a reconciliation
          </Link>
        }
      />
      <Card>
        <div className={styles.toolbar}>
          <FormField id="rc-entity" label="Legal entity">
            {(c) => <EntityLookup entity="legalEntity" {...c} value={legalEntityId} onChange={(e) => set("legalEntityId", e.target.value)} />}
          </FormField>
          <FormField id="rc-account" label="Bank account">
            {(c) => (
              <Select {...c} value={bankAccountId} onChange={(e) => set("bankAccountId", e.target.value)}>
                <option value="">All accounts</option>
                {(accounts.data ?? []).map((a) => (
                  <option key={a.id} value={a.id}>
                    {accountLabel(a)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="rc-state" label="State">
            {(c) => (
              <Select {...c} value={state} onChange={(e) => set("state", e.target.value)}>
                <option value="">Any state</option>
                {RECON_STATES.map((s) => (
                  <option key={s} value={s}>
                    {STATE_LABELS[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
      </Card>
      {!chosen ? (
        <Card>
          <EmptyState icon="database" title="Choose a legal entity" description="Reconciliations belong to one legal entity." />
        </Card>
      ) : list.error ? (
        <LoadFailure title="Could not load the reconciliations" problem={list.error} retry={list.reload} />
      ) : (
        <DataTable
          label="Bank reconciliations"
          columns={columns}
          rows={list.data?.items ?? []}
          getRowKey={(r) => r.id}
          loading={list.loading}
          empty={{ title: "No reconciliations yet", description: "Open one from an imported bank statement." }}
          onRowOpen={(r) => navigate(`/treasury/reconciliations/${r.id}`)}
        />
      )}
    </div>
  );
}

/** Open the reconciliation of one imported statement. */
export function NewReconciliationPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [legalEntityId, setLegalEntityId] = useState(params.get("legalEntityId") ?? "");
  const [bankAccountId, setBankAccountId] = useState("");
  const [statementId, setStatementId] = useState("");
  const [book, setBook] = useState("");
  const [fromOpening, setFromOpening] = useState(false);
  const [outstanding, setOutstanding] = useState("");
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [key, setKey] = useState(newKey());
  const accounts = useLoad((signal) => (UUID.test(legalEntityId) ? listBankAccounts(legalEntityId, signal) : Promise.resolve([] as BankAccount[])), legalEntityId);
  const statements = useLoad((signal) => (UUID.test(bankAccountId) ? listStatements(bankAccountId, signal) : Promise.resolve([])), bankAccountId);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateBalances(fromOpening ? "0" : book, outstanding);
    if (!UUID.test(statementId)) found.statementId = "Choose the statement to reconcile.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createBankReconciliation(
        fromOpening
          ? { statementId, bookBalanceSource: "opening", outstandingAmount: outstanding.trim() || undefined }
          : { statementId, bookBalance: book.trim(), outstandingAmount: outstanding.trim() || undefined },
        key,
      );
      navigate(`/treasury/reconciliations/${created.id}`);
    } catch (error) {
      if (!keepKeyAfter(error)) setKey(newKey());
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Open a reconciliation" description="Pick an imported statement and state the book balance for the same date. You can correct both balances until the period closes." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="nr-entity" label="Legal entity">
            {(c) => (
              <EntityLookup
                entity="legalEntity"
                {...c}
                value={legalEntityId}
                onChange={(e) => {
                  setLegalEntityId(e.target.value);
                  setBankAccountId("");
                  setStatementId("");
                }}
              />
            )}
          </FormField>
          <FormField id="nr-account" label="Bank account">
            {(c) => (
              <Select
                {...c}
                value={bankAccountId}
                onChange={(e) => {
                  setBankAccountId(e.target.value);
                  setStatementId("");
                }}
              >
                <option value="">Choose a bank account</option>
                {(accounts.data ?? []).map((a) => (
                  <option key={a.id} value={a.id}>
                    {accountLabel(a)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="nr-statement" label="Statement" error={errors.statementId}>
            {(c) => (
              <Select {...c} value={statementId} onChange={(e) => setStatementId(e.target.value)} invalid={Boolean(errors.statementId)}>
                <option value="">Choose a statement</option>
                {(statements.data ?? []).map((s) => (
                  <option key={s.id} value={s.id}>
                    {`${s.statementId}, closing ${money(s.closingBalance, s.currency)} on ${s.closingDate}`}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="nr-book-source" label="Book balance from" hint="The first reconciliation after the cutover can start from the opening balance the migration loaded for this account.">
            {(c) => (
              <Select {...c} value={fromOpening ? "opening" : "stated"} onChange={(e) => setFromOpening(e.target.value === "opening")}>
                <option value="stated">I state it</option>
                <option value="opening">The opening balance from the migration</option>
              </Select>
            )}
          </FormField>
          {fromOpening ? null : (
            <FormField id="nr-book" label="Book balance" hint="The bank account's balance in the books on the statement's closing date." error={errors.bookBalance}>
              {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={book} onChange={(e) => setBook(e.target.value)} invalid={Boolean(errors.bookBalance)} />}
            </FormField>
          )}
          <FormField id="nr-outstanding" label="Items not yet on the statement" hint="Positive for outstanding payments such as checks, negative for deposits in transit." error={errors.outstandingAmount}>
            {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={outstanding} onChange={(e) => setOutstanding(e.target.value)} invalid={Boolean(errors.outstandingAmount)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <CommandFailure title={FAILURES[failure.code] ?? "Could not open the reconciliation"} problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Open reconciliation
            </Button>
            <Link className={buttonClassName("secondary")} to="/treasury/reconciliations">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

interface DetailView {
  recon: BankReconciliation;
  account: BankAccount;
  lines: StatementLine[];
}

async function loadAll(id: string, signal: AbortSignal): Promise<DetailView> {
  const recon = await getBankReconciliation(id, signal);
  const account = await getBankAccount(recon.bankAccountId, signal);
  const lines: StatementLine[] = [];
  let after = 0;
  for (let guard = 0; guard < 50; guard++) {
    const page = await listStatementLines(recon.statementId, after, 100, signal);
    lines.push(...page.items);
    if (page.nextAfterLineNo === null) break;
    after = page.nextAfterLineNo;
  }
  return { recon, account, lines };
}

export function ReconciliationDetailPage() {
  const { id = "" } = useParams();
  return <ReconciliationDetail key={id} id={id} />;
}

function ReconciliationDetail({ id }: { id: string }) {
  const state = useLoad((signal) => loadAll(id, signal), id);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [book, setBook] = useState<string | null>(null);
  const [outstanding, setOutstanding] = useState<string | null>(null);
  const [balanceErrors, setBalanceErrors] = useState<Errors>({});
  const [reopenReason, setReopenReason] = useState("");
  const [selected, setSelected] = useState<number | null>(null);
  const [reason, setReason] = useState("");
  const [notice, setNotice] = useState<string | null>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Bank reconciliation" />
        <LoadFailure title="Could not load the reconciliation" problem={state.error} retry={state.reload} />
      </div>
    );
  }
  const view = state.data;
  if (!view) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Bank reconciliation" />
        <Card>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }
  const { recon, account, lines } = view;
  const closed = recon.state === "closed";
  const bookValue = book ?? recon.bookBalance;
  const outstandingValue = outstanding ?? recon.outstandingAmount;
  const line = lines.find((l) => l.lineNo === selected);

  async function run(action: () => Promise<unknown>, done: string) {
    setBusy(true);
    setFailure(null);
    setNotice(null);
    try {
      await action();
      setNotice(done);
      setBook(null);
      setOutstanding(null);
      setReason("");
      setReopenReason("");
      setSelected(null);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  function saveBalances(event: FormEvent) {
    event.preventDefault();
    const found = validateBalances(bookValue, outstandingValue);
    setBalanceErrors(found);
    if (Object.keys(found).length > 0) return;
    void run(() => setReconBalances(recon.id, recon.version, bookValue.trim(), outstandingValue.trim() || "0"), "Balances saved.");
  }

  const columns: DataTableColumn<StatementLine>[] = [
    { key: "lineNo", header: "Line", mono: true, align: "end" },
    { key: "bookingDate", header: "Booked", mono: true },
    { key: "amount", header: "Amount", mono: true, align: "end", render: (l) => money(l.amount, l.currency) },
    { key: "counterpartyName", header: "Counterparty", render: (l) => l.counterpartyName || "Not given" },
    { key: "reference", header: "Reference", mono: true, render: (l) => l.endToEndRef || l.reference || "None" },
    { key: "status", header: "Status", render: (l) => <StatusPill status={l.status} /> },
    {
      key: "act",
      header: "Action",
      render: (l) =>
        closed ? (
          "Locked"
        ) : (
          <Button size="sm" onClick={() => setSelected(l.lineNo)} aria-label={`Act on line ${l.lineNo}`}>
            Act on line
          </Button>
        ),
    },
  ];

  const zero = Number(recon.difference) === 0;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Reconciliation ${recon.periodFrom} to ${recon.periodTo}`}
        description={`${accountLabel(account)}. ${STATE_LABELS[recon.state]}.`}
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to={`/treasury/statements/${recon.statementId}`}>
              Statement
            </Link>
            <Link className={buttonClassName("secondary")} to="/treasury/reconciliations">
              All reconciliations
            </Link>
          </div>
        }
      />
      {notice ? (
        <Alert tone="success" title={notice} live="status">
          The figures below are up to date.
        </Alert>
      ) : null}
      {failure ? <CommandFailure title={FAILURES[failure.code] ?? "That did not work"} problem={failure} /> : null}
      <Card title="Summary" headingLevel={3}>
        <dl className={styles.details}>
          <Detail term="Bank closing balance" mono>
            {money(recon.closingBalance, recon.currency)}
          </Detail>
          <Detail term="Book balance" mono>
            {money(recon.bookBalance, recon.currency)}
          </Detail>
          <Detail term="Not yet on the statement" mono>
            {money(recon.outstandingAmount, recon.currency)}
          </Detail>
          <Detail term="Excluded lines" mono>
            {`${recon.summary.excludedCount} lines, ${money(recon.summary.excludedTotal, recon.currency)}`}
          </Detail>
          <Detail term="Difference" mono>
            {money(recon.difference, recon.currency)}
          </Detail>
          <Detail term="Matched lines">{`${recon.summary.matchedCount} of ${recon.summary.lines}`}</Detail>
          <Detail term="Unmatched lines">{`${recon.summary.unmatchedCount}, ${money(recon.summary.unmatchedTotal, recon.currency)}`}</Detail>
          <Detail term={recon.bookBalanceSource === "opening" ? "Book balance" : "Book balance stated by"}>
            {recon.bookBalanceSource === "opening" ? "The opening balance from the migration" : recon.bookBalanceStatedBy ? `${recon.bookBalanceStatedBy} on ${dateTime(recon.bookBalanceStatedAt)}` : "Not recorded"}
          </Detail>
          {recon.closedAt ? <Detail term="Closed">{`${recon.closedBy ?? "Unknown"} on ${dateTime(recon.closedAt)}`}</Detail> : null}
          {recon.reopenedAt ? <Detail term="Reopened">{`${recon.reopenedBy ?? "Unknown"} on ${dateTime(recon.reopenedAt)}: ${recon.reopenReason ?? ""}`}</Detail> : null}
        </dl>
        <div className={styles.actions}>
          {closed ? null : (
            <Button variant="primary" loading={busy} disabled={!zero || recon.summary.unmatchedCount > 0} onClick={() => void run(() => closeBankReconciliation(recon.id, recon.version), "Period closed.")}>
              Close the period
            </Button>
          )}
        </div>
        {!closed && (!zero || recon.summary.unmatchedCount > 0) ? <p className={styles.note}>Close the period when every line is matched or excluded and the difference is zero.</p> : null}
      </Card>
      {closed ? (
        <Card title="Reopen the period" headingLevel={3}>
          <form
            className={styles.form}
            onSubmit={(e) => {
              e.preventDefault();
              if (reopenReason.trim() !== "") void run(() => reopenBankReconciliation(recon.id, recon.version, reopenReason.trim()), "Period reopened.");
            }}
          >
            <FormField id="rc-reopen" label="Reason" hint="Kept with who reopened the period.">
              {(c) => <Input {...c} value={reopenReason} onChange={(e) => setReopenReason(e.target.value)} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" loading={busy} disabled={reopenReason.trim() === ""}>
                Reopen the period
              </Button>
            </div>
          </form>
        </Card>
      ) : (
        <Card title="Balances" headingLevel={3}>
          <form className={styles.form} onSubmit={saveBalances} noValidate>
            <FormField id="rc-book" label="Book balance" error={balanceErrors.bookBalance}>
              {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={bookValue} onChange={(e) => setBook(e.target.value)} invalid={Boolean(balanceErrors.bookBalance)} />}
            </FormField>
            <FormField id="rc-outstanding" label="Items not yet on the statement" error={balanceErrors.outstandingAmount}>
              {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={outstandingValue} onChange={(e) => setOutstanding(e.target.value)} invalid={Boolean(balanceErrors.outstandingAmount)} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" loading={busy}>
                Save balances
              </Button>
            </div>
          </form>
        </Card>
      )}
      <Card title="Statement lines" headingLevel={3}>
        <DataTable label="Statement lines" columns={columns} rows={lines} getRowKey={(l) => String(l.lineNo)} empty={{ title: "No lines", description: "The statement has no entries." }} />
      </Card>
      {line && !closed ? (
        <Card title={`Line ${line.lineNo}: ${money(line.amount, line.currency)}`} headingLevel={3}>
          <div className={styles.form}>
            <FormField id="rc-line-reason" label="Reason" hint="Needed to exclude the line or give up on a bank transaction.">
              {(c) => <Input {...c} value={reason} onChange={(e) => setReason(e.target.value)} />}
            </FormField>
            <div className={styles.actions}>
              {line.status === "excluded" ? (
                <Button loading={busy} onClick={() => void run(() => includeStatementLine(recon.id, line.lineNo), "Line put back.")}>
                  Put the line back
                </Button>
              ) : (
                <Button loading={busy} disabled={reason.trim() === ""} onClick={() => void run(() => excludeStatementLine(recon.id, line.lineNo, reason.trim()), "Line excluded.")}>
                  Exclude the line
                </Button>
              )}
              {line.status !== "matched" && line.status !== "created" && line.status !== "excluded"
                ? TRANSACTION_KINDS.map((k) => (
                    <Button key={k} loading={busy} onClick={() => void run(() => createLineTransaction(recon.id, line.lineNo, k), `${KIND_LABELS[k]} booked.`)}>
                      {`Book as ${KIND_LABELS[k].toLowerCase()}`}
                    </Button>
                  ))
                : null}
              <Button loading={busy} disabled={reason.trim() === ""} onClick={() => void run(() => abandonLineTransaction(recon.id, line.lineNo, reason.trim()), "Bank transaction given up.")}>
                Give up the bank transaction
              </Button>
            </div>
          </div>
        </Card>
      ) : null}
    </div>
  );
}
