import { useState, type FormEvent } from "react";
import { calculatePayroll, PayrollProblem, type FilingStatus, type Statement } from "./api/client";

const filingStatuses: { value: FilingStatus; label: string }[] = [
  { value: "SINGLE", label: "Single" },
  { value: "MARRIED_FILING_JOINTLY", label: "Married filing jointly" },
  { value: "MARRIED_FILING_SEPARATELY", label: "Married filing separately" },
  { value: "HEAD_OF_HOUSEHOLD", label: "Head of household" },
];

/** Preview a gross-to-net statement for one worker (services/payroll). */
export function PayrollHome() {
  const [gross, setGross] = useState("4000.00");
  const [preTax, setPreTax] = useState("240.00");
  const [filingStatus, setFilingStatus] = useState<FilingStatus>("SINGLE");
  const [multipleJobs, setMultipleJobs] = useState(false);
  const [statement, setStatement] = useState<Statement | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      setStatement(
        await calculatePayroll({
          runType: "REGULAR",
          country: "US",
          taxYear: 2025,
          periodsPerYear: 26,
          w4: { filingStatus, multipleJobs },
          state: "CO",
          earnings: [{ code: "REGULAR", amount: gross }],
          preTax: [{ code: "401K", amount: preTax }],
          postTax: [],
          ytd: { ficaWages: "", futaWages: "", supplementalWages: "" },
        }),
      );
    } catch (err) {
      setStatement(null);
      setError(err instanceof PayrollProblem ? `${err.code}: ${err.message}` : "The preview could not be calculated.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <section aria-labelledby="payroll-title">
      <h1 id="payroll-title">Pay statement preview</h1>
      <p>Calculate gross to net for one biweekly worker under the US 2025 rule set. Nothing is posted or paid.</p>
      <form onSubmit={onSubmit}>
        <label>
          Regular gross
          <input value={gross} onChange={(e) => setGross(e.target.value)} inputMode="decimal" />
        </label>
        <label>
          W-4 filing status
          <select value={filingStatus} onChange={(e) => setFilingStatus(e.target.value as FilingStatus)}>
            {filingStatuses.map((f) => (
              <option key={f.value} value={f.value}>
                {f.label}
              </option>
            ))}
          </select>
        </label>
        <label>
          <input type="checkbox" checked={multipleJobs} onChange={(e) => setMultipleJobs(e.target.checked)} />
          W-4 Step 2 checkbox (multiple jobs)
        </label>
        <label>
          401(k) pre-tax
          <input value={preTax} onChange={(e) => setPreTax(e.target.value)} inputMode="decimal" />
        </label>
        <button type="submit" disabled={busy}>
          Calculate
        </button>
      </form>
      {error && <p role="alert">{error}</p>}
      {statement && (
        <div>
          <p>
            Net pay <strong>{statement.net}</strong> (rule set {statement.ruleSet})
          </p>
          <table>
            <thead>
              <tr>
                <th scope="col">Code</th>
                <th scope="col">Kind</th>
                <th scope="col">Amount</th>
                <th scope="col">Basis</th>
              </tr>
            </thead>
            <tbody>
              {statement.lines.map((l) => (
                <tr key={`${l.kind}-${l.code}`}>
                  <td>{l.code}</td>
                  <td>{l.kind}</td>
                  <td>{l.amount}</td>
                  <td>
                    {l.basisAmount}
                    {l.bracket && ` (${l.bracket.schedule.toLowerCase()} schedule, over ${l.bracket.over} at ${l.bracket.rate})`}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
