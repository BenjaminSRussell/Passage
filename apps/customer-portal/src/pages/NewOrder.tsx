import { useRef, useState, type FormEvent } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { get, newSubmitKey, post } from "../api";
import { Field, money, Notice, Page, State, messageOf, useLoad } from "../ui";
import type { Order } from "./Orders";

export interface Product { productId: string; productName?: string; unit: string; unitPrice: string; currency: string; lastOrderedAt: string }
interface Line { choice: string; quantity: string }

const choiceOf = (p: Pick<Product, "productId" | "unit">): string => `${p.productId}|${p.unit}`;
const label = (p: Product): string => `${p.productName ?? "Product"} (${p.unit}, ${money(p.unitPrice, p.currency)} last time)`;

/**
 * Place an order. The customer can order only what it has bought before, at the price, unit and warehouse of its last order: the
 * list comes from the server and the form sends only a product, a unit and a quantity. A submit that fails and is sent again with the
 * same lines carries the same key, so it can never place two orders; changing a line is a new submit with a new key.
 */
export function NewOrder() {
  const products = useLoad(() => get<{ items: Product[] }>("/api/v1/products"), []);
  const navigate = useNavigate();
  const again = (useLocation().state as { lines?: Array<{ productId: string; unit: string; quantity: string }> } | null)?.lines;
  const [lines, setLines] = useState<Line[] | undefined>();
  const [date, setDate] = useState("");
  const [reference, setReference] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const submitted = useRef<{ body: string; key: string }>({ body: "", key: "" });
  const today = new Date().toISOString().slice(0, 10);

  return (
    <Page title="New order">
      <State load={products} empty={(d) => d.items.length === 0}>
        {(d) => {
          const known = new Set(d.items.map(choiceOf));
          const fromAgain: Line[] = (again ?? []).filter((l) => known.has(`${l.productId}|${l.unit}`)).map((l) => ({ choice: `${l.productId}|${l.unit}`, quantity: l.quantity }));
          const current: Line[] = lines ?? fromAgain;
          const shown = current.length > 0 ? current : [{ choice: choiceOf(d.items[0] as Product), quantity: "1" }];
          const change = (i: number, patch: Partial<Line>) => setLines(shown.map((l, n) => (n === i ? { ...l, ...patch } : l)));
          const submit = async (e: FormEvent) => {
            e.preventDefault();
            setError("");
            if (!date) return setError("Choose the date you want the goods shipped.");
            if (shown.some((l) => !(Number(l.quantity) > 0))) return setError("Every quantity must be more than zero.");
            const body = {
              requestedShipDate: date,
              ...(reference.trim() ? { externalRef: reference.trim() } : {}),
              lines: shown.map((l) => {
                const [productId = "", unit = ""] = l.choice.split("|");
                return { productId, unit, quantity: l.quantity.trim() };
              }),
            };
            const text = JSON.stringify(body);
            if (submitted.current.body !== text) submitted.current = { body: text, key: newSubmitKey() };
            setBusy(true);
            try {
              const order = await post<Order>("/api/v1/orders", body, submitted.current.key);
              navigate(`/orders/${order.id}`, { state: { placed: order.orderNumber } });
            } catch (err) {
              setError(messageOf(err));
              setBusy(false);
            }
          };
          return (
            <form onSubmit={(e) => void submit(e)} noValidate>
              <p>You can order the products you have bought before. Prices are those of your last order; the order is confirmed after a credit check.</p>
              {shown.map((l, i) => (
                <fieldset key={i}>
                  <legend>Line {i + 1}</legend>
                  <Field label={`Product ${i + 1}`}>
                    {(id) => (
                      <select id={id} value={l.choice} onChange={(e) => change(i, { choice: e.target.value })}>
                        {d.items.map((p) => <option key={choiceOf(p)} value={choiceOf(p)}>{label(p)}</option>)}
                      </select>
                    )}
                  </Field>
                  <Field label={`Quantity ${i + 1}`}>
                    {(id) => <input id={id} inputMode="decimal" value={l.quantity} onChange={(e) => change(i, { quantity: e.target.value })} />}
                  </Field>
                  {shown.length > 1 ? <button type="button" className="secondary" onClick={() => setLines(shown.filter((_, n) => n !== i))}>Remove line {i + 1}</button> : null}
                </fieldset>
              ))}
              <p><button type="button" className="secondary" onClick={() => setLines([...shown, { choice: choiceOf(d.items[0] as Product), quantity: "1" }])}>Add a line</button></p>
              <Field label="Ship on">{(id) => <input id={id} type="date" min={today} value={date} onChange={(e) => setDate(e.target.value)} />}</Field>
              <Field label="Your reference" hint="Your own purchase order number. Optional.">
                {(id) => <input id={id} maxLength={100} value={reference} onChange={(e) => setReference(e.target.value)} aria-describedby={`${id}-hint`} />}
              </Field>
              {error ? <Notice kind="error">{error}</Notice> : null}
              <button type="submit" disabled={busy}>{busy ? "Placing the order…" : "Place the order"}</button>
            </form>
          );
        }}
      </State>
    </Page>
  );
}
