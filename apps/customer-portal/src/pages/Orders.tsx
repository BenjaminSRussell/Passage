import { Link, useParams } from "react-router-dom";
import { get } from "../api";
import { Badge, day, money, Page, State, useLoad } from "../ui";

export interface Order {
  id: string; orderNumber: string; orderType: string; status: string; currency: string; requestedShipDate: string; externalRef?: string;
  netTotal: string; lineCount?: number; createdAt: string;
  lines?: Array<{ lineNo: number; quantity: string; unit: string; unitPrice: string; lineNet: string; requestedShipDate: string; confirmedShipDate?: string; lineStatus: string; deliveredQty: string; invoicedQty: string }>;
}

export function Orders() {
  const list = useLoad(() => get<{ items: Order[] }>("/api/v1/orders?limit=100"), []);
  return (
    <Page title="Orders">
      <State load={list} empty={(d) => d.items.length === 0}>
        {(d) => (
          <table>
            <caption className="sr-only">Your orders</caption>
            <thead><tr><th scope="col">Order</th><th scope="col">Your reference</th><th scope="col">Requested ship date</th><th scope="col">Net total</th><th scope="col">Status</th></tr></thead>
            <tbody>
              {d.items.map((o) => (
                <tr key={o.id}>
                  <th scope="row"><Link to={`/orders/${o.id}`}>{o.orderNumber}</Link></th>
                  <td>{o.externalRef ?? ""}</td>
                  <td>{day(o.requestedShipDate)}</td>
                  <td>{money(o.netTotal, o.currency)}</td>
                  <td><Badge value={o.status} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </State>
    </Page>
  );
}

export function OrderDetail() {
  const { id = "" } = useParams();
  const load = useLoad(() => get<Order>(`/api/v1/orders/${id}`), [id]);
  return (
    <Page title="Order">
      <State load={load}>
        {(o) => (
          <>
            <p><Link to="/orders">Back to orders</Link></p>
            <dl>
              <dt>Order</dt><dd>{o.orderNumber}</dd>
              <dt>Status</dt><dd><Badge value={o.status} /></dd>
              <dt>Net total</dt><dd>{money(o.netTotal, o.currency)}</dd>
            </dl>
            <table>
              <caption className="sr-only">Lines of order {o.orderNumber}</caption>
              <thead><tr><th scope="col">Line</th><th scope="col">Quantity</th><th scope="col">Unit price</th><th scope="col">Net</th><th scope="col">Ship date</th><th scope="col">Delivered</th><th scope="col">Invoiced</th><th scope="col">Status</th></tr></thead>
              <tbody>
                {(o.lines ?? []).map((l) => (
                  <tr key={l.lineNo}>
                    <th scope="row">{l.lineNo}</th>
                    <td>{l.quantity} {l.unit}</td>
                    <td>{l.unitPrice}</td>
                    <td>{l.lineNet}</td>
                    <td>{day(l.confirmedShipDate ?? l.requestedShipDate)}</td>
                    <td>{l.deliveredQty}</td>
                    <td>{l.invoicedQty}</td>
                    <td><Badge value={l.lineStatus} /></td>
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
