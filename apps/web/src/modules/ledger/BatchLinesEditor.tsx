import { Button, DataTable, Input, Select, type DataTableColumn } from "../../shell/ui";
import type { BatchLine, BatchLineInput } from "./api/client";
import styles from "./pages.module.css";

/** One editable line of a journal batch. Amounts stay strings; dimensions are typed as `DEPT=D100, CC=C200`. */
export interface LineDraft {
  key: string;
  account: string;
  dimensions: string;
  side: "D" | "C";
  amount: string;
  text: string;
}

let nextKey = 0;
function newLineKey(): string {
  nextKey += 1;
  return `line-${nextKey}`;
}

export function emptyLine(side: "D" | "C" = "D"): LineDraft {
  return { key: newLineKey(), account: "", dimensions: "", side, amount: "", text: "" };
}

/** Drafts for the lines a batch already has. */
export function linesToDrafts(lines: BatchLine[]): LineDraft[] {
  return lines.map((l) => ({
    key: newLineKey(),
    account: l.account,
    dimensions: Object.entries(l.dimensions ?? {})
      .map(([attr, value]) => `${attr}=${value}`)
      .join(", "),
    side: l.debitCredit,
    amount: l.amount,
    text: l.text ?? "",
  }));
}

const ACCOUNT = /^[A-Za-z0-9._-]{1,20}$/;
const ATTRIBUTE = /^[A-Z][A-Z0-9_]{0,19}$/;
const AMOUNT = /^\d{1,14}(\.\d{1,6})?$/;
const MICROS = 1_000_000n;

/** Parses `DEPT=D100, CC=C200`; returns null when malformed. */
export function parseDimensions(text: string): Record<string, string> | null {
  const out: Record<string, string> = {};
  for (const part of text.split(",").map((p) => p.trim()).filter(Boolean)) {
    const [attr, value, ...rest] = part.split("=").map((p) => p.trim());
    if (rest.length > 0 || !attr || !value || !ATTRIBUTE.test(attr) || value.length > 20 || attr in out) return null;
    out[attr] = value;
  }
  return out;
}

/** Exact decimal amount in millionths (the service's numeric(20,6)); never a float. */
export function toMicros(amount: string): bigint | null {
  const value = amount.trim();
  if (!AMOUNT.test(value)) return null;
  const [whole, fraction = ""] = value.split(".");
  return BigInt(whole) * MICROS + BigInt(fraction.padEnd(6, "0"));
}

/** Formats millionths at `scale` decimals (rounding is never needed: amounts carry at most the currency's scale). */
export function formatMicros(micros: bigint, scale = 2): string {
  const negative = micros < 0n;
  const abs = negative ? -micros : micros;
  const whole = abs / MICROS;
  const fraction = (abs % MICROS).toString().padStart(6, "0").slice(0, scale);
  return `${negative ? "-" : ""}${whole.toString()}${scale > 0 ? `.${fraction}` : ""}`;
}

/** Side totals of the drafts (lines with an unreadable amount count as zero). */
export function lineTotals(lines: LineDraft[]): { debit: bigint; credit: bigint } {
  let debit = 0n;
  let credit = 0n;
  for (const l of lines) {
    const micros = toMicros(l.amount) ?? 0n;
    if (l.side === "D") debit += micros;
    else credit += micros;
  }
  return { debit, credit };
}

/** Client checks that mirror the service's line shape checks; rule checks (balance, accounts) run on submit. */
export function validateLines(lines: LineDraft[]): Record<string, string> {
  const errors: Record<string, string> = {};
  if (lines.length === 0) errors.lines = "Add at least one line.";
  lines.forEach((l, i) => {
    const n = i + 1;
    if (!ACCOUNT.test(l.account.trim())) errors[`lines.${i}.account`] = `Line ${n}: enter an account of up to 20 letters, digits, dots, dashes or underscores.`;
    if (toMicros(l.amount) === null) errors[`lines.${i}.amount`] = `Line ${n}: enter a non-negative amount such as 125.00.`;
    if (parseDimensions(l.dimensions) === null) errors[`lines.${i}.dimensions`] = `Line ${n}: write dimensions as DEPT=D100, CC=C200.`;
    if (l.text.length > 255) errors[`lines.${i}.text`] = `Line ${n}: text is at most 255 characters.`;
  });
  return errors;
}

/** The wire form of valid drafts in the batch currency. */
export function toLineInputs(lines: LineDraft[], currency: string): BatchLineInput[] {
  return lines.map((l) => {
    const dimensions = parseDimensions(l.dimensions) ?? {};
    const input: BatchLineInput = { account: l.account.trim(), debitCredit: l.side, amount: { currency, value: l.amount.trim() } };
    if (Object.keys(dimensions).length > 0) input.dimensions = dimensions;
    if (l.text.trim() !== "") input.text = l.text.trim();
    return input;
  });
}

/** Maps the service's `lines[0].amount.value` field errors onto readable messages. */
export function serviceLineErrors(fields: Record<string, string>): string[] {
  return Object.entries(fields).map(([field, message]) => {
    const m = /^lines\[(\d+)\]\.(.+)$/.exec(field);
    return m ? `Line ${Number(m[1]) + 1} ${m[2]}: ${message}` : `${field}: ${message}`;
  });
}

export interface BatchLinesEditorProps {
  lines: LineDraft[];
  currency: string;
  onChange(lines: LineDraft[]): void;
  scale?: number;
}

/** Line grid of a journal batch with a running totals bar (§2.11.6 Journal batch detail). */
export function BatchLinesEditor({ lines, currency, onChange, scale = 2 }: BatchLinesEditorProps) {
  const update = (key: string, patch: Partial<LineDraft>) => onChange(lines.map((l) => (l.key === key ? { ...l, ...patch } : l)));
  const remove = (key: string) => onChange(lines.filter((l) => l.key !== key));
  const number = (l: LineDraft) => lines.indexOf(l) + 1;
  const totals = lineTotals(lines);
  const difference = totals.debit - totals.credit;

  const columns: DataTableColumn<LineDraft>[] = [
    { key: "number", header: "Line", align: "end", render: (l) => number(l) },
    {
      key: "account",
      header: "Account",
      render: (l) => (
        <Input aria-label={`Line ${number(l)} account`} className={styles.mono} value={l.account} maxLength={20} onChange={(e) => update(l.key, { account: e.target.value })} />
      ),
    },
    {
      key: "dimensions",
      header: "Dimensions",
      render: (l) => (
        <Input
          aria-label={`Line ${number(l)} dimensions`}
          className={styles.mono}
          placeholder="DEPT=D100"
          value={l.dimensions}
          onChange={(e) => update(l.key, { dimensions: e.target.value.toUpperCase() })}
        />
      ),
    },
    {
      key: "side",
      header: "D/C",
      render: (l) => (
        <Select aria-label={`Line ${number(l)} debit or credit`} value={l.side} onChange={(e) => update(l.key, { side: e.target.value === "C" ? "C" : "D" })}>
          <option value="D">Debit</option>
          <option value="C">Credit</option>
        </Select>
      ),
    },
    {
      key: "amount",
      header: `Amount (${currency})`,
      render: (l) => (
        <Input aria-label={`Line ${number(l)} amount`} className={styles.amount} inputMode="decimal" value={l.amount} onChange={(e) => update(l.key, { amount: e.target.value })} />
      ),
    },
    {
      key: "text",
      header: "Text",
      render: (l) => <Input aria-label={`Line ${number(l)} text`} value={l.text} maxLength={255} onChange={(e) => update(l.key, { text: e.target.value })} />,
    },
    {
      key: "remove",
      header: "Remove",
      render: (l) => (
        <Button size="sm" variant="ghost" onClick={() => remove(l.key)} disabled={lines.length === 1}>
          Remove line {number(l)}
        </Button>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <DataTable label="Batch lines" columns={columns} rows={lines} getRowKey={(l) => l.key} empty={{ title: "No lines", description: "Add a line to start the batch." }} />
      <div className={styles.formActions}>
        <Button size="sm" onClick={() => onChange([...lines, emptyLine(difference > 0n ? "C" : "D")])}>
          Add line
        </Button>
      </div>
      <dl className={styles.totals} aria-label="Batch totals">
        <div className={styles.entry}>
          <dt className={styles.term}>Total debit ({currency})</dt>
          <dd className={styles.value} data-testid="batch-debit">
            {formatMicros(totals.debit, scale)}
          </dd>
        </div>
        <div className={styles.entry}>
          <dt className={styles.term}>Total credit ({currency})</dt>
          <dd className={styles.value} data-testid="batch-credit">
            {formatMicros(totals.credit, scale)}
          </dd>
        </div>
        <div className={styles.entry}>
          <dt className={styles.term}>Difference</dt>
          <dd className={styles.value} data-testid="batch-difference">
            {difference === 0n ? "Balanced" : formatMicros(difference, scale)}
          </dd>
        </div>
      </dl>
    </div>
  );
}
