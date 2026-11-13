import { useState } from "react";
import { get } from "../api";
import { Badge, day, DownloadButton, Field, money, Page, State, useLoad } from "../ui";

export interface OpenItem {
  id: string; documentNumber: string; documentDate: string; dueDate: string; currency: string; amount: string; openAmount: string;
  side: string; status: string; sourceType: string;
}
interface Payment { id: string; paymentMode: string; amount: string; unappliedAmount: string; currency: string; receivedDate: string; valueDate: string; status: string }
interface Section {
  currency: string; closingBalance: string;
  lines: Array<{ sourceType: string; documentNumber: string; documentDate: string; dueDate?: string; amount: string; openAmount: string; runningBalance: string }>;
  aging: Array<{ bucket: string; amount: string }>;
}

/** What is owed, by currency: debits less credits, so an open credit note reduces the balance. */
export function balances(items: OpenItem[]): Array<{ currency: string; open: number; overdue: number }> {
  const today = new Date().toISOString().slice(0, 10);
  const by = new Map<string, { open: number; overdue: number }>();
  for (const i of items) {
    const row = by.get(i.currency) ?? { open: 0, overdue: 0 };
    const amount = Number(i.openAmount);
    row.open += amount;
    if (i.dueDate < today && i.side !== "CREDIT") row.overdue += amount;
    by.set(i.currency, row);
  }
  return [...by.entries()].map(([currency, v]) => ({ currency, ...v }));
}

export function OpenItems() {
  const load = useLoad(() => get<{ items: OpenItem[] }>("/api/v1/open-items?status=open&limit=200"), []);
  return (
    <Page title="Open items">
      <State load={load} empty={(d) => d.items.length === 0}>
        {(d) => (
          <>
            <ul>{balances(d.items).map((b) => <li key={b.currency}>Balance {money(b.open.toFixed(2), b.currency)}{b.overdue > 0 ? `, of which ${money(b.overdue.toFixed(2), b.currency)} is overdue` : ""}</li>)}</ul>
            <table>
              <caption className="sr-only">Your open items</caption>
              <thead><tr><th scope="col">Document</th><th scope="col">Date</th><th scope="col">Due</th><th scope="col">Amount</th><th scope="col">Open</th><th scope="col">Status</th></tr></thead>
              <tbody>
                {d.items.map((i) => (
                  <tr key={i.id}>
                    <th scope="row">{i.documentNumber}</th>
                    <td>{day(i.documentDate)}</td>
                    <td>{day(i.dueDate)}</td>
                    <td>{money(i.amount, i.currency)}</td>
                    <td>{money(i.openAmount, i.currency)}</td>
                    <td><Badge value={i.status} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </>
        )}
      </State>
    </Page>
  );
}

export function Payments() {
  const load = useLoad(() => get<{ items: Payment[] }>("/api/v1/payments?limit=200"), []);
  return (
    <Page title="Payments">
      <State load={load} empty={(d) => d.items.length === 0}>
        {(d) => (
          <table>
            <caption className="sr-only">Payments we received from you</caption>
            <thead><tr><th scope="col">Received</th><th scope="col">Method</th><th scope="col">Amount</th><th scope="col">Not yet applied</th><th scope="col">Status</th></tr></thead>
            <tbody>
              {d.items.map((p) => (
                <tr key={p.id}>
                  <th scope="row">{day(p.receivedDate)}</th>
                  <td>{p.paymentMode.toLowerCase()}</td>
                  <td>{money(p.amount, p.currency)}</td>
                  <td>{money(p.unappliedAmount, p.currency)}</td>
                  <td><Badge value={p.status} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </State>
    </Page>
  );
}

export function Statement() {
  const [asOf, setAsOf] = useState("");
  const load = useLoad(() => get<{ asOf: string; sections: Section[] }>(`/api/v1/statement${asOf ? `?asOf=${asOf}` : ""}`), [asOf]);
  return (
    <Page title="Statement">
      <Field label="As of" hint="Leave empty for today.">{(id) => <input id={id} type="date" value={asOf} aria-describedby={`${id}-hint`} onChange={(e) => setAsOf(e.target.value)} />}</Field>
      <State load={load} empty={(d) => d.sections.length === 0}>
        {(d) => (
          <>
            <DownloadButton path={`/api/v1/statement/pdf${asOf ? `?asOf=${asOf}` : ""}`} fileName={`statement-${d.asOf}.pdf`} label="Download statement PDF" />
            {d.sections.map((s) => (
              <section key={s.currency} aria-label={`Statement in ${s.currency}`}>
                <h2>{s.currency}</h2>
                <table>
                  <caption className="sr-only">Statement in {s.currency} as of {d.asOf}</caption>
                  <thead><tr><th scope="col">Document</th><th scope="col">Date</th><th scope="col">Amount</th><th scope="col">Open</th><th scope="col">Balance</th></tr></thead>
                  <tbody>{s.lines.map((l) => <tr key={l.documentNumber}><th scope="row">{l.documentNumber}</th><td>{day(l.documentDate)}</td><td>{l.amount}</td><td>{l.openAmount}</td><td>{l.runningBalance}</td></tr>)}</tbody>
                  <tfoot><tr><th scope="row" colSpan={4}>Closing balance</th><td>{money(s.closingBalance, s.currency)}</td></tr></tfoot>
                </table>
              </section>
            ))}
          </>
        )}
      </State>
    </Page>
  );
}
