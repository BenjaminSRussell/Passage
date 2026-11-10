import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { get, post } from "../api";
import { Badge, day, Field, messageOf, Notice, Page, State, useLoad } from "../ui";
import type { Po, PoSummary } from "./PurchaseOrders";

interface AsnLine { poLineNo: number; qty: string; batch?: string; expiryDate?: string }
interface Asn { id: string; status: string; purchaseOrderId: string; carrier?: string; trackingNumber?: string; shipDate?: string; expectedArrival?: string; lines: AsnLine[] }

export function Shipments() {
  const load = useLoad(() => get<{ items: Asn[] }>("/api/v1/asns?limit=100"), []);
  return (
    <Page title="Advance ship notices" action={<Link to="/shipments/new" className="button">New notice</Link>}>
      <State load={load} empty={(d) => d.items.length === 0}>
        {(d) => (
          <table>
            <caption className="sr-only">Your advance ship notices</caption>
            <thead><tr><th scope="col">Tracking</th><th scope="col">Carrier</th><th scope="col">Ships</th><th scope="col">Arrives</th><th scope="col">Lines</th><th scope="col">Status</th></tr></thead>
            <tbody>
              {d.items.map((a) => (
                <tr key={a.id}><th scope="row"><Link to={`/shipments/${a.id}`}>{a.trackingNumber || a.id.slice(0, 8)}</Link></th><td>{a.carrier}</td><td>{day(a.shipDate)}</td><td>{day(a.expectedArrival)}</td><td>{a.lines.length}</td><td><Badge value={a.status} /></td></tr>
              ))}
            </tbody>
          </table>
        )}
      </State>
    </Page>
  );
}

export function ShipmentDetail() {
  const { id = "" } = useParams();
  const load = useLoad(() => get<Asn>(`/api/v1/asns/${id}`), [id]);
  return (
    <Page title="Advance ship notice">
      <State load={load}>
        {(a) => (
          <>
            <p>Tracking {a.trackingNumber || "none"} · <Badge value={a.status} /> · <Link to={`/purchase-orders/${a.purchaseOrderId}`}>purchase order</Link></p>
            <table>
              <caption className="sr-only">Shipped lines</caption>
              <thead><tr><th scope="col">PO line</th><th scope="col">Quantity</th><th scope="col">Batch</th><th scope="col">Expiry</th></tr></thead>
              <tbody>{a.lines.map((l) => <tr key={l.poLineNo}><th scope="row">{l.poLineNo}</th><td>{l.qty}</td><td>{l.batch ?? ""}</td><td>{l.expiryDate ?? ""}</td></tr>)}</tbody>
            </table>
          </>
        )}
      </State>
    </Page>
  );
}

interface Row { qty: string; batch: string; expiryDate: string }

/** Builds a notice from the open lines of one of the vendor's purchase orders. */
export function NewShipment() {
  const orders = useLoad(() => get<{ items: PoSummary[] }>("/api/v1/purchase-orders?limit=100"), []);
  const [poId, setPoId] = useState("");
  return (
    <Page title="New advance ship notice">
      <State load={orders}>
        {(d) => (
          <>
            <Field label="Purchase order">
              {(id) => (
                <select id={id} value={poId} onChange={(e) => setPoId(e.target.value)}>
                  <option value="">Choose an order</option>
                  {d.items.filter((o) => o.approvalStatus === "APPROVED" || o.approvalStatus === "CONFIRMED").map((o) => <option key={o.id} value={o.id}>{o.number}</option>)}
                </select>
              )}
            </Field>
            {poId ? <ShipmentForm key={poId} poId={poId} /> : null}
          </>
        )}
      </State>
    </Page>
  );
}

function ShipmentForm({ poId }: { poId: string }) {
  const po = useLoad(() => get<Po>(`/api/v1/purchase-orders/${poId}`), [poId]);
  const nav = useNavigate();
  const [carrier, setCarrier] = useState("");
  const [tracking, setTracking] = useState("");
  const [shipDate, setShipDate] = useState("");
  const [arrival, setArrival] = useState("");
  const [rows, setRows] = useState<Record<number, Row>>({});
  const [error, setError] = useState("");
  const set = (n: number, p: Partial<Row>) => setRows((r) => ({ ...r, [n]: { ...{ qty: "", batch: "", expiryDate: "" }, ...r[n], ...p } }));

  async function submit(e: FormEvent) {
    e.preventDefault();
    const lines = Object.entries(rows).filter(([, r]) => r.qty.trim() !== "").map(([n, r]) => ({ poLineNo: Number(n), qty: r.qty.trim(), ...(r.batch ? { batch: r.batch } : {}), ...(r.expiryDate ? { expiryDate: r.expiryDate } : {}) }));
    if (lines.length === 0) return setError("Enter the quantity shipped on at least one line.");
    setError("");
    try {
      const created = await post<Asn>("/api/v1/asns", { purchaseOrderId: poId, carrier, trackingNumber: tracking, ...(shipDate ? { shipDate } : {}), ...(arrival ? { expectedArrival: arrival } : {}), lines });
      nav(`/shipments/${created.id}`);
    } catch (err) {
      setError(messageOf(err));
    }
  }

  return (
    <State load={po}>
      {(p) => (
        <form onSubmit={submit} noValidate>
          <Field label="Carrier">{(id) => <input id={id} value={carrier} onChange={(e) => setCarrier(e.target.value)} />}</Field>
          <Field label="Tracking number">{(id) => <input id={id} value={tracking} onChange={(e) => setTracking(e.target.value)} />}</Field>
          <Field label="Ship date">{(id) => <input id={id} type="date" value={shipDate} onChange={(e) => setShipDate(e.target.value)} />}</Field>
          <Field label="Expected arrival">{(id) => <input id={id} type="date" value={arrival} onChange={(e) => setArrival(e.target.value)} />}</Field>
          <table>
            <caption>Quantity shipped per line</caption>
            <thead><tr><th scope="col">Line</th><th scope="col">Ordered</th><th scope="col">Received</th><th scope="col">Shipping</th><th scope="col">Batch</th><th scope="col">Expiry</th></tr></thead>
            <tbody>
              {p.lines.filter((l) => l.status !== "CANCELLED").map((l) => (
                <tr key={l.lineNo}>
                  <th scope="row">{l.lineNo}</th><td>{l.qty} {l.unit}</td><td>{l.receivedQty}</td>
                  <td><input aria-label={`Quantity shipped for line ${l.lineNo}`} inputMode="decimal" value={rows[l.lineNo]?.qty ?? ""} onChange={(e) => set(l.lineNo, { qty: e.target.value })} /></td>
                  <td><input aria-label={`Batch for line ${l.lineNo}`} value={rows[l.lineNo]?.batch ?? ""} onChange={(e) => set(l.lineNo, { batch: e.target.value })} /></td>
                  <td><input type="date" aria-label={`Expiry date for line ${l.lineNo}`} value={rows[l.lineNo]?.expiryDate ?? ""} onChange={(e) => set(l.lineNo, { expiryDate: e.target.value })} /></td>
                </tr>
              ))}
            </tbody>
          </table>
          {error ? <Notice kind="error">{error}</Notice> : null}
          <button type="submit">Send notice</button>
        </form>
      )}
    </State>
  );
}
