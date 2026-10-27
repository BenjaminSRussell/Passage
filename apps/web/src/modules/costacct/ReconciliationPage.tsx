import { useState, type FormEvent } from "react";
import { Badge, Button, Card, DataTable, FormField, Input, PageHeader, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeError, listCostElements, reconcile, type JournalBalance, type Reconciliation } from "./api/client";
import { CURRENCY, formatAmount, isPeriod, parseJournalCsv, UUID } from "./logic";
import styles from "./pages.module.css";
import { ErrorList, thisPeriod } from "./parts";
import { useLoad } from "./useLoad";

type Line = Reconciliation["lines"][number];

const unmappedColumns: DataTableColumn<JournalBalance>[] = [
  { key: "mainAccount", header: "Main account", mono: true },
  { key: "amount", header: "Journal balance", align: "end", mono: true, render: (b) => formatAmount(b.amount) },
];

/** FR-38.11: compares Σ primary cost entries per element with the journal balances of the period. */
export function ReconciliationPage() {
  const [legalEntityId, setLegalEntityId] = useState("");
  const [period, setPeriod] = useState(thisPeriod());
  const [currency, setCurrency] = useState("USD");
  const [csv, setCsv] = useState("mainAccount,amount\n");
  const [errors, setErrors] = useState<string[]>([]);
  const [failure, setFailure] = useState<string | null>(null);
  const [result, setResult] = useState<Reconciliation | null>(null);
  const [busy, setBusy] = useState(false);
  const le = UUID.test(legalEntityId.trim()) ? legalEntityId.trim() : "";
  const elements = useLoad((signal) => (le ? listCostElements(le, signal) : Promise.resolve({ items: [] })), le);
  const code = (id: string) => (elements.data?.items ?? []).find((e) => e.id === id)?.code ?? id;

  const lineColumns: DataTableColumn<Line>[] = [
    { key: "costElementId", header: "Cost element", mono: true, render: (l) => code(l.costElementId) },
    { key: "journal", header: "Journal", align: "end", mono: true, render: (l) => formatAmount(l.journal) },
    { key: "costLedger", header: "Cost ledger", align: "end", mono: true, render: (l) => formatAmount(l.costLedger) },
    { key: "difference", header: "Difference", align: "end", mono: true, render: (l) => formatAmount(l.difference) },
  ];

  async function submit(e: FormEvent) {
    e.preventDefault();
    const found: string[] = [];
    if (!le) found.push("Enter the legal entity's id (a UUID).");
    if (!isPeriod(period)) found.push("Enter the period as YYYY-MM.");
    if (!CURRENCY.test(currency)) found.push("Use a three-letter currency code.");
    const parsed = parseJournalCsv(csv);
    if (!parsed.ok) found.push(parsed.error);
    setErrors(found);
    setFailure(null);
    if (found.length > 0 || !parsed.ok) return;
    setBusy(true);
    try {
      setResult(await reconcile({ legalEntityId: le, period, currency, journalBalances: parsed.value }));
    } catch (err) {
      setResult(null);
      setFailure(describeError(err, "The reconciliation could not be run."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cost ledger reconciliation"
        description="Paste the period's journal balances by main account. Each primary element's mirrored cost must equal the journal with a 0.00 difference."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="recon-le" label="Legal entity">
            {(c) => <Input {...c} className={styles.mono} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} />}
          </FormField>
          <FormField id="recon-period" label="Period" hint="YYYY-MM">
            {(c) => <Input {...c} className={styles.mono} value={period} onChange={(e) => setPeriod(e.target.value)} />}
          </FormField>
          <FormField id="recon-currency" label="Currency">
            {(c) => <Input {...c} className={styles.mono} value={currency} onChange={(e) => setCurrency(e.target.value.toUpperCase())} />}
          </FormField>
          <div className={styles.wide}>
            <FormField id="recon-csv" label="Journal balances" hint="One line per main account: mainAccount,amount. A header row is skipped.">
              {(c) => <Textarea {...c} mono rows={8} value={csv} onChange={(e) => setCsv(e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Reconcile
            </Button>
          </div>
        </form>
      </Card>
      <ErrorList errors={errors} failure={failure} />
      {result ? (
        <div className={styles.stack}>
          <p role="status">
            <Badge tone={result.reconciled ? "success" : "danger"}>{result.reconciled ? "Reconciled" : "Differences found"}</Badge>
          </p>
          <DataTable
            label="Reconciliation by element"
            columns={lineColumns}
            rows={result.lines}
            getRowKey={(l) => l.costElementId}
            empty={{ title: "Nothing to compare", description: "No journal balance maps to a primary element and no primary cost is mirrored." }}
          />
          {result.unmappedAccounts.length > 0 ? (
            <DataTable label="Accounts outside the cost ledger" columns={unmappedColumns} rows={result.unmappedAccounts} getRowKey={(b) => b.mainAccount} />
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
