import { useState } from "react";
import { get } from "../api";
import { Badge, day, Field, Page, State, useLoad } from "../ui";

export interface Payment {
  invoiceId: string;
  invoiceNumber: string;
  invoiceDate: string;
  dueDate: string;
  currency: string;
  amount: string;
  paidAmount: string;
  openAmount: string;
  status: string;
  scheduledFor?: string;
  paidAt?: string;
}

const STATUSES: Array<[string, string]> = [["", "All"], ["OPEN", "Waiting for payment"], ["SCHEDULED", "Scheduled"], ["PARTIALLY_PAID", "Partly paid"], ["PAID", "Paid"], ["CANCELLED", "Cancelled"]];

/** When the money moves, in the supplier's words: the paid date, the scheduled date, or the due date. */
export function whenOf(p: Payment): string {
  if (p.status === "PAID") return p.paidAt ? `Paid ${day(p.paidAt)}` : "Paid";
  if (p.status === "PARTIALLY_PAID") return `Part paid ${day(p.paidAt)}, rest due ${day(p.dueDate)}`;
  if (p.status === "SCHEDULED") return p.scheduledFor ? `Payment run ${day(p.scheduledFor)}` : `Due ${day(p.dueDate)}`;
  if (p.status === "CANCELLED") return "Cancelled";
  return `Due ${day(p.dueDate)}`;
}

export function Payments() {
  const [status, setStatus] = useState("");
  const load = useLoad(() => get<{ items: Payment[] }>(`/api/v1/payments?limit=100${status ? `&status=${status}` : ""}`), [status]);
  return (
    <Page title="Payments">
      <Field label="Show">{(id) => <select id={id} value={status} onChange={(e) => setStatus(e.target.value)}>{STATUSES.map(([v, label]) => <option key={v} value={v}>{label}</option>)}</select>}</Field>
      <State load={load} empty={(d) => d.items.length === 0}>
        {(d) => (
          <table>
            <caption className="sr-only">Payment status of your invoices</caption>
            <thead><tr><th scope="col">Invoice</th><th scope="col">Invoice date</th><th scope="col">Amount</th><th scope="col">Paid</th><th scope="col">Still to pay</th><th scope="col">Status</th><th scope="col">When</th></tr></thead>
            <tbody>
              {d.items.map((p) => (
                <tr key={p.invoiceId}>
                  <th scope="row">{p.invoiceNumber}</th><td>{day(p.invoiceDate)}</td><td>{p.amount} {p.currency}</td><td>{p.paidAmount} {p.currency}</td><td>{p.openAmount} {p.currency}</td>
                  <td><Badge value={p.status} /></td><td>{whenOf(p)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </State>
    </Page>
  );
}
