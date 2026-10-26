import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { useParams } from "react-router-dom";
import { fetchEntry, fetchTrialBalance, type TrialBalanceQuery } from "./api/client";

const UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const PERIOD = /^\d{4}-\d{2}$/;

/** Trial balance: account rows from the balance projection, with the balanced check. */
export function TrialBalanceScreen() {
  const [draft, setDraft] = useState<TrialBalanceQuery>({ ledgerId: "" });
  const [query, setQuery] = useState<TrialBalanceQuery | null>(null);
  const [formError, setFormError] = useState<string | null>(null);

  const result = useQuery({
    queryKey: ["ledger", "trial-balance", query],
    queryFn: () => fetchTrialBalance(query as TrialBalanceQuery),
    enabled: query !== null,
  });

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!UUID.test(draft.ledgerId)) {
      setFormError("Enter the ledger id as a UUID.");
      return;
    }
    if ((draft.periodFrom && !PERIOD.test(draft.periodFrom)) || (draft.periodTo && !PERIOD.test(draft.periodTo))) {
      setFormError("Periods use the form YYYY-MM.");
      return;
    }
    setFormError(null);
    setQuery({ ...draft, ledgerId: draft.ledgerId.trim() });
  }

  return (
    <section aria-labelledby="tb-heading">
      <h1 id="tb-heading">Trial balance</h1>
      <form onSubmit={submit} aria-label="Trial balance filters">
        <label>
          Ledger id
          <input name="ledger" value={draft.ledgerId} onChange={(e) => setDraft({ ...draft, ledgerId: e.target.value })} />
        </label>
        <label>
          Period from
          <input name="periodFrom" placeholder="2026-01" value={draft.periodFrom ?? ""} onChange={(e) => setDraft({ ...draft, periodFrom: e.target.value || undefined })} />
        </label>
        <label>
          Period to
          <input name="periodTo" placeholder="2026-12" value={draft.periodTo ?? ""} onChange={(e) => setDraft({ ...draft, periodTo: e.target.value || undefined })} />
        </label>
        <button type="submit">Show trial balance</button>
      </form>
      {formError && <p role="alert">{formError}</p>}
      {result.isPending && query && <p role="status">Loading trial balance…</p>}
      {result.isError && <p role="alert">{(result.error as Error).message}</p>}
      {result.data && (
        <>
          <p data-testid="tb-balanced">{result.data.balanced ? "Balanced" : "Not balanced"}</p>
          <table>
            <caption>Accounts in {result.data.currency}</caption>
            <thead>
              <tr>
                <th scope="col">Account</th>
                <th scope="col">Name</th>
                <th scope="col">Debit</th>
                <th scope="col">Credit</th>
                <th scope="col">Net</th>
                <th scope="col">Lines</th>
              </tr>
            </thead>
            <tbody>
              {result.data.rows.map((r) => (
                <tr key={r.account}>
                  <th scope="row">{r.account}</th>
                  <td>{r.name}</td>
                  <td>{r.debit}</td>
                  <td>{r.credit}</td>
                  <td>{r.net}</td>
                  <td>{r.lineCount}</td>
                </tr>
              ))}
            </tbody>
            <tfoot>
              <tr>
                <th scope="row" colSpan={2}>Total</th>
                <td>{result.data.debit}</td>
                <td>{result.data.credit}</td>
                <td>{result.data.net}</td>
                <td />
              </tr>
            </tfoot>
          </table>
        </>
      )}
    </section>
  );
}

/** One posted entry with its lines and totals (drill-down from the trial balance). */
export function EntryScreen() {
  const { id = "" } = useParams();
  const result = useQuery({
    queryKey: ["ledger", "entry", id],
    queryFn: () => fetchEntry(id),
    enabled: UUID.test(id),
  });

  if (!UUID.test(id)) return <p role="alert">The entry id in the link is not valid.</p>;
  if (result.isPending) return <p role="status">Loading entry…</p>;
  if (result.isError) return <p role="alert">{(result.error as Error).message}</p>;

  const entry = result.data;
  return (
    <section aria-labelledby="entry-heading">
      <h1 id="entry-heading">Journal entry {entry.voucher}</h1>
      <dl>
        <dt>Posting sequence</dt>
        <dd>{entry.postingSequence}</dd>
        <dt>Posting date</dt>
        <dd>{entry.postingDate}</dd>
        <dt>Period</dt>
        <dd>{entry.periodKey}</dd>
        <dt>Description</dt>
        <dd>{entry.description ?? ""}</dd>
        <dt>Trace</dt>
        <dd>{entry.traceId}</dd>
      </dl>
      <table>
        <caption>Lines</caption>
        <thead>
          <tr>
            <th scope="col">Line</th>
            <th scope="col">Account</th>
            <th scope="col">Dimensions</th>
            <th scope="col">D/C</th>
            <th scope="col">Amount</th>
            <th scope="col">Text</th>
          </tr>
        </thead>
        <tbody>
          {entry.lines.map((l) => (
            <tr key={l.id}>
              <td>{l.lineNumber}</td>
              <td>{l.account}</td>
              <td>{l.displayName}</td>
              <td>{l.debitCredit}</td>
              <td>{l.amount}</td>
              <td>{l.text ?? ""}</td>
            </tr>
          ))}
        </tbody>
        <tfoot>
          <tr>
            <th scope="row" colSpan={4}>Totals ({entry.totals.currency})</th>
            <td>{entry.totals.debit} / {entry.totals.credit}</td>
            <td />
          </tr>
        </tfoot>
      </table>
    </section>
  );
}
