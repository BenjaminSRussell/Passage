import { Link, useParams } from "react-router-dom";
import { get } from "../api";
import { Badge, day, DownloadButton, money, Page, State, useLoad } from "../ui";

interface Totals { net: string; charges: string; tax: string; gross: string }
export interface Invoice {
  id: string; type: string; status: string; number: string; currency: string; invoiceDate: string; dueDate: string;
  paymentTermDays: number; totals: Totals; originalNumber?: string;
  lines?: Array<{ lineNo: number; description: string; qty: string; unitPrice: string; discountPct: string; net: string; tax: string }>;
}

export function Invoices() {
  const list = useLoad(() => get<{ items: Invoice[] }>("/api/v1/invoices?limit=100"), []);
  return (
    <Page title="Invoices">
      <State load={list} empty={(d) => d.items.length === 0}>
        {(d) => (
          <table>
            <caption className="sr-only">Your invoices and credit notes</caption>
            <thead><tr><th scope="col">Number</th><th scope="col">Type</th><th scope="col">Date</th><th scope="col">Due</th><th scope="col">Total</th><th scope="col">Status</th></tr></thead>
            <tbody>
              {d.items.map((i) => (
                <tr key={i.id}>
                  <th scope="row"><Link to={`/invoices/${i.id}`}>{i.number}</Link></th>
                  <td>{i.type === "CREDIT_NOTE" ? "Credit note" : "Invoice"}</td>
                  <td>{day(i.invoiceDate)}</td>
                  <td>{day(i.dueDate)}</td>
                  <td>{money(i.totals.gross, i.currency)}</td>
                  <td><Badge value={i.status} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </State>
    </Page>
  );
}

export function InvoiceDetail() {
  const { id = "" } = useParams();
  const load = useLoad(() => get<Invoice>(`/api/v1/invoices/${id}`), [id]);
  return (
    <Page title="Invoice">
      <State load={load}>
        {(i) => (
          <>
            <p><Link to="/invoices">Back to invoices</Link></p>
            <DownloadButton path={`/api/v1/invoices/${i.id}/pdf`} fileName={`${i.number}.pdf`} label={`Download ${i.type === "CREDIT_NOTE" ? "credit note" : "invoice"} PDF`} />
            <dl>
              <dt>Number</dt><dd>{i.number}</dd>
              {i.originalNumber ? <><dt>Credits invoice</dt><dd>{i.originalNumber}</dd></> : null}
              <dt>Date</dt><dd>{day(i.invoiceDate)}</dd>
              <dt>Due</dt><dd>{day(i.dueDate)} ({i.paymentTermDays} days)</dd>
              <dt>Status</dt><dd><Badge value={i.status} /></dd>
            </dl>
            <table>
              <caption className="sr-only">Lines of invoice {i.number}</caption>
              <thead><tr><th scope="col">Line</th><th scope="col">Description</th><th scope="col">Quantity</th><th scope="col">Unit price</th><th scope="col">Net</th><th scope="col">Tax</th></tr></thead>
              <tbody>{(i.lines ?? []).map((l) => <tr key={l.lineNo}><td>{l.lineNo}</td><th scope="row">{l.description}</th><td>{l.qty}</td><td>{l.unitPrice}</td><td>{l.net}</td><td>{l.tax}</td></tr>)}</tbody>
              <tfoot>
                <tr><th scope="row" colSpan={4}>Net</th><td colSpan={2}>{money(i.totals.net, i.currency)}</td></tr>
                <tr><th scope="row" colSpan={4}>Tax</th><td colSpan={2}>{money(i.totals.tax, i.currency)}</td></tr>
                <tr><th scope="row" colSpan={4}>Total</th><td colSpan={2}>{money(i.totals.gross, i.currency)}</td></tr>
              </tfoot>
            </table>
          </>
        )}
      </State>
    </Page>
  );
}
