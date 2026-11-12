import { Link, useLocation, useParams } from "react-router-dom";
import { get } from "../api";
import { Badge, day, money, Notice, Page, State, useLoad } from "../ui";

export interface Return {
  id: string; rmaNumber: string; originalInvoiceNumber: string; returnReasonCode: string; status: string; currency: string; expiresAt?: string;
  gross: string; restocking: string; net: string; tax: string; createdAt: string;
  lines: Array<{ lineNo: number; productId: string; productName?: string; quantityExpected: string; quantityReceived: string; quantityAccepted: string; unit: string; unitPrice: string }>;
}

export function Returns() {
  const list = useLoad(() => get<{ items: Return[] }>("/api/v1/returns?limit=100"), []);
  return (
    <Page title="Returns" action={<Link className="button" to="/returns/new">Request a return</Link>}>
      <State load={list} empty={(d) => d.items.length === 0}>
        {(d) => (
          <table>
            <caption className="sr-only">Your returns</caption>
            <thead><tr><th scope="col">Return</th><th scope="col">Invoice</th><th scope="col">Reason</th><th scope="col">Credit</th><th scope="col">Status</th></tr></thead>
            <tbody>
              {d.items.map((r) => (
                <tr key={r.id}>
                  <th scope="row"><Link to={`/returns/${r.id}`}>{r.rmaNumber}</Link></th>
                  <td>{r.originalInvoiceNumber}</td>
                  <td>{r.returnReasonCode}</td>
                  <td>{money(r.net, r.currency)}</td>
                  <td><Badge value={r.status} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </State>
    </Page>
  );
}

export function ReturnDetail() {
  const { id = "" } = useParams();
  const load = useLoad(() => get<Return>(`/api/v1/returns/${id}`), [id]);
  const requested = (useLocation().state as { requested?: string } | null)?.requested;
  return (
    <Page title="Return">
      {requested ? <Notice kind="ok">Your return {requested} has been requested. We will tell you when it is authorized and where to send the goods.</Notice> : null}
      <State load={load}>
        {(r) => (
          <>
            <p><Link to="/returns">Back to returns</Link></p>
            <dl>
              <dt>Return</dt><dd>{r.rmaNumber}</dd>
              <dt>Invoice</dt><dd>{r.originalInvoiceNumber}</dd>
              <dt>Status</dt><dd><Badge value={r.status} /></dd>
              {r.expiresAt ? <><dt>Send the goods back by</dt><dd>{day(r.expiresAt)}</dd></> : null}
              <dt>Credit</dt><dd>{money(r.net, r.currency)} (goods {r.gross}, restocking fee {r.restocking}, tax {r.tax})</dd>
            </dl>
            <table>
              <caption className="sr-only">Lines of return {r.rmaNumber}</caption>
              <thead><tr><th scope="col">Line</th><th scope="col">Product</th><th scope="col">Expected</th><th scope="col">Received</th><th scope="col">Accepted</th><th scope="col">Unit price</th></tr></thead>
              <tbody>{r.lines.map((l) => <tr key={l.lineNo}><th scope="row">{l.lineNo}</th><td>{l.productName ?? "Product"}</td><td>{l.quantityExpected} {l.unit}</td><td>{l.quantityReceived}</td><td>{l.quantityAccepted}</td><td>{l.unitPrice}</td></tr>)}</tbody>
            </table>
          </>
        )}
      </State>
    </Page>
  );
}
