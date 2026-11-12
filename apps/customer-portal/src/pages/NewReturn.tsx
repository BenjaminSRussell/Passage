import { useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { get, newSubmitKey, post } from "../api";
import { day, Field, Notice, Page, State, messageOf, useLoad } from "../ui";
import type { Return } from "./Returns";

interface InvoiceRow { id: string; type: string; status: string; number: string; currency: string; invoiceDate: string }
interface InvoiceLine { lineNo: number; productId?: string; description: string; qty: string }
interface InvoiceDetail extends InvoiceRow { lines?: InvoiceLine[] }

export const REASONS: Array<[string, string]> = [["DEFECT", "The goods are defective"], ["DAMAGED", "The goods were damaged on the way"], ["WRONG_ITEM", "I received the wrong item"], ["NOT_NEEDED", "I no longer need the goods"], ["OTHER", "Another reason"]];

/** Request a return of goods from one of the customer's own invoices. Staff authorize it; nothing is credited until the goods are inspected. */
export function NewReturn() {
  const invoices = useLoad(() => get<{ items: InvoiceRow[] }>("/api/v1/invoices?limit=100"), []);
  const [invoiceId, setInvoiceId] = useState("");
  return (
    <Page title="Request a return">
      <State load={invoices} empty={(d) => d.items.filter(returnable).length === 0}>
        {(d) => {
          const rows = d.items.filter(returnable);
          const chosen = invoiceId || (rows[0]?.id ?? "");
          return (
            <>
              <Field label="Invoice">
                {(id) => (
                  <select id={id} value={chosen} onChange={(e) => setInvoiceId(e.target.value)}>
                    {rows.map((r) => <option key={r.id} value={r.id}>{r.number} ({day(r.invoiceDate)})</option>)}
                  </select>
                )}
              </Field>
              <ReturnLines key={chosen} invoiceId={chosen} />
            </>
          );
        }}
      </State>
    </Page>
  );
}

const returnable = (i: InvoiceRow): boolean => i.type !== "CREDIT_NOTE" && i.status !== "PROPOSAL";

function ReturnLines({ invoiceId }: { invoiceId: string }) {
  const detail = useLoad(() => get<InvoiceDetail>(`/api/v1/invoices/${invoiceId}`), [invoiceId]);
  const navigate = useNavigate();
  const [qty, setQty] = useState<Record<number, string>>({});
  const [reason, setReason] = useState("DEFECT");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const submitted = useRef<{ body: string; key: string }>({ body: "", key: "" });
  return (
    <State load={detail}>
      {(inv) => {
        const lines = (inv.lines ?? []).filter((l) => l.productId);
        const skipped = (inv.lines ?? []).length - lines.length;
        const submit = async (e: FormEvent) => {
          e.preventDefault();
          setError("");
          const chosen = lines.filter((l) => Number(qty[l.lineNo]) > 0);
          if (chosen.length === 0) return setError("Enter the quantity you want to return on at least one line.");
          const over = chosen.find((l) => Number(qty[l.lineNo]) > Number(l.qty));
          if (over) return setError(`You can return at most ${over.qty} of "${over.description}".`);
          const body = { originalInvoiceId: invoiceId, returnReasonCode: reason, lines: chosen.map((l) => ({ productId: l.productId as string, quantity: (qty[l.lineNo] ?? "").trim() })) };
          const text = JSON.stringify(body);
          if (submitted.current.body !== text) submitted.current = { body: text, key: newSubmitKey() };
          setBusy(true);
          try {
            const created = await post<Return>("/api/v1/returns", body, submitted.current.key);
            navigate(`/returns/${created.id}`, { state: { requested: created.rmaNumber } });
          } catch (err) {
            setError(messageOf(err));
            setBusy(false);
          }
        };
        return (
          <form onSubmit={(e) => void submit(e)} noValidate>
            <table>
              <caption className="sr-only">Lines of invoice {inv.number}</caption>
              <thead><tr><th scope="col">Product</th><th scope="col">Invoiced</th><th scope="col">Quantity to return</th></tr></thead>
              <tbody>
                {lines.map((l) => (
                  <tr key={l.lineNo}>
                    <th scope="row">{l.description}</th>
                    <td>{l.qty}</td>
                    <td><input aria-label={`Quantity to return of ${l.description}`} inputMode="decimal" value={qty[l.lineNo] ?? ""} onChange={(e) => setQty({ ...qty, [l.lineNo]: e.target.value })} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
            {skipped > 0 ? <p>{skipped} line{skipped === 1 ? "" : "s"} of this invoice cannot be returned here. Contact us to return {skipped === 1 ? "it" : "them"}.</p> : null}
            <Field label="Reason">
              {(id) => <select id={id} value={reason} onChange={(e) => setReason(e.target.value)}>{REASONS.map(([code, text]) => <option key={code} value={code}>{text}</option>)}</select>}
            </Field>
            <p>Credit is given after we have received and inspected the goods.</p>
            {error ? <Notice kind="error">{error}</Notice> : null}
            <button type="submit" disabled={busy || lines.length === 0}>{busy ? "Requesting…" : "Request the return"}</button>
          </form>
        );
      }}
    </State>
  );
}
