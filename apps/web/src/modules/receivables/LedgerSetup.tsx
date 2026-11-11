import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import {
  backfillGLJournals,
  deleteBankAccount,
  describeProblem,
  getFXSetting,
  listBankAccounts,
  putBankAccount,
  putFXSetting,
  type BackfillResult,
  type BankAccountMapping,
  type FXSetting,
  type Problem,
} from "./api/client";
import { CURRENCY, DAY, UUID } from "./format";
import { CommandFailure, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const ACCOUNT = /^[0-9A-Za-z._-]{1,20}$/;

type Errors = Record<string, string>;

export function validateMapping(v: { currency: string; accountRef: string; glAccount: string; name: string }): Errors {
  const errors: Errors = {};
  if (!CURRENCY.test(v.currency.trim())) errors.currency = "Enter the three-letter currency, such as USD.";
  if (!/^[0-9A-Za-z ]{0,48}$/.test(v.accountRef)) errors.accountRef = "Use letters and digits only, as on the bank statement.";
  if (!ACCOUNT.test(v.glAccount.trim())) errors.glAccount = "Enter a ledger account number, such as 1010.";
  if (v.name.length > 100) errors.name = "Use at most 100 characters.";
  return errors;
}

export function validateFX(v: Pick<FXSetting, "crossCurrencyEnabled" | "functionalCurrency" | "gainAccount" | "lossAccount">): Errors {
  const errors: Errors = {};
  if (v.functionalCurrency.trim() !== "" && !CURRENCY.test(v.functionalCurrency.trim())) errors.functionalCurrency = "Enter the three-letter currency, such as USD.";
  else if (v.crossCurrencyEnabled && v.functionalCurrency.trim() === "") errors.functionalCurrency = "Enter the functional currency to settle across currencies.";
  if (!ACCOUNT.test(v.gainAccount.trim())) errors.gainAccount = "Enter a ledger account number, such as 7100.";
  if (!ACCOUNT.test(v.lossAccount.trim())) errors.lossAccount = "Enter a ledger account number, such as 7200.";
  return errors;
}

export function validateBackfill(v: { openingAccount: string; openingDate: string }): Errors {
  const errors: Errors = {};
  const hasAccount = v.openingAccount.trim() !== "";
  const hasDate = v.openingDate !== "";
  if (hasAccount !== hasDate) errors.openingAccount = "Give the opening account and the opening date together, or leave both empty.";
  else if (hasAccount && !ACCOUNT.test(v.openingAccount.trim())) errors.openingAccount = "Enter a ledger account number.";
  if (hasDate && !DAY.test(v.openingDate)) errors.openingDate = "Enter a date.";
  return errors;
}

/**
 * Ledger setup for receivables (30-2, BR-30.4): which ledger account each bank account's cash is booked to, how the legal entity
 * settles across currencies and where it books the realized exchange gain or loss, and the backfill that queues the journals owed for
 * what happened before receivables posted to the ledger.
 */
export function LedgerSetupPage() {
  const [entity, setEntity] = useState("");
  const ready = UUID.test(entity);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Ledger setup"
        description="Where receivables books its cash, how it books exchange differences, and the journals still owed for earlier activity."
        actions={
          <Link className={buttonClassName("secondary")} to="/receivables/gl-journals">
            Ledger journals
          </Link>
        }
      />
      <Card>
        <div className={styles.form}>
          <FormField id="ls-entity" label="Legal entity">
            {(c) => <EntityLookup entity="legalEntity" {...c} value={entity} onChange={(e) => setEntity(e.target.value)} />}
          </FormField>
        </div>
      </Card>
      {ready ? (
        <>
          <BankAccounts key={`b-${entity}`} legalEntityId={entity} />
          <FXProfile key={`f-${entity}`} legalEntityId={entity} />
          <Backfill key={`k-${entity}`} legalEntityId={entity} />
        </>
      ) : (
        <p className={styles.note}>Choose a legal entity to see and change its ledger setup.</p>
      )}
    </div>
  );
}

function BankAccounts({ legalEntityId }: { legalEntityId: string }) {
  const state = useLoad((signal) => listBankAccounts(legalEntityId, signal), legalEntityId);
  const [values, setValues] = useState({ currency: "", accountRef: "", glAccount: "", name: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const columns: DataTableColumn<BankAccountMapping>[] = [
    { key: "name", header: "Bank account", render: (row) => (row.name !== "" ? row.name : row.accountRef === "" ? "Default account" : "Unnamed account") },
    { key: "currency", header: "Currency", mono: true },
    { key: "accountRef", header: "Statement account", mono: true, render: (row) => (row.accountRef === "" ? "Any account" : row.accountRef) },
    { key: "glAccount", header: "Ledger account", mono: true },
    {
      key: "actions",
      header: "",
      align: "end",
      render: (row) => (
        <Button
          variant="secondary"
          onClick={() => {
            setFailure(null);
            void deleteBankAccount(legalEntityId, row.currency, row.accountRef).then(state.reload, (error) => setFailure(describeProblem(error)));
          }}
        >
          Remove
        </Button>
      ),
    },
  ];

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateMapping(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await putBankAccount({ legalEntityId, currency: values.currency.trim(), accountRef: values.accountRef.trim(), glAccount: values.glAccount.trim(), name: values.name.trim() });
      setValues({ currency: "", accountRef: "", glAccount: "", name: "" });
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Bank accounts" headingLevel={2}>
      <div className={styles.stack}>
        <p className={styles.note}>Cash received through a bank account is booked to the ledger account named here. Cash with no mapping is booked to account 1000.</p>
        {state.error ? (
          <LoadFailure title="Could not load the bank accounts" problem={state.error} retry={state.reload} />
        ) : (
          <DataTable
            label="Bank accounts"
            columns={columns}
            rows={state.data ?? []}
            getRowKey={(row) => `${row.currency}|${row.accountRef}`}
            loading={state.loading}
            empty={{ title: "No bank accounts mapped", description: "Until you map one, all cash is booked to account 1000." }}
          />
        )}
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="ba-currency" label="Currency" error={errors.currency}>
            {(c) => <Input {...c} value={values.currency} onChange={(e) => setValues({ ...values, currency: e.target.value.toUpperCase() })} invalid={Boolean(errors.currency)} />}
          </FormField>
          <FormField id="ba-ref" label="Statement account" optional hint="The account number or IBAN on the bank statement. Leave empty for the default account of the currency." error={errors.accountRef}>
            {(c) => <Input {...c} value={values.accountRef} onChange={(e) => setValues({ ...values, accountRef: e.target.value })} invalid={Boolean(errors.accountRef)} />}
          </FormField>
          <FormField id="ba-gl" label="Ledger account" error={errors.glAccount}>
            {(c) => <Input {...c} value={values.glAccount} onChange={(e) => setValues({ ...values, glAccount: e.target.value })} invalid={Boolean(errors.glAccount)} />}
          </FormField>
          <FormField id="ba-name" label="Name" optional error={errors.name}>
            {(c) => <Input {...c} value={values.name} onChange={(e) => setValues({ ...values, name: e.target.value })} invalid={Boolean(errors.name)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save mapping
            </Button>
          </div>
        </form>
        {failure ? <CommandFailure title="Could not change the bank accounts" problem={failure} /> : null}
      </div>
    </Card>
  );
}

function FXProfile({ legalEntityId }: { legalEntityId: string }) {
  const state = useLoad((signal) => getFXSetting(legalEntityId, signal), legalEntityId);
  return (
    <Card title="Exchange differences" headingLevel={2}>
      {state.error ? (
        <LoadFailure title="Could not load the exchange settings" problem={state.error} retry={state.reload} />
      ) : state.data ? (
        <FXForm key={JSON.stringify(state.data)} initial={state.data} />
      ) : (
        <p className={styles.note}>Loading.</p>
      )}
    </Card>
  );
}

function FXForm({ initial }: { initial: FXSetting }) {
  const [values, setValues] = useState(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateFX(values);
    setErrors(found);
    setFailure(null);
    setSaved(false);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await putFXSetting({ ...values, functionalCurrency: values.functionalCurrency.trim(), gainAccount: values.gainAccount.trim(), lossAccount: values.lossAccount.trim() });
      setSaved(true);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate>
      <p className={`${styles.note} ${styles.wide}`}>
        When a payment settles an invoice in another currency, the difference between what the cash is worth on the payment date and what the invoice was booked at is posted as its own journal in the functional currency.
      </p>
      <span className={styles.check}>
        <input id="fx-enabled" type="checkbox" checked={values.crossCurrencyEnabled} onChange={(e) => setValues({ ...values, crossCurrencyEnabled: e.target.checked })} />
        <label htmlFor="fx-enabled">Allow a payment to settle invoices in another currency</label>
      </span>
      <FormField id="fx-functional" label="Functional currency" error={errors.functionalCurrency}>
        {(c) => <Input {...c} value={values.functionalCurrency} onChange={(e) => setValues({ ...values, functionalCurrency: e.target.value.toUpperCase() })} invalid={Boolean(errors.functionalCurrency)} />}
      </FormField>
      <FormField id="fx-gain" label="Realized gain account" error={errors.gainAccount}>
        {(c) => <Input {...c} value={values.gainAccount} onChange={(e) => setValues({ ...values, gainAccount: e.target.value })} invalid={Boolean(errors.gainAccount)} />}
      </FormField>
      <FormField id="fx-loss" label="Realized loss account" error={errors.lossAccount}>
        {(c) => <Input {...c} value={values.lossAccount} onChange={(e) => setValues({ ...values, lossAccount: e.target.value })} invalid={Boolean(errors.lossAccount)} />}
      </FormField>
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          Save exchange settings
        </Button>
      </div>
      {saved ? (
        <Alert tone="success" title="Saved" live="status">
          The exchange settings are saved.
        </Alert>
      ) : null}
      {failure ? <CommandFailure title="Could not save the exchange settings" problem={failure} /> : null}
    </form>
  );
}

function Backfill({ legalEntityId }: { legalEntityId: string }) {
  const [values, setValues] = useState({ openingAccount: "", openingDate: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<BackfillResult | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateBackfill(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const body = { legalEntityId, ...(values.openingAccount.trim() !== "" ? { openingAccount: values.openingAccount.trim(), openingDate: values.openingDate } : {}) };
      setResult(await backfillGLJournals(body));
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Journals for earlier activity" headingLevel={2}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <p className={`${styles.note} ${styles.wide}`}>
          Queues the ledger journals for cash received, payments applied, dunning fees, interest, down payments and deposits, and deductions written off before receivables posted to the ledger. Running it again queues nothing twice. Fill in the opening account only when the ledger does not already carry the receivable that was handed over from the old system.
        </p>
        <FormField id="bf-account" label="Opening balance account" optional error={errors.openingAccount}>
          {(c) => <Input {...c} value={values.openingAccount} onChange={(e) => setValues({ ...values, openingAccount: e.target.value })} invalid={Boolean(errors.openingAccount)} />}
        </FormField>
        <FormField id="bf-date" label="Opening date" optional error={errors.openingDate}>
          {(c) => <Input {...c} type="date" value={values.openingDate} onChange={(e) => setValues({ ...values, openingDate: e.target.value })} invalid={Boolean(errors.openingDate)} />}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Queue the journals
          </Button>
        </div>
        {result ? (
          <Alert tone="success" title="Journals queued" live="status">
            {result.receipts} cash receipts, {result.applications} applications, {result.dunningFees} dunning fees, {result.interest} interest charges, {result.downPayments} down payment or deposit postings, {result.deductions} deductions and {result.openingItems} opening balances were queued.
            {result.more ? " There is more history to work through; run it again." : ""}
          </Alert>
        ) : null}
        {failure ? <CommandFailure title="Could not queue the journals" problem={failure} /> : null}
      </form>
    </Card>
  );
}
