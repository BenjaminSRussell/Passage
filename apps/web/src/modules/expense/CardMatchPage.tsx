import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, StatGroup, StatTile, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeProblem, matchCards, type CardMatchInput, type CardMatchPair, type CardMatchResult, type MatchItem, type Problem } from "./api/client";
import { DAY, DECIMAL, splitProblem } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

export interface CardMatchValues {
  cards: string;
  receipts: string;
  tolerancePct: string;
  toleranceDays: string;
}

export const emptyCardMatch: CardMatchValues = { cards: "", receipts: "", tolerancePct: "1", toleranceDays: "2" };

/** The service's batch limit per list. */
export const MAX_MATCH_ITEMS = 1000;

/**
 * Reads one item per line as "id, worker, date, amount, merchant". The merchant is
 * the rest of the line, so it may hold commas. Returns the items, or the first
 * problem as a message naming its line.
 */
export function parseMatchItems(raw: string, noun: string): { items: MatchItem[]; error?: string } {
  const items: MatchItem[] = [];
  const seen = new Set<string>();
  const lines = raw.split(/\r?\n/);
  for (let n = 0; n < lines.length; n++) {
    const line = lines[n].trim();
    if (line === "") continue;
    const parts = line.split(",").map((p) => p.trim());
    const at = `Line ${n + 1}`;
    if (parts.length < 5) return { items, error: `${at}: enter id, worker, date, amount and merchant separated by commas.` };
    const [id, workerId, date, amount] = parts;
    const merchant = parts.slice(4).join(", ");
    if (id === "" || id.length > 64 || seen.has(id)) return { items, error: `${at}: the ${noun} id must be unique and up to 64 characters.` };
    if (workerId === "" || workerId.length > 64) return { items, error: `${at}: enter the worker, up to 64 characters.` };
    if (!DAY.test(date)) return { items, error: `${at}: enter the date as YYYY-MM-DD.` };
    if (!DECIMAL.test(amount)) return { items, error: `${at}: enter the amount as a number such as 100.00.` };
    if (merchant === "" || merchant.length > 200) return { items, error: `${at}: enter the merchant, up to 200 characters.` };
    seen.add(id);
    items.push({ id, workerId, date, merchant, amount });
  }
  if (items.length > MAX_MATCH_ITEMS) return { items, error: `Enter at most ${MAX_MATCH_ITEMS} ${noun}s.` };
  return { items };
}

export function validateCardMatch(v: CardMatchValues): { errors: Errors; input?: CardMatchInput } {
  const errors: Errors = {};
  const cards = parseMatchItems(v.cards, "card transaction");
  const receipts = parseMatchItems(v.receipts, "receipt");
  if (cards.error) errors.cards = cards.error;
  else if (cards.items.length === 0) errors.cards = "Enter at least one card transaction.";
  if (receipts.error) errors.receipts = receipts.error;
  const pct = Number(v.tolerancePct);
  if (!DECIMAL.test(v.tolerancePct.trim()) || pct > 10) errors.tolerancePct = "Enter a percentage from 0 to 10.";
  const days = Number(v.toleranceDays);
  if (!/^\d+$/.test(v.toleranceDays.trim()) || days > 10) errors.toleranceDays = "Enter 0 to 10 days.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { cards: cards.items, receipts: receipts.items, tolerancePct: v.tolerancePct.trim(), toleranceDays: days } };
}

const SHOWN = ["cards", "receipts", "tolerancePct", "toleranceDays"];

const matchColumns: DataTableColumn<CardMatchPair>[] = [
  { key: "cardId", header: "Card transaction", mono: true },
  { key: "receiptId", header: "Receipt", mono: true },
];

const unmatchedColumns: DataTableColumn<{ kind: string; id: string }>[] = [
  { key: "kind", header: "Kind" },
  { key: "id", header: "Id", mono: true },
];

/** FR-43.13 card matching: pair card transactions with receipts one to one within the tolerance, never across workers. Stores nothing. */
export function CardMatchPage() {
  const [values, setValues] = useState<CardMatchValues>(emptyCardMatch);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Pick<Problem, "code" | "message"> | null>(null);
  const [result, setResult] = useState<CardMatchResult | null>(null);
  const [busy, setBusy] = useState(false);

  function update(patch: Partial<CardMatchValues>) {
    setValues((prev) => ({ ...prev, ...patch }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateCardMatch(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      setResult(await matchCards(input));
    } catch (error) {
      const split = splitProblem(describeProblem(error), SHOWN);
      setResult(null);
      setErrors(split.fields);
      setFailure(split.failure);
    } finally {
      setBusy(false);
    }
  }

  const unmatched = result
    ? [...result.unmatchedCards.map((id) => ({ kind: "Card transaction", id })), ...result.unmatchedReceipts.map((id) => ({ kind: "Receipt", id }))]
    : [];
  const ratePct = result ? `${(Number(result.autoMatchRate) * 100).toFixed(2)}%` : "";

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Card matching"
        description="Pair card transactions with receipts. A receipt matches a transaction of the same worker and merchant within the amount and date tolerance; each is used once. Unmatched transactions stay in the worker's unassigned pool. Nothing is saved."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <div className={styles.wide}>
            <FormField id="match-cards" label="Card transactions" hint="One per line: id, worker, date, amount, merchant." error={errors.cards}>
              {(c) => <Textarea {...c} mono rows={6} value={values.cards} onChange={(e) => update({ cards: e.target.value })} invalid={Boolean(errors.cards)} />}
            </FormField>
          </div>
          <div className={styles.wide}>
            <FormField id="match-receipts" label="Receipts" optional hint="One per line: id, worker, date, amount, merchant." error={errors.receipts}>
              {(c) => <Textarea {...c} mono rows={6} value={values.receipts} onChange={(e) => update({ receipts: e.target.value })} invalid={Boolean(errors.receipts)} />}
            </FormField>
          </div>
          <FormField id="match-pct" label="Amount tolerance (%)" error={errors.tolerancePct}>
            {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={values.tolerancePct} onChange={(e) => update({ tolerancePct: e.target.value })} invalid={Boolean(errors.tolerancePct)} />}
          </FormField>
          <FormField id="match-days" label="Date tolerance (days)" error={errors.toleranceDays}>
            {(c) => <Input {...c} inputMode="numeric" value={values.toleranceDays} onChange={(e) => update({ toleranceDays: e.target.value })} invalid={Boolean(errors.toleranceDays)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not match the transactions" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Match
            </Button>
          </div>
        </form>
      </Card>
      <Card title="Result">
        {result ? (
          <div className={styles.stack}>
            <StatGroup label="Match summary">
              <StatTile label="Matched" value={result.matches.length} />
              <StatTile label="Unmatched transactions" value={result.unmatchedCards.length} tone="warning" />
              <StatTile label="Unused receipts" value={result.unmatchedReceipts.length} />
              <StatTile label="Auto-match rate" value={ratePct} />
            </StatGroup>
            <DataTable
              label="Matches"
              columns={matchColumns}
              rows={result.matches}
              getRowKey={(m) => m.cardId}
              empty={{ title: "No transaction matched a receipt", description: "Check the merchants and dates, or widen the tolerance." }}
            />
            <DataTable
              label="Unmatched"
              columns={unmatchedColumns}
              rows={unmatched}
              getRowKey={(u) => `${u.kind}-${u.id}`}
              empty={{ title: "Every transaction and receipt was matched" }}
            />
          </div>
        ) : busy ? (
          <DataTable label="Matches" columns={matchColumns} rows={[]} getRowKey={(m) => m.cardId} loading />
        ) : (
          <EmptyState icon="receipt" title="Nothing matched yet" description="Enter the card transactions and receipts, then match." compact />
        )}
      </Card>
    </div>
  );
}
