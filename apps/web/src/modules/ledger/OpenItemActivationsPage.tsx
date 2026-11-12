import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { activateOpenItemAccount, DUTY_LEDGER_ADMIN, fetchActivationRuns, newKey, type ActivationRun } from "./api/client";
import { LedgerPicker, ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

const ACCOUNT = /^[A-Za-z0-9._-]{1,20}$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

const columns: DataTableColumn<ActivationRun>[] = [
  { key: "createdAt", header: "Run", mono: true, render: (r) => r.createdAt.slice(0, 10) },
  { key: "account", header: "Account", mono: true, render: (r) => `${r.account} ${r.accountName}` },
  { key: "activationDate", header: "Activation date", mono: true },
  { key: "itemsCreated", header: "Opening items", align: "end", mono: true },
  { key: "lineItemsCreated", header: "Later line items", align: "end", mono: true },
  { key: "totalDebit", header: "Debit total", align: "end", mono: true },
  { key: "totalCredit", header: "Credit total", align: "end", mono: true },
  { key: "createdBy", header: "By" },
];

/** Activation runs of a ledger: accounts with history switched on for open-item management (FR-11.19). */
export function OpenItemActivationsPage() {
  const [ledgerId, setLedgerId] = useLedgerParam();
  const result = useQuery({ queryKey: ["ledger", "open-item-activations", ledgerId], queryFn: () => fetchActivationRuns(ledgerId), enabled: ledgerId !== "" });
  const query = ledgerQuery(ledgerId);
  const canActivate = holdsDuty(usePrincipalDuties(), DUTY_LEDGER_ADMIN);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Open-item activation"
        description="An account that already has a balance is switched on for open-item management by an activation run: the lines up to the activation date become one opening open item per dimension combination with a balance, each later line becomes an open item of its own, so the open items add up to the ledger balance."
        actions={
          ledgerId && canActivate ? (
            <Link className={buttonClassName("primary")} to={`/ledger/open-item-activations/new${query}`}>
              Activate an account
            </Link>
          ) : undefined
        }
      />
      <LedgerPicker id="oac-ledger" label="Choose a ledger" ledgerId={ledgerId} onChange={setLedgerId} />
      {ledgerId === "" ? (
        <p className={styles.note}>Choose a ledger to see its activation runs.</p>
      ) : result.isError ? (
        <Alert tone="danger" title="Could not load the activation runs" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {errorText(result.error)}
        </Alert>
      ) : (
        <DataTable
          label="Activation runs"
          columns={columns}
          rows={result.data ?? []}
          getRowKey={(r) => r.id}
          loading={result.isPending}
          empty={{ title: "No activation runs yet", description: "Activate a clearing, GR/IR or suspense account that already has postings." }}
        />
      )}
      {ledgerId ? (
        <p className={styles.note}>
          <Link to={`/ledger/open-item-accounts${query}`}>Open-item accounts</Link>
        </p>
      ) : null}
    </div>
  );
}

function ActivationForm({ ledgerId }: { ledgerId: string }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [account, setAccount] = useState("");
  const [date, setDate] = useState("");
  const [key] = useState(newKey);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<ReactNode>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!ACCOUNT.test(account.trim())) found.account = "Enter the account number, up to 20 letters, digits, dots, underscores or dashes.";
    if (!DATE.test(date.trim())) found.date = "Enter the activation date as YYYY-MM-DD.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await activateOpenItemAccount(ledgerId, account.trim(), date.trim(), key);
      await queryClient.invalidateQueries({ queryKey: ["ledger"] });
      navigate(`/ledger/open-item-activations${ledgerQuery(ledgerId)}`);
    } catch (e) {
      setFailure(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label="Activate an account">
        <FormField id="oac-account" label="Account number" hint="A postable balance-sheet account that is not a customer or vendor reconciliation account." error={errors.account}>
          {(c) => <Input {...c} className={styles.mono} maxLength={20} value={account} onChange={(e) => setAccount(e.target.value)} invalid={Boolean(errors.account)} />}
        </FormField>
        <FormField id="oac-date" label="Activation date" hint="YYYY-MM-DD. Lines up to and including this date are carried in as opening items dated this day; later lines become items of their own." error={errors.date}>
          {(c) => <Input {...c} className={styles.mono} placeholder="2026-04-01" value={date} onChange={(e) => setDate(e.target.value)} invalid={Boolean(errors.date)} />}
        </FormField>
        {failure ? (
          <div className={styles.wide}>
            <Alert tone="danger" title="Could not activate the account" live="alert">{failure}</Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>Activate account</Button>
          <Link to={`/ledger/open-item-activations${ledgerQuery(ledgerId)}`}>Cancel</Link>
        </div>
      </form>
    </Card>
  );
}

export function NewOpenItemActivationPage() {
  const [ledgerId] = useLedgerParam();
  const canActivate = holdsDuty(usePrincipalDuties(), DUTY_LEDGER_ADMIN);
  return (
    <div className={styles.stack}>
      <PageHeader title="Activate an account" description="Posted lines are not changed. The run only creates the open items and switches the account on." />
      {!ledgerId ? (
        <p className={styles.note}>Open this page from the activation list of a ledger so it knows which ledger to use.</p>
      ) : canActivate ? (
        <ActivationForm ledgerId={ledgerId} />
      ) : (
        <Alert tone="info" title="You cannot run an activation">Activation runs need the Administer ledger duty.</Alert>
      )}
    </div>
  );
}
