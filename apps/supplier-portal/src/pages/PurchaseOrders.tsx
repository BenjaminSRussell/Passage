import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { get, post } from "../api";
import { Badge, day, Field, messageOf, Notice, Page, State, useLoad } from "../ui";

export interface PoSummary { id: string; number: string; currency: string; approvalStatus: string; versionNo: number; total: string; lineCount: number; acknowledgement: string; createdAt: string }
export interface PoLine { lineNo: number; description?: string; qty: string; unit: string; unitPrice: string; lineAmount: string; deliveryDate: string; receivedQty: string; status: string }
export interface Ack { id: string; versionNo: number; outcome: string; reason?: string; decision?: string; acknowledgedAt: string }
export interface Po { id: string; number: string; currency: string; approvalStatus: string; versionNo: number; total: string; lines: PoLine[]; acknowledgements: Ack[] }

export function PurchaseOrders() {
  const load = useLoad(() => get<{ items: PoSummary[] }>("/api/v1/purchase-orders?limit=100"), []);
  return (
    <Page title="Purchase orders">
      <State load={load} empty={(d) => d.items.length === 0}>
        {(d) => (
          <table>
            <caption className="sr-only">Purchase orders sent to you</caption>
            <thead><tr><th scope="col">Number</th><th scope="col">Date</th><th scope="col">Lines</th><th scope="col">Total</th><th scope="col">Your answer</th></tr></thead>
            <tbody>
              {d.items.map((o) => (
                <tr key={o.id}>
                  <th scope="row"><Link to={`/purchase-orders/${o.id}`}>{o.number}</Link></th>
                  <td>{day(o.createdAt)}</td><td>{o.lineCount}</td><td>{o.total} {o.currency}</td><td><Badge value={o.acknowledgement || "NONE"} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </State>
    </Page>
  );
}

type Outcome = "ACCEPTED" | "REJECTED" | "CHANGES_PROPOSED";

export function PurchaseOrderDetail() {
  const { id = "" } = useParams();
  const load = useLoad(() => get<Po>(`/api/v1/purchase-orders/${id}`), [id]);
  return (
    <Page title="Purchase order">
      <State load={load}>{(po) => <Detail po={po} onAnswered={load.reload} />}</State>
    </Page>
  );
}

function Detail({ po, onAnswered }: { po: Po; onAnswered: () => void }) {
  return (
    <>
      <p><strong>{po.number}</strong> · version {po.versionNo} · {po.total} {po.currency} · <Badge value={po.approvalStatus} /></p>
      <table>
        <caption className="sr-only">Lines of purchase order {po.number}</caption>
        <thead><tr><th scope="col">Line</th><th scope="col">Item</th><th scope="col">Quantity</th><th scope="col">Unit price</th><th scope="col">Delivery</th><th scope="col">Received</th></tr></thead>
        <tbody>
          {po.lines.map((l) => (
            <tr key={l.lineNo}><th scope="row">{l.lineNo}</th><td>{l.description ?? ""}</td><td>{l.qty} {l.unit}</td><td>{l.unitPrice}</td><td>{day(l.deliveryDate)}</td><td>{l.receivedQty}</td></tr>
          ))}
        </tbody>
      </table>
      <h2>Your answers</h2>
      {po.acknowledgements.length === 0 ? <p>You have not answered this order.</p> : (
        <ul>{po.acknowledgements.map((a) => <li key={a.id}>Version {a.versionNo}: <Badge value={a.outcome} />{a.reason ? ` — ${a.reason}` : ""}{a.decision ? ` (buyer: ${a.decision.toLowerCase()})` : ""}</li>)}</ul>
      )}
      <AckForm po={po} onAnswered={onAnswered} />
    </>
  );
}

interface Change { qty: string; unitPrice: string; deliveryDate: string }

/** Accept, reject with a reason, or propose changes per line. Sends the version the vendor is looking at. */
export function AckForm({ po, onAnswered }: { po: Po; onAnswered: () => void }) {
  const [outcome, setOutcome] = useState<Outcome>("ACCEPTED");
  const [reason, setReason] = useState("");
  const [changes, setChanges] = useState<Record<number, Change>>({});
  const [error, setError] = useState("");
  const [done, setDone] = useState(false);
  const setChange = (lineNo: number, patch: Partial<Change>) => setChanges((c) => ({ ...c, [lineNo]: { ...{ qty: "", unitPrice: "", deliveryDate: "" }, ...c[lineNo], ...patch } }));

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    const body: Record<string, unknown> = { versionNo: po.versionNo, outcome };
    if (reason.trim()) body["reason"] = reason.trim();
    if (outcome === "REJECTED" && !reason.trim()) return setError("Say why you are rejecting this order.");
    if (outcome === "CHANGES_PROPOSED") {
      const list = Object.entries(changes)
        .map(([lineNo, c]) => ({ lineNo: Number(lineNo), ...(c.qty ? { qty: c.qty } : {}), ...(c.unitPrice ? { unitPrice: c.unitPrice } : {}), ...(c.deliveryDate ? { deliveryDate: c.deliveryDate } : {}) }))
        .filter((c) => Object.keys(c).length > 1);
      if (list.length === 0) return setError("Propose a quantity, price or delivery date on at least one line.");
      body["changes"] = list;
    }
    try {
      await post(`/api/v1/purchase-orders/${po.id}/acknowledgements`, body);
      setDone(true);
      onAnswered();
    } catch (err) {
      setError(messageOf(err));
    }
  }

  return (
    <form onSubmit={submit} aria-labelledby="answer-title" noValidate>
      <h2 id="answer-title">Answer this order</h2>
      <fieldset>
        <legend>Your answer</legend>
        {(["ACCEPTED", "REJECTED", "CHANGES_PROPOSED"] as const).map((o) => (
          <label key={o} className="inline"><input type="radio" name="outcome" value={o} checked={outcome === o} onChange={() => setOutcome(o)} /> {{ ACCEPTED: "Accept", REJECTED: "Reject", CHANGES_PROPOSED: "Propose changes" }[o]}</label>
        ))}
      </fieldset>
      {outcome !== "ACCEPTED" ? <Field label="Reason">{(id) => <textarea id={id} value={reason} onChange={(e) => setReason(e.target.value)} maxLength={2000} />}</Field> : null}
      {outcome === "CHANGES_PROPOSED" ? (
        <table>
          <caption>Proposed changes (leave a cell empty to keep the ordered value)</caption>
          <thead><tr><th scope="col">Line</th><th scope="col">Quantity</th><th scope="col">Unit price</th><th scope="col">Delivery date</th></tr></thead>
          <tbody>
            {po.lines.map((l) => (
              <tr key={l.lineNo}>
                <th scope="row">{l.lineNo}</th>
                <td><input aria-label={`Quantity for line ${l.lineNo}`} inputMode="decimal" value={changes[l.lineNo]?.qty ?? ""} onChange={(e) => setChange(l.lineNo, { qty: e.target.value })} /></td>
                <td><input aria-label={`Unit price for line ${l.lineNo}`} inputMode="decimal" value={changes[l.lineNo]?.unitPrice ?? ""} onChange={(e) => setChange(l.lineNo, { unitPrice: e.target.value })} /></td>
                <td><input type="date" aria-label={`Delivery date for line ${l.lineNo}`} value={changes[l.lineNo]?.deliveryDate ?? ""} onChange={(e) => setChange(l.lineNo, { deliveryDate: e.target.value })} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}
      {error ? <Notice kind="error">{error}</Notice> : null}
      {done ? <Notice kind="ok">Your answer was sent.</Notice> : null}
      <button type="submit">Send answer</button>
    </form>
  );
}
