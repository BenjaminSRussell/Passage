import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { get, post } from "../api";
import { Badge, day, Field, messageOf, Notice, Page, State, useLoad } from "../ui";
import type { Po } from "./PurchaseOrders";

interface Line {
  receiptId: string;
  receiptLineNo: number;
  packingSlip: string;
  receiptDate: string;
  purchaseOrderId: string;
  poLineNo?: number;
  receivedQty: string;
  availableQty: string;
  unitPrice: string;
  currency: string;
}
interface Submission { id: string; invoiceNumber: string; invoiceDate: string; currency: string; total: string; status: string; decisionNote?: string; createdAt: string }
interface Pick { qty: string; unitPrice: string }

/** Names for the lines: the order number and the item's description come from the purchase orders the lines belong to. */
function useOrders(lines: Line[]) {
  const ids = [...new Set(lines.map((l) => l.purchaseOrderId))].sort();
  return useLoad(async () => {
    const out: Record<string, Po> = {};
    await Promise.all(ids.map(async (id) => { out[id] = await get<Po>(`/api/v1/purchase-orders/${id}`); }));
    return out;
  }, [ids.join(",")]);
}

/** The amount of a line in cents-free decimal text; empty or unreadable is zero. */
export function lineAmount(qty: string, price: string): number {
  const q = Number(qty);
  const p = Number(price);
  return Number.isFinite(q) && Number.isFinite(p) ? Math.round(q * p * 1e6) / 1e6 : 0;
}

export function InvoiceLines() {
  const lines = useLoad(() => get<{ items: Line[] }>("/api/v1/invoiceable-lines?limit=200"), []);
  const submitted = useLoad(() => get<{ items: Submission[] }>("/api/v1/invoice-submissions?limit=100"), []);
  return (
    <Page title="Invoice received goods" action={<Link to="/invoices">Back to invoices</Link>}>
      <p>Choose the goods we have received from you and bill them. Your invoice is checked by accounts payable before it is posted.</p>
      <State load={lines} empty={(d) => d.items.length === 0}>
        {(d) => <Builder lines={d.items} onSent={() => { lines.reload(); submitted.reload(); }} />}
      </State>
      <h2>Invoices you submitted</h2>
      <State load={submitted} empty={(d) => d.items.length === 0}>
        {(d) => <Submitted items={d.items} onChanged={() => { lines.reload(); submitted.reload(); }} />}
      </State>
    </Page>
  );
}

function Builder({ lines, onSent }: { lines: Line[]; onSent: () => void }) {
  const orders = useOrders(lines);
  const [picked, setPicked] = useState<Record<string, Pick>>({});
  const [number, setNumber] = useState("");
  const [date, setDate] = useState("");
  const [note, setNote] = useState("");
  const [error, setError] = useState("");
  const [sent, setSent] = useState("");
  const key = (l: Line) => `${l.receiptId}:${l.receiptLineNo}`;
  const nameOf = (l: Line) => {
    const po = orders.data?.[l.purchaseOrderId];
    const item = po?.lines.find((x) => x.lineNo === l.poLineNo);
    return { order: po?.number ?? "Purchase order", item: item?.description ?? (l.poLineNo ? `Line ${l.poLineNo}` : "Item") };
  };
  const chosen = lines.filter((l) => picked[key(l)]);
  const currencies = [...new Set(chosen.map((l) => l.currency))];
  const total = chosen.reduce((sum, l) => sum + lineAmount(picked[key(l)]?.qty ?? "", picked[key(l)]?.unitPrice ?? ""), 0);

  function toggle(l: Line, on: boolean) {
    setPicked((p) => {
      const next = { ...p };
      if (on) next[key(l)] = { qty: l.availableQty, unitPrice: l.unitPrice };
      else delete next[key(l)];
      return next;
    });
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    setSent("");
    if (chosen.length === 0) return setError("Choose at least one received line.");
    if (currencies.length > 1) return setError("One invoice is in one currency. Choose lines in the same currency.");
    if (!number.trim()) return setError("Enter your invoice number.");
    if (!/^\d{4}-\d{2}-\d{2}$/.test(date)) return setError("Enter the invoice date.");
    for (const l of chosen) {
      const p = picked[key(l)] as Pick;
      const q = Number(p.qty);
      if (!(q > 0)) return setError(`Enter a quantity above zero for ${nameOf(l).item}.`);
      if (q > Number(l.availableQty)) return setError(`${nameOf(l).item}: only ${l.availableQty} can still be invoiced.`);
      if (!(Number(p.unitPrice) >= 0) || p.unitPrice.trim() === "") return setError(`Enter a unit price for ${nameOf(l).item}.`);
    }
    try {
      await post("/api/v1/invoice-submissions", {
        invoiceNumber: number.trim(), invoiceDate: date, currency: currencies[0], note: note.trim(),
        lines: chosen.map((l) => ({ receiptId: l.receiptId, receiptLineNo: l.receiptLineNo, qty: (picked[key(l)] as Pick).qty, unitPrice: (picked[key(l)] as Pick).unitPrice })),
      });
      setSent(`Invoice ${number.trim()} was sent for review.`);
      setPicked({});
      setNumber("");
      setNote("");
      onSent();
    } catch (err) {
      setError(messageOf(err));
    }
  }

  return (
    <form onSubmit={submit} noValidate>
      <table>
        <caption className="sr-only">Received goods you can still invoice</caption>
        <thead>
          <tr><th scope="col">Bill</th><th scope="col">Item</th><th scope="col">Order</th><th scope="col">Delivery note</th><th scope="col">Received</th><th scope="col">Left to invoice</th><th scope="col">Quantity</th><th scope="col">Unit price</th></tr>
        </thead>
        <tbody>
          {lines.map((l) => {
            const n = nameOf(l);
            const p = picked[key(l)];
            return (
              <tr key={key(l)}>
                <td><input type="checkbox" aria-label={`Bill ${n.item} from delivery note ${l.packingSlip}`} checked={Boolean(p)} onChange={(e) => toggle(l, e.target.checked)} /></td>
                <th scope="row">{n.item}</th><td>{n.order}</td><td>{l.packingSlip} · {day(l.receiptDate)}</td><td>{l.receivedQty}</td><td>{l.availableQty}</td>
                <td>{p ? <input aria-label={`Quantity of ${n.item}`} inputMode="decimal" value={p.qty} onChange={(e) => setPicked((x) => ({ ...x, [key(l)]: { ...p, qty: e.target.value } }))} /> : null}</td>
                <td>{p ? <input aria-label={`Unit price of ${n.item}`} inputMode="decimal" value={p.unitPrice} onChange={(e) => setPicked((x) => ({ ...x, [key(l)]: { ...p, unitPrice: e.target.value } }))} /> : `${l.unitPrice} ${l.currency}`}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
      <Field label="Invoice number">{(id) => <input id={id} value={number} maxLength={64} onChange={(e) => setNumber(e.target.value)} />}</Field>
      <Field label="Invoice date">{(id) => <input id={id} type="date" value={date} onChange={(e) => setDate(e.target.value)} />}</Field>
      <Field label="Note for accounts payable">{(id) => <textarea id={id} value={note} maxLength={1000} onChange={(e) => setNote(e.target.value)} />}</Field>
      <p aria-live="polite">{chosen.length} line{chosen.length === 1 ? "" : "s"} chosen · total {total.toFixed(2)} {currencies[0] ?? ""}</p>
      {error ? <Notice kind="error">{error}</Notice> : null}
      {sent ? <Notice kind="ok">{sent}</Notice> : null}
      <button type="submit">Send invoice</button>
    </form>
  );
}

const WORDS: Record<string, string> = { SUBMITTED: "IN_REVIEW", POSTED: "ACCEPTED" };

function Submitted({ items, onChanged }: { items: Submission[]; onChanged: () => void }) {
  const [error, setError] = useState("");
  return (
    <>
      {error ? <Notice kind="error">{error}</Notice> : null}
      <table>
        <caption className="sr-only">Invoices you submitted against received goods</caption>
        <thead><tr><th scope="col">Invoice</th><th scope="col">Date</th><th scope="col">Total</th><th scope="col">Status</th><th scope="col">Notes</th><th scope="col">Actions</th></tr></thead>
        <tbody>
          {items.map((s) => (
            <tr key={s.id}>
              <th scope="row">{s.invoiceNumber}</th><td>{day(s.invoiceDate)}</td><td>{s.total} {s.currency}</td><td><Badge value={WORDS[s.status] ?? s.status} /></td><td>{s.decisionNote ?? ""}</td>
              <td>{s.status === "SUBMITTED" ? <button type="button" className="secondary" onClick={() => post(`/api/v1/invoice-submissions/${s.id}/withdraw`).then(onChanged, (e: unknown) => setError(messageOf(e)))}>Withdraw {s.invoiceNumber}</button> : null}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}
