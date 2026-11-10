import type { CostMethod, Fallback, SettleTransaction } from "./api/client";
import { FormField, Select, Textarea } from "../../shell/ui";

export const COST_METHODS: CostMethod[] = [
  "FIFO",
  "LIFO",
  "LIFO_DATE",
  "WEIGHTED_AVG_RUNNING",
  "WEIGHTED_AVG_PERIODIC",
  "MOVING_AVG",
  "STANDARD",
];

export const FALLBACKS: Fallback[] = ["RUNNING_AVG", "STANDARD", "LAST_RECEIPT"];

export const CURRENCY = /^[A-Z]{3}$/;
export const DECIMAL = /^-?\d+(\.\d+)?$/;

/** The FR-20.1 example: receipts 100 @ 10.00 and 100 @ 12.00, then an issue of 150. */
export const SAMPLE_TRANSACTIONS = JSON.stringify(
  [
    { id: "rcpt-1", kind: "RECEIPT", qty: "100", value: "1000.00", financialDate: "2026-01-05" },
    { id: "rcpt-2", kind: "RECEIPT", qty: "100", value: "1200.00", financialDate: "2026-01-20" },
    { id: "iss-1", kind: "ISSUE", qty: "150", financialDate: "2026-01-25" },
  ],
  null,
  2,
);

/** Parses the transactions textarea; returns a message instead of throwing. */
export function parseTransactions(text: string): { ok: true; value: SettleTransaction[] } | { ok: false; error: string } {
  try {
    const parsed: unknown = JSON.parse(text);
    if (!Array.isArray(parsed) || parsed.length === 0) {
      return { ok: false, error: "Transactions must be a non-empty JSON array." };
    }
    return { ok: true, value: parsed as SettleTransaction[] };
  } catch {
    return { ok: false, error: "Transactions are not valid JSON." };
  }
}

export function MethodField({ id, value, onChange }: { id: string; value: CostMethod; onChange(m: CostMethod): void }) {
  return (
    <FormField id={id} label="Costing method">
      {(c) => (
        <Select {...c} value={value} onChange={(e) => onChange(e.target.value as CostMethod)}>
          {COST_METHODS.map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
        </Select>
      )}
    </FormField>
  );
}

export function FallbackField({ id, value, onChange }: { id: string; value: Fallback; onChange(f: Fallback): void }) {
  return (
    <FormField id={id} label="Unsettled issue fallback" hint="How to value an issued quantity that no receipt covers.">
      {(c) => (
        <Select {...c} value={value} onChange={(e) => onChange(e.target.value as Fallback)}>
          {FALLBACKS.map((f) => (
            <option key={f} value={f}>
              {f}
            </option>
          ))}
        </Select>
      )}
    </FormField>
  );
}

export function TransactionsField({ id, value, error, onChange }: { id: string; value: string; error?: string; onChange(v: string): void }) {
  return (
    <FormField
      id={id}
      label="Transactions (JSON)"
      hint="An array of receipts and issues: id, kind, qty, value (receipts), financialDate, and optionally valuationType and markedReceiptId."
      error={error}
    >
      {(c) => <Textarea {...c} mono rows={12} value={value} invalid={Boolean(error)} onChange={(e) => onChange(e.target.value)} />}
    </FormField>
  );
}
