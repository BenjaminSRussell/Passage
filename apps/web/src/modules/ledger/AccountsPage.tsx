import { useInfiniteQuery } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { exportAccountsCsv, fetchAccounts, type Account } from "./api/client";
import { CHART_CODE, DUTY_ACCOUNT_WRITE, typeLabel } from "./accountCommon";
import { ExportButton } from "./ExportButton";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<Account>[] = [
  { key: "accountNumber", header: "Account", mono: true },
  { key: "name", header: "Name" },
  { key: "accountType", header: "Type", render: (a) => typeLabel(a.accountType) },
  {
    key: "isPostable",
    header: "Postable",
    render: (a) => (a.isPostable ? <Badge tone="success">Postable</Badge> : <Badge tone="neutral">Total or reporting</Badge>),
  },
  { key: "reconciliationAccountType", header: "Reconciliation", render: (a) => (a.reconciliationAccountType === "none" ? "None" : typeLabel(a.reconciliationAccountType)) },
  { key: "suspendedFrom", header: "Suspended from", render: (a) => a.suspendedFrom ?? "" },
];

function errorText(error: unknown): ReactNode {
  return <ProblemMessage problem={error} fallback="The request failed." />;
}

/** The chart of accounts: accounts of one chart by number, with a CSV export and the account form. */
export function AccountsPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const chart = params.get("chart") ?? "";
  const valid = CHART_CODE.test(chart);
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_ACCOUNT_WRITE);
  const [draft, setDraft] = useState(chart);
  const [error, setError] = useState<string | undefined>();

  const result = useInfiniteQuery({
    queryKey: ["ledger", "accounts", chart],
    queryFn: ({ pageParam }) => fetchAccounts(chart, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextAfter || undefined,
    enabled: valid,
  });
  const rows = result.data?.pages.flatMap((p) => p.items) ?? [];

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const code = draft.trim().toUpperCase();
    if (!CHART_CODE.test(code)) {
      setError("Use 1 to 10 capital letters or digits, such as STD.");
      return;
    }
    setError(undefined);
    setParams({ chart: code });
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Chart of accounts"
        description="The main accounts of a chart in account-number order. Open an account to change its name, type, posting rule or suspension."
        actions={
          canWrite && valid ? (
            <Link className={buttonClassName("primary")} to={`/ledger/accounts/new?chart=${encodeURIComponent(chart)}`}>
              New account
            </Link>
          ) : undefined
        }
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Chart">
          <FormField id="coa-chart" label="Chart code" error={error} hint="The chart of the ledger, for example STD.">
            {(c) => <Input {...c} className={styles.mono} value={draft} maxLength={10} onChange={(e) => setDraft(e.target.value.toUpperCase())} invalid={Boolean(error)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Show accounts
            </Button>
          </div>
        </form>
      </Card>
      {!valid ? (
        <p className={styles.note}>Enter a chart code to list its accounts.</p>
      ) : result.isError && rows.length === 0 ? (
        <Alert tone="danger" title="Could not load the accounts" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {errorText(result.error)}
        </Alert>
      ) : (
        <>
          <DataTable
            label="Chart of accounts"
            columns={columns}
            rows={rows}
            getRowKey={(a) => a.accountNumber}
            loading={result.isPending}
            empty={{ title: "No accounts in this chart", description: "Check the chart code, or add the first account." }}
            onRowOpen={(a) => navigate(`/ledger/accounts/${encodeURIComponent(chart)}/${encodeURIComponent(a.accountNumber)}`)}
          />
          {result.isFetchNextPageError ? (
            <Alert tone="danger" title="Could not load more accounts" live="alert" action={<Button size="sm" onClick={() => void result.fetchNextPage()}>Try again</Button>}>
              {errorText(result.error)}
            </Alert>
          ) : null}
          {result.hasNextPage && !result.isFetchNextPageError ? (
            <div className={styles.formActions}>
              <Button loading={result.isFetchingNextPage} onClick={() => void result.fetchNextPage()}>
                Load more accounts
              </Button>
              <span className={styles.note}>{rows.length} accounts shown</span>
            </div>
          ) : null}
          {rows.length > 0 ? <ExportButton label="Export chart (CSV)" run={() => exportAccountsCsv(chart)} /> : null}
        </>
      )}
    </div>
  );
}
