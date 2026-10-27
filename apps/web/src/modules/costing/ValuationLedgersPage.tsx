import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { describeError, previewValuationLedgers, type CostMethod, type LedgerResult, type LedgersPreviewResponse, type LedgerSpec } from "./api/client";
import styles from "./pages.module.css";
import { COST_METHODS, CURRENCY, DECIMAL, parseTransactions, SAMPLE_TRANSACTIONS, TransactionsField } from "./shared";

export const MAX_LEDGERS = 8;

interface LedgerDraft {
  id: string;
  method: CostMethod;
  currency: string;
  rate: string;
  primary: boolean;
  standardUnitCost: string;
}

type Errors = Record<string, string>;

/** AC-20.11: IFRS primary in EUR on the periodic average, a group ledger in USD at 1.10 on FIFO. */
const SAMPLE_LEDGERS: LedgerDraft[] = [
  { id: "IFRS", method: "WEIGHTED_AVG_PERIODIC", currency: "EUR", rate: "1", primary: true, standardUnitCost: "" },
  { id: "GROUP", method: "FIFO", currency: "USD", rate: "1.10", primary: false, standardUnitCost: "" },
];

const blankLedger: LedgerDraft = { id: "", method: "FIFO", currency: "", rate: "", primary: false, standardUnitCost: "" };

/** Client checks that mirror the preview validation and BR-20.13's one-primary rule. */
export function validateLedgers(ledgers: LedgerDraft[]): Errors {
  const errors: Errors = {};
  const seen = new Set<string>();
  ledgers.forEach((l, i) => {
    const id = l.id.trim();
    if (id === "") errors[`ledgers[${i}].id`] = "Enter a ledger id.";
    else if (seen.has(id)) errors[`ledgers[${i}].id`] = "Each ledger needs its own id.";
    seen.add(id);
    if (!CURRENCY.test(l.currency)) errors[`ledgers[${i}].currency`] = "Use a three-letter ISO 4217 code, such as USD.";
    if (!DECIMAL.test(l.rate.trim()) || Number(l.rate) <= 0) errors[`ledgers[${i}].rate`] = "Enter a rate above 0.";
    else if (l.primary && Number(l.rate) !== 1) errors[`ledgers[${i}].rate`] = "The primary ledger's rate is 1.";
    if (l.method === "STANDARD" && !(DECIMAL.test(l.standardUnitCost.trim()) && Number(l.standardUnitCost) > 0)) {
      errors[`ledgers[${i}].standardUnitCost`] = "STANDARD needs a standard unit cost above 0.";
    }
  });
  const primaries = ledgers.filter((l) => l.primary).length;
  if (primaries !== 1) errors.primary = `Mark exactly one ledger as primary; ${primaries} ${primaries === 1 ? "is" : "are"} marked.`;
  return errors;
}

function toSpec(l: LedgerDraft): LedgerSpec {
  const spec: LedgerSpec = { id: l.id.trim(), method: l.method, currency: l.currency, rate: l.rate.trim(), primary: l.primary };
  if (l.standardUnitCost.trim() !== "") spec.standardUnitCost = l.standardUnitCost.trim();
  return spec;
}

const ledgerColumns: DataTableColumn<LedgerResult>[] = [
  { key: "id", header: "Ledger", mono: true },
  { key: "primary", header: "Role", render: (l) => (l.primary ? <Badge tone="info">Primary</Badge> : <Badge tone="neutral">Parallel</Badge>) },
  { key: "currency", header: "Currency", mono: true },
  { key: "remainingQty", header: "Remaining qty", align: "end", mono: true },
  { key: "remainingValue", header: "Remaining value", align: "end", mono: true },
  { key: "settlements", header: "Settlements", align: "end" },
  { key: "exceptions", header: "Exceptions", align: "end" },
];

interface IssueRow {
  issueId: string;
  costs: Record<string, string>;
}

function issueRows(ledgers: LedgerResult[]): IssueRow[] {
  const ids = new Set<string>();
  ledgers.forEach((l) => Object.keys(l.issueCosts).forEach((id) => ids.add(id)));
  return [...ids].sort().map((issueId) => ({ issueId, costs: Object.fromEntries(ledgers.map((l) => [l.id, l.issueCosts[issueId] ?? "—"])) }));
}

function issueColumns(ledgers: LedgerResult[]): DataTableColumn<IssueRow>[] {
  return [
    { key: "issueId", header: "Issue", mono: true },
    ...ledgers.map((l) => ({ key: l.id, header: `${l.id} (${l.currency})`, align: "end" as const, mono: true, render: (r: IssueRow) => r.costs[l.id] })),
  ];
}

/** Parallel valuation ledgers (FR-20.15, BR-20.13): one transaction sequence settled under each ledger's method and currency. */
export function ValuationLedgersPage() {
  const [ledgers, setLedgers] = useState<LedgerDraft[]>(SAMPLE_LEDGERS);
  const [text, setText] = useState(SAMPLE_TRANSACTIONS);
  const [errors, setErrors] = useState<Errors>({});
  const [result, setResult] = useState<LedgersPreviewResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const setLedger = (i: number, patch: Partial<LedgerDraft>) => setLedgers((prev) => prev.map((l, j) => (j === i ? { ...l, ...patch } : l)));

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    const found = validateLedgers(ledgers);
    const parsed = parseTransactions(text);
    if (!parsed.ok) found.transactions = parsed.error;
    setErrors(found);
    if (Object.keys(found).length > 0 || !parsed.ok) {
      setResult(null);
      return;
    }
    setBusy(true);
    try {
      setResult(await previewValuationLedgers({ ledgers: ledgers.map(toSpec), transactions: parsed.value }));
    } catch (err) {
      setResult(null);
      setError(describeError(err, "The ledger preview failed."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Valuation ledgers"
        description="Settle the same receipts and issues under each valuation ledger's own costing method and currency. Ledgers share quantities and order, not values. Nothing is posted."
      />
      <Card>
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          {ledgers.map((l, i) => (
            <fieldset key={i} className={styles.lineSet}>
              <legend className={styles.legend}>{`Ledger ${i + 1}`}</legend>
              <FormField id={`ledger-id-${i}`} label="Ledger id" error={errors[`ledgers[${i}].id`]}>
                {(c) => <Input {...c} value={l.id} invalid={Boolean(errors[`ledgers[${i}].id`])} onChange={(e) => setLedger(i, { id: e.target.value })} />}
              </FormField>
              <FormField id={`ledger-method-${i}`} label="Costing method">
                {(c) => (
                  <Select {...c} value={l.method} onChange={(e) => setLedger(i, { method: e.target.value as CostMethod })}>
                    {COST_METHODS.map((m) => (
                      <option key={m} value={m}>
                        {m}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id={`ledger-currency-${i}`} label="Currency" error={errors[`ledgers[${i}].currency`]}>
                {(c) => (
                  <Input
                    {...c}
                    value={l.currency}
                    maxLength={3}
                    invalid={Boolean(errors[`ledgers[${i}].currency`])}
                    onChange={(e) => setLedger(i, { currency: e.target.value.toUpperCase() })}
                  />
                )}
              </FormField>
              <FormField id={`ledger-rate-${i}`} label="Rate" hint="Ledger currency per unit of the primary currency." error={errors[`ledgers[${i}].rate`]}>
                {(c) => <Input {...c} inputMode="decimal" value={l.rate} invalid={Boolean(errors[`ledgers[${i}].rate`])} onChange={(e) => setLedger(i, { rate: e.target.value })} />}
              </FormField>
              <FormField id={`ledger-standard-${i}`} label="Standard unit cost" optional error={errors[`ledgers[${i}].standardUnitCost`]}>
                {(c) => (
                  <Input
                    {...c}
                    inputMode="decimal"
                    value={l.standardUnitCost}
                    invalid={Boolean(errors[`ledgers[${i}].standardUnitCost`])}
                    onChange={(e) => setLedger(i, { standardUnitCost: e.target.value })}
                  />
                )}
              </FormField>
              <span className={styles.check}>
                <Input id={`ledger-primary-${i}`} type="checkbox" checked={l.primary} onChange={(e) => setLedger(i, { primary: e.target.checked })} />
                <label htmlFor={`ledger-primary-${i}`}>{`Primary ledger ${i + 1}`}</label>
              </span>
              {ledgers.length > 1 ? (
                <Button variant="ghost" size="sm" onClick={() => setLedgers((prev) => prev.filter((_, j) => j !== i))}>
                  {`Remove ledger ${i + 1}`}
                </Button>
              ) : null}
            </fieldset>
          ))}
          {errors.primary ? (
            <div className={styles.wide}>
              <Alert tone="danger" live="alert">
                {errors.primary}
              </Alert>
            </div>
          ) : null}
          <div className={styles.wide}>
            <TransactionsField id="ledger-transactions" value={text} error={errors.transactions} onChange={setText} />
          </div>
          <div className={styles.formActions}>
            <Button variant="secondary" disabled={ledgers.length >= MAX_LEDGERS} onClick={() => setLedgers((prev) => [...prev, blankLedger])}>
              Add ledger
            </Button>
            <Button type="submit" variant="primary" loading={busy}>
              Preview ledgers
            </Button>
          </div>
        </form>
      </Card>
      {error ? (
        <Alert tone="danger" title="The ledger preview failed" live="alert">
          {error}
        </Alert>
      ) : null}
      {result ? (
        <>
          <Card title="Ledger results">
            <DataTable label="Ledger results" columns={ledgerColumns} rows={result.ledgers} getRowKey={(l) => l.id} />
          </Card>
          <Card title="Issue cost by ledger">
            <DataTable
              label="Issue cost by ledger"
              columns={issueColumns(result.ledgers)}
              rows={issueRows(result.ledgers)}
              getRowKey={(r) => r.issueId}
              empty={{ title: "No issues to cost", description: "Add an ISSUE transaction to compare its cost across ledgers." }}
            />
          </Card>
        </>
      ) : (
        <Card>
          <EmptyState icon="database" title="No ledger preview yet" description="Run a preview to compare issue costs and remaining value across your valuation ledgers." />
        </Card>
      )}
    </div>
  );
}
