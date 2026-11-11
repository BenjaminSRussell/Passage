import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { fetchOpenItemAccounts, putOpenItemAccount, type OpenItemAccount } from "./api/client";
import { LedgerPicker, ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

const ACCOUNT = /^[A-Za-z0-9._-]{1,20}$/;

/** Open-item management of accounts in a ledger (FR-11.19, BR-11.18). */
export function OpenItemAccountsPage() {
  const queryClient = useQueryClient();
  const [ledgerId, setLedgerId] = useLedgerParam();
  const [account, setAccount] = useState("");
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<ReactNode>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const result = useQuery({ queryKey: ["ledger", "open-item-accounts", ledgerId], queryFn: () => fetchOpenItemAccounts(ledgerId), enabled: ledgerId !== "" });
  const query = ledgerQuery(ledgerId);

  async function set(number: string, enabled: boolean) {
    setFailure(null);
    setBusy(number);
    try {
      await putOpenItemAccount(ledgerId, number, enabled);
      await queryClient.invalidateQueries({ queryKey: ["ledger", "open-item-accounts"] });
      setAccount("");
    } catch (e) {
      setFailure(errorText(e));
    } finally {
      setBusy(null);
    }
  }

  function add(event: FormEvent) {
    event.preventDefault();
    if (!ACCOUNT.test(account)) {
      setError("Enter the account number, up to 20 letters, digits, dots, underscores or dashes.");
      return;
    }
    setError(undefined);
    void set(account, true);
  }

  const columns: DataTableColumn<OpenItemAccount>[] = [
    { key: "account", header: "Account", mono: true },
    { key: "name", header: "Name" },
    { key: "enabled", header: "Open-item management", render: (a) => <Badge tone={a.enabled ? "success" : "neutral"}>{a.enabled ? "On" : "Off"}</Badge> },
    { key: "openItems", header: "Open items", align: "end" },
    {
      key: "actions",
      header: "Action",
      render: (a) => (
        <Button size="sm" loading={busy === a.account} onClick={() => void set(a.account, !a.enabled)}>
          {a.enabled ? `Switch off ${a.account}` : `Switch on ${a.account}`}
        </Button>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Open-item accounts"
        description="Every line posted on an open-item managed account creates an open item that is cleared later. Only postable balance-sheet accounts that are not customer or vendor reconciliation accounts qualify, and an account that already has a balance needs an activation run."
      />
      <LedgerPicker id="oa-ledger" label="Choose a ledger" ledgerId={ledgerId} onChange={setLedgerId} />
      {ledgerId === "" ? (
        <p className={styles.note}>Choose a ledger to see its open-item accounts.</p>
      ) : (
        <>
          <Card>
            <form className={styles.form} onSubmit={add} noValidate aria-label="Switch on open-item management">
              <FormField id="oa-account" label="Account number" error={error}>
                {(c) => <Input {...c} className={styles.mono} maxLength={20} value={account} onChange={(e) => setAccount(e.target.value)} invalid={Boolean(error)} />}
              </FormField>
              <div className={styles.formActions}>
                <Button type="submit" variant="primary">Switch on</Button>
              </div>
            </form>
          </Card>
          {failure ? (
            <Alert tone="danger" title="Could not change the account" live="alert">{failure}</Alert>
          ) : null}
          {result.isError ? (
            <Alert tone="danger" title="Could not load the open-item accounts" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
              {errorText(result.error)}
            </Alert>
          ) : (
            <DataTable
              label="Open-item accounts"
              columns={columns}
              rows={result.data ?? []}
              getRowKey={(a) => a.account}
              loading={result.isPending}
              empty={{ title: "No open-item accounts yet", description: "Switch on a clearing, GR/IR or suspense account to start collecting open items." }}
            />
          )}
          <p className={styles.note}>
            <Link to={`/ledger/open-items${query}`}>Open items</Link> · <Link to={`/ledger/open-item-activations${query}`}>Open-item activation</Link>
          </p>
        </>
      )}
    </div>
  );
}
