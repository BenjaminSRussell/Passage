import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { describeError, previewSettlements, type CostMethod, type Fallback, type SettlePreviewRequest, type SettlePreviewResponse, type SettlementRow } from "./api/client";
import styles from "./pages.module.css";
import { FallbackField, MethodField, parseTransactions, SAMPLE_TRANSACTIONS, TransactionsField } from "./shared";

export { COST_METHODS, FALLBACKS, parseTransactions } from "./shared";

interface Row {
  key: string;
  value: string;
}

type Exception = SettlePreviewResponse["exceptions"][number];

const issueColumns = (currency: string): DataTableColumn<Row>[] => [
  { key: "key", header: "Issue", mono: true },
  { key: "value", header: `Cost (${currency})`, align: "end", mono: true },
];

const settlementColumns: DataTableColumn<SettlementRow>[] = [
  { key: "seq", header: "Seq", align: "end" },
  { key: "issueId", header: "Issue", mono: true },
  { key: "receiptId", header: "Receipt or pool", mono: true, render: (s) => s.receiptId || s.pool || "" },
  { key: "qty", header: "Qty", align: "end", mono: true },
  { key: "value", header: "Value", align: "end", mono: true },
];

const exceptionColumns: DataTableColumn<Exception>[] = [
  { key: "issueId", header: "Issue", mono: true },
  { key: "qty", header: "Qty", align: "end", mono: true },
  { key: "value", header: "Value", align: "end", mono: true },
  { key: "fallback", header: "Fallback" },
];

const typeColumns = (currency: string): DataTableColumn<Row>[] => [
  { key: "key", header: "Valuation type", mono: true },
  { key: "value", header: `Remaining value (${currency})`, align: "end", mono: true },
];

const toRows = (m: Record<string, string> = {}): Row[] =>
  Object.entries(m)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, value]) => ({ key, value }));

/** Settlement preview: values issues under a costing method without posting (FR-20.1 to FR-20.3, FR-20.8, FR-20.16). */
export function SettlementPreview() {
  const [method, setMethod] = useState<CostMethod>("FIFO");
  const [currency, setCurrency] = useState("EUR");
  const [fallback, setFallback] = useState<Fallback>("RUNNING_AVG");
  const [standardUnitCost, setStandardUnitCost] = useState("");
  const [defaultType, setDefaultType] = useState("");
  const [text, setText] = useState(SAMPLE_TRANSACTIONS);
  const [result, setResult] = useState<SettlePreviewResponse | null>(null);
  const [jsonError, setJsonError] = useState<string | undefined>();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    const parsed = parseTransactions(text);
    if (!parsed.ok) {
      setJsonError(parsed.error);
      setResult(null);
      return;
    }
    setJsonError(undefined);
    const body: SettlePreviewRequest = { method, currency, fallback, transactions: parsed.value };
    if (standardUnitCost.trim() !== "") {
      body.standardUnitCost = standardUnitCost.trim();
    }
    if (defaultType.trim() !== "") {
      body.defaultValuationType = defaultType.trim();
    }
    setBusy(true);
    try {
      setResult(await previewSettlements(body));
    } catch (err) {
      setResult(null);
      setError(describeError(err, "The preview failed."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Settlement preview"
        description="Value issues against receipts under a costing method, with marking and split valuation types, without posting anything."
      />
      <Card>
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <MethodField id="settle-method" value={method} onChange={setMethod} />
          <FormField id="settle-currency" label="Currency">
            {(c) => <Input {...c} value={currency} maxLength={3} onChange={(e) => setCurrency(e.target.value.toUpperCase())} />}
          </FormField>
          <FallbackField id="settle-fallback" value={fallback} onChange={setFallback} />
          <FormField id="settle-standard" label="Standard unit cost" optional hint="Required for STANDARD and the STANDARD fallback.">
            {(c) => <Input {...c} value={standardUnitCost} inputMode="decimal" onChange={(e) => setStandardUnitCost(e.target.value)} />}
          </FormField>
          <FormField id="settle-default-type" label="Default valuation type" optional hint="Used for untyped receipts and issues of a split-valued product.">
            {(c) => <Input {...c} value={defaultType} maxLength={64} onChange={(e) => setDefaultType(e.target.value)} />}
          </FormField>
          <div className={styles.wide}>
            <TransactionsField id="settle-transactions" value={text} error={jsonError} onChange={setText} />
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Preview settlement
            </Button>
          </div>
        </form>
      </Card>
      {error ? (
        <Alert tone="danger" title="The preview failed" live="alert">
          {error}
        </Alert>
      ) : null}
      {result ? (
        <>
          <StatGroup label="Settlement totals">
            <StatTile label="Remaining quantity" value={result.remainingQty} />
            <StatTile label="Remaining value" value={`${result.remainingValue} ${result.currency}`} />
            <StatTile label="Exceptions" value={result.exceptions.length} tone="warning" />
          </StatGroup>
          <Card title="Issue costs">
            <DataTable
              label="Issue costs"
              columns={issueColumns(result.currency)}
              rows={toRows(result.issueCosts)}
              getRowKey={(r) => r.key}
              empty={{ title: "No issues to cost", description: "Add an ISSUE transaction to see its cost." }}
            />
          </Card>
          <Card title="Settlements">
            <DataTable
              label="Settlements"
              columns={settlementColumns}
              rows={result.settlements}
              getRowKey={(s) => `${s.seq}-${s.issueId}`}
              empty={{ title: "No settlements", description: "No receipt or pool settled an issue in this run." }}
            />
          </Card>
          {result.remainingByValuationType ? (
            <Card title="Remaining value by valuation type">
              <DataTable label="Remaining value by valuation type" columns={typeColumns(result.currency)} rows={toRows(result.remainingByValuationType)} getRowKey={(r) => r.key} />
            </Card>
          ) : null}
          {result.exceptions.length > 0 ? (
            <Card title="Exceptions">
              <div className={styles.stack}>
                <p className={styles.note}>Quantity no receipt covered, valued at the fallback until a later receipt settles it.</p>
                <DataTable label="Exceptions" columns={exceptionColumns} rows={result.exceptions} getRowKey={(x) => x.issueId} />
              </div>
            </Card>
          ) : null}
        </>
      ) : (
        <Card>
          <EmptyState icon="box" title="No preview run yet" description="Run a preview to see what each issue costs and which receipts it settled against." />
        </Card>
      )}
    </div>
  );
}

export function CostingHome() {
  return <SettlementPreview />;
}
