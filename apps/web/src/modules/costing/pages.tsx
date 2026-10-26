import { useState, type FormEvent } from "react";
import {
  CostingApiError,
  previewSettlements,
  type CostMethod,
  type Fallback,
  type SettlePreviewRequest,
  type SettlePreviewResponse,
  type SettleTransaction,
} from "./api/client";

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

const SAMPLE = JSON.stringify(
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

/** Settlement preview: values issues under a costing method without posting. */
export function SettlementPreview() {
  const [method, setMethod] = useState<CostMethod>("FIFO");
  const [currency, setCurrency] = useState("EUR");
  const [fallback, setFallback] = useState<Fallback>("RUNNING_AVG");
  const [standardUnitCost, setStandardUnitCost] = useState("");
  const [text, setText] = useState(SAMPLE);
  const [result, setResult] = useState<SettlePreviewResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    const parsed = parseTransactions(text);
    if (!parsed.ok) {
      setError(parsed.error);
      return;
    }
    const body: SettlePreviewRequest = { method, currency, fallback, transactions: parsed.value };
    if (standardUnitCost.trim() !== "") {
      body.standardUnitCost = standardUnitCost.trim();
    }
    setBusy(true);
    try {
      setResult(await previewSettlements(body));
    } catch (err) {
      setResult(null);
      setError(err instanceof CostingApiError ? `${err.problem.code}: ${err.message}` : "The preview failed.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <section aria-labelledby="costing-preview-heading">
      <h1 id="costing-preview-heading">Settlement preview</h1>
      <form onSubmit={onSubmit}>
        <label>
          Costing method
          <select value={method} onChange={(e) => setMethod(e.target.value as CostMethod)}>
            {COST_METHODS.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
        </label>
        <label>
          Currency
          <input value={currency} maxLength={3} onChange={(e) => setCurrency(e.target.value.toUpperCase())} />
        </label>
        <label>
          Unsettled issue fallback
          <select value={fallback} onChange={(e) => setFallback(e.target.value as Fallback)}>
            {FALLBACKS.map((f) => (
              <option key={f} value={f}>
                {f}
              </option>
            ))}
          </select>
        </label>
        <label>
          Standard unit cost
          <input
            value={standardUnitCost}
            inputMode="decimal"
            placeholder="Required for STANDARD"
            onChange={(e) => setStandardUnitCost(e.target.value)}
          />
        </label>
        <label>
          Transactions (JSON)
          <textarea rows={12} value={text} onChange={(e) => setText(e.target.value)} />
        </label>
        <button type="submit" disabled={busy}>
          Preview settlement
        </button>
      </form>
      {error && <p role="alert">{error}</p>}
      {result && (
        <div>
          <h2>Issue costs</h2>
          <table>
            <thead>
              <tr>
                <th>Issue</th>
                <th>Cost ({result.currency})</th>
              </tr>
            </thead>
            <tbody>
              {Object.entries(result.issueCosts)
                .sort(([a], [b]) => a.localeCompare(b))
                .map(([issueId, cost]) => (
                  <tr key={issueId}>
                    <td>{issueId}</td>
                    <td>{cost}</td>
                  </tr>
                ))}
            </tbody>
          </table>
          <h2>Settlements</h2>
          <table>
            <thead>
              <tr>
                <th>Seq</th>
                <th>Issue</th>
                <th>Receipt</th>
                <th>Qty</th>
                <th>Value</th>
              </tr>
            </thead>
            <tbody>
              {result.settlements.map((s) => (
                <tr key={`${s.seq}-${s.issueId}-${s.receiptId ?? ""}`}>
                  <td>{s.seq}</td>
                  <td>{s.issueId}</td>
                  <td>{s.receiptId ?? s.pool ?? ""}</td>
                  <td>{s.qty}</td>
                  <td>{s.value}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <p>
            Remaining quantity {result.remainingQty}, remaining value {result.remainingValue} {result.currency}.
          </p>
          {result.exceptions.length > 0 && (
            <>
              <h2>Exceptions</h2>
              <p>Quantity no receipt covered, valued at the fallback until a later receipt settles it.</p>
              <table>
                <thead>
                  <tr>
                    <th>Issue</th>
                    <th>Qty</th>
                    <th>Value</th>
                    <th>Fallback</th>
                  </tr>
                </thead>
                <tbody>
                  {result.exceptions.map((x) => (
                    <tr key={x.issueId}>
                      <td>{x.issueId}</td>
                      <td>{x.qty}</td>
                      <td>{x.value}</td>
                      <td>{x.fallback}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </>
          )}
        </div>
      )}
    </section>
  );
}

export function CostingHome() {
  return <SettlementPreview />;
}
