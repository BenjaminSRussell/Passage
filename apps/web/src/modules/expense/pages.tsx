import { useState, type FormEvent } from "react";
import { evaluateReport, ExpenseProblem, type EvaluationResponse } from "./api/client";

/** Check one expense line against the policy rules before it goes on a report. */
export function ExpenseHome() {
  const today = new Date().toISOString().slice(0, 10);
  const [category, setCategory] = useState("MEALS");
  const [amount, setAmount] = useState("80.00");
  const [hasReceipt, setHasReceipt] = useState(true);
  const [justification, setJustification] = useState("");
  const [result, setResult] = useState<EvaluationResponse | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      setResult(
        await evaluateReport(today, [
          {
            id: "draft-1",
            category,
            txnDate: today,
            amount,
            merchant: "",
            attendees: 0,
            hasReceipt,
            justification,
            paidBy: "EMPLOYEE",
          },
        ]),
      );
    } catch (err) {
      setResult(null);
      setError(err instanceof ExpenseProblem ? `${err.code}: ${err.message}` : "The line could not be checked.");
    }
  }

  return (
    <section aria-labelledby="expense-title">
      <h1 id="expense-title">Check an expense line</h1>
      <p>Policy outcomes are shown before you submit. A blocked line cannot be submitted.</p>
      <form onSubmit={onSubmit}>
        <label>
          Category
          <select value={category} onChange={(e) => setCategory(e.target.value)}>
            <option value="MEALS">Meals</option>
            <option value="TRAVEL">Travel</option>
            <option value="ALCOHOL">Alcohol</option>
          </select>
        </label>
        <label>
          Amount
          <input value={amount} onChange={(e) => setAmount(e.target.value)} inputMode="decimal" />
        </label>
        <label>
          <input type="checkbox" checked={hasReceipt} onChange={(e) => setHasReceipt(e.target.checked)} />
          Receipt attached
        </label>
        <label>
          Justification
          <input value={justification} onChange={(e) => setJustification(e.target.value)} />
        </label>
        <button type="submit">Check line</button>
      </form>
      {error && <p role="alert">{error}</p>}
      {result && (
        <div>
          <p>{result.submittable ? "Ready to submit" : "Cannot submit yet"}</p>
          <ul>
            {result.results.map((r) => (
              <li key={`${r.lineId}-${r.ruleId}`}>
                {r.ruleId}: {r.outcome} {r.satisfied ? "(satisfied)" : "(open)"} - {r.code} {r.message}
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}
