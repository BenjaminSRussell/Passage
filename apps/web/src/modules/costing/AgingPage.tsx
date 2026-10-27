import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { describeError, previewAging, type AgingBucket, type AgingPreviewRequest, type AgingPreviewResponse, type CostMethod, type Fallback } from "./api/client";
import styles from "./pages.module.css";
import { CURRENCY, FallbackField, MethodField, parseTransactions, SAMPLE_TRANSACTIONS, TransactionsField } from "./shared";

export const DEFAULT_BOUNDS = "30, 90, 180, 365";
export const MAX_BOUNDS = 12;
const DAY = /^\d{4}-\d{2}-\d{2}$/;

type Errors = Record<string, string>;

/** Parses "30, 90, 180" into strictly ascending whole days; empty means the default buckets. */
export function parseBounds(text: string): { ok: true; value?: number[] } | { ok: false; error: string } {
  const parts = text
    .split(",")
    .map((p) => p.trim())
    .filter((p) => p !== "");
  if (parts.length === 0) return { ok: true };
  if (parts.length > MAX_BOUNDS) return { ok: false, error: `Use at most ${MAX_BOUNDS} bounds.` };
  const value: number[] = [];
  for (const p of parts) {
    if (!/^\d+$/.test(p)) return { ok: false, error: "Bounds are whole days, such as 30, 90, 180." };
    const n = Number(p);
    if (value.length > 0 && n <= value[value.length - 1]) return { ok: false, error: "Each bound must be larger than the one before it." };
    value.push(n);
  }
  return { ok: true, value };
}

const bucketColumns = (currency: string): DataTableColumn<AgingBucket>[] => [
  { key: "label", header: "Age (days)" },
  { key: "qty", header: "Quantity", align: "end", mono: true },
  { key: "value", header: `Value (${currency})`, align: "end", mono: true },
];

/** Inventory aging (FR-20.17, BR-20.15): remaining cost bucketed by calendar days since receipt, per valuation type. */
export function AgingPage() {
  const [method, setMethod] = useState<CostMethod>("FIFO");
  const [currency, setCurrency] = useState("EUR");
  const [fallback, setFallback] = useState<Fallback>("RUNNING_AVG");
  const [standardUnitCost, setStandardUnitCost] = useState("");
  const [defaultType, setDefaultType] = useState("");
  const [asOf, setAsOf] = useState("2026-01-31");
  const [bounds, setBounds] = useState(DEFAULT_BOUNDS);
  const [text, setText] = useState(SAMPLE_TRANSACTIONS);
  const [errors, setErrors] = useState<Errors>({});
  const [result, setResult] = useState<AgingPreviewResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    const found: Errors = {};
    if (!CURRENCY.test(currency)) found.currency = "Use a three-letter ISO 4217 code, such as EUR.";
    if (!DAY.test(asOf)) found.asOf = "Enter the as-of date.";
    const parsedBounds = parseBounds(bounds);
    if (!parsedBounds.ok) found.bucketBounds = parsedBounds.error;
    const parsed = parseTransactions(text);
    if (!parsed.ok) found.transactions = parsed.error;
    setErrors(found);
    if (Object.keys(found).length > 0 || !parsed.ok || !parsedBounds.ok) {
      setResult(null);
      return;
    }
    const body: AgingPreviewRequest = { method, currency, fallback, asOf, transactions: parsed.value };
    if (parsedBounds.value) body.bucketBounds = parsedBounds.value;
    if (standardUnitCost.trim() !== "") body.standardUnitCost = standardUnitCost.trim();
    if (defaultType.trim() !== "") body.defaultValuationType = defaultType.trim();
    setBusy(true);
    try {
      setResult(await previewAging(body));
    } catch (err) {
      setResult(null);
      setError(describeError(err, "The aging preview failed."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Inventory aging"
        description="Bucket the remaining cost by days since each layer was received. Average methods age their FIFO layers repriced to the average, so the total ties to the remaining value."
      />
      <Card>
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <MethodField id="aging-method" value={method} onChange={setMethod} />
          <FormField id="aging-currency" label="Currency" error={errors.currency}>
            {(c) => <Input {...c} value={currency} maxLength={3} invalid={Boolean(errors.currency)} onChange={(e) => setCurrency(e.target.value.toUpperCase())} />}
          </FormField>
          <FormField id="aging-as-of" label="As of date" error={errors.asOf}>
            {(c) => <Input {...c} type="date" value={asOf} invalid={Boolean(errors.asOf)} onChange={(e) => setAsOf(e.target.value)} />}
          </FormField>
          <FormField id="aging-bounds" label="Bucket bounds" optional hint="Upper bound of each bucket in days. Empty uses 30, 90, 180, 365." error={errors.bucketBounds}>
            {(c) => <Input {...c} value={bounds} invalid={Boolean(errors.bucketBounds)} onChange={(e) => setBounds(e.target.value)} />}
          </FormField>
          <FallbackField id="aging-fallback" value={fallback} onChange={setFallback} />
          <FormField id="aging-standard" label="Standard unit cost" optional hint="Required for STANDARD and the STANDARD fallback.">
            {(c) => <Input {...c} value={standardUnitCost} inputMode="decimal" onChange={(e) => setStandardUnitCost(e.target.value)} />}
          </FormField>
          <FormField id="aging-default-type" label="Default valuation type" optional hint="Used for untyped receipts and issues of a split-valued product.">
            {(c) => <Input {...c} value={defaultType} maxLength={64} onChange={(e) => setDefaultType(e.target.value)} />}
          </FormField>
          <div className={styles.wide}>
            <TransactionsField id="aging-transactions" value={text} error={errors.transactions} onChange={setText} />
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run aging
            </Button>
          </div>
        </form>
      </Card>
      {error ? (
        <Alert tone="danger" title="The aging preview failed" live="alert">
          {error}
        </Alert>
      ) : null}
      {result ? (
        <>
          <StatGroup label="Aging totals">
            <StatTile label="Total quantity" value={result.totalQty} />
            <StatTile label="Total value" value={`${result.totalValue} ${result.currency}`} />
            <StatTile label="Remaining value" value={`${result.remainingValue} ${result.currency}`} />
          </StatGroup>
          {result.tiesToRemaining ? (
            <Alert tone="success">{`The buckets tie to the remaining value of ${result.remainingValue} ${result.currency}.`}</Alert>
          ) : (
            <Alert tone="warning">{`The buckets total ${result.totalValue} but the remaining value is ${result.remainingValue} ${result.currency}.`}</Alert>
          )}
          <Card title={`Aging as of ${result.asOf}`}>
            <DataTable label="Aging buckets" columns={bucketColumns(result.currency)} rows={result.buckets} getRowKey={(b) => b.label} />
          </Card>
          {(result.valuationTypes ?? []).map((t) => (
            <Card key={t.valuationType} title={`Valuation type ${t.valuationType}`}>
              <DataTable label={`Aging buckets for ${t.valuationType}`} columns={bucketColumns(result.currency)} rows={t.buckets} getRowKey={(b) => b.label} />
            </Card>
          ))}
        </>
      ) : (
        <Card>
          <EmptyState icon="calendarCheck" title="No aging run yet" description="Run the aging to see how much of your remaining stock value sits in each age bucket." />
        </Card>
      )}
    </div>
  );
}
