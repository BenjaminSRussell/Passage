import { useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input } from "../../shell/ui";
import { describeProblem, editTransfer, type Problem, type TransferEditInput, type TransferOrder } from "./api/client";
import { DECIMAL, UUID } from "./format";
import styles from "./pages.module.css";

export interface TransferEditValues {
  shipDate: string;
  plannedReceiptDate: string;
  toSiteId: string;
  /** Quantity per line id, as typed. */
  qty: Record<string, string>;
  remove: Record<string, boolean>;
  add: { productId: string; site: string; unit: string; qty: string };
}

export const startingValues = (order: TransferOrder): TransferEditValues => ({
  shipDate: order.shipDate,
  plannedReceiptDate: order.plannedReceiptDate ?? "",
  toSiteId: order.toSiteId ?? "",
  qty: Object.fromEntries(order.lines.map((l) => [l.id, l.qty])),
  remove: {},
  add: { productId: "", site: "", unit: "", qty: "" },
});

/**
 * Turns the form into an edit holding only what changed, or field errors. The added line is
 * optional: leaving all four of its fields empty adds nothing.
 */
export function buildTransferEdit(order: TransferOrder, v: TransferEditValues): { edit: TransferEditInput; errors: Record<string, string> } {
  const errors: Record<string, string> = {};
  const edit: TransferEditInput = {};
  if (v.shipDate !== order.shipDate) {
    if (v.shipDate === "") errors.shipDate = "Enter the planned ship date.";
    else edit.shipDate = v.shipDate;
  }
  if (v.plannedReceiptDate !== (order.plannedReceiptDate ?? "")) edit.plannedReceiptDate = v.plannedReceiptDate;
  const shipDate = v.shipDate || order.shipDate;
  if (v.plannedReceiptDate !== "" && v.plannedReceiptDate < shipDate) errors.plannedReceiptDate = "The planned receipt cannot be before the ship date.";
  if (v.toSiteId.trim() !== (order.toSiteId ?? "")) edit.toSiteId = v.toSiteId.trim();
  const removed = order.lines.filter((l) => v.remove[l.id]);
  if (removed.length > 0) edit.removeLineIds = removed.map((l) => l.id);
  const updateLines: NonNullable<TransferEditInput["updateLines"]> = [];
  for (const l of order.lines) {
    if (v.remove[l.id]) continue;
    const qty = (v.qty[l.id] ?? "").trim();
    if (qty === l.qty) continue;
    if (!DECIMAL.test(qty) || Number(qty) <= 0) errors[`qty-${l.id}`] = "Enter a quantity above zero.";
    else updateLines.push({ lineId: l.id, qty });
  }
  if (updateLines.length > 0) edit.updateLines = updateLines;
  const a = v.add;
  if (a.productId.trim() || a.site.trim() || a.unit.trim() || a.qty.trim()) {
    if (!UUID.test(a.productId.trim())) errors.addProduct = "Enter the product id (a UUID).";
    if (!a.site.trim()) errors.addSite = "Enter the site.";
    if (!/^[A-Z0-9_-]{1,12}$/.test(a.unit.trim())) errors.addUnit = "Enter the unit (A-Z, 0-9, _ or -).";
    if (!DECIMAL.test(a.qty.trim()) || Number(a.qty) <= 0) errors.addQty = "Enter a quantity above zero.";
    if (Object.keys(errors).every((k) => !k.startsWith("add"))) {
      edit.addLines = [{ productId: a.productId.trim(), dimensions: { site: a.site.trim() }, unit: a.unit.trim(), qty: a.qty.trim() }];
    }
  }
  if (removed.length === order.lines.length && !edit.addLines) errors.form = "An order needs at least one line: add a line before removing them all.";
  if (Object.keys(errors).length === 0 && Object.keys(edit).length === 0) errors.form = "Nothing was changed.";
  return { edit, errors };
}

/** Edit form of a CREATED transfer order (FR-16.15): dates, receiving site and lines. */
export function TransferEditForm({ order, onDone, onError }: { order: TransferOrder; onDone(next: TransferOrder): void; onError(p: Problem): void }) {
  const [v, setV] = useState<TransferEditValues>(() => startingValues(order));
  const [invalid, setInvalid] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { edit, errors } = buildTransferEdit(order, v);
    setInvalid(errors);
    if (Object.keys(errors).length > 0) return;
    setBusy(true);
    try {
      onDone(await editTransfer(order.id, order.version, edit));
    } catch (error) {
      onError(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Edit order">
      <form onSubmit={submit} noValidate className={styles.form}>
        <FormField id="edit-ship-date" label="Planned ship date" error={invalid.shipDate}>
          {(control) => <Input {...control} type="date" value={v.shipDate} onChange={(e) => setV({ ...v, shipDate: e.target.value })} invalid={Boolean(invalid.shipDate)} />}
        </FormField>
        <FormField id="edit-receipt-date" label="Planned receipt date" hint="Leave empty for none." error={invalid.plannedReceiptDate}>
          {(control) => (
            <Input {...control} type="date" value={v.plannedReceiptDate} onChange={(e) => setV({ ...v, plannedReceiptDate: e.target.value })} invalid={Boolean(invalid.plannedReceiptDate)} />
          )}
        </FormField>
        <FormField id="edit-to-site" label="Receiving site" hint="Only when it differs from the lines' site.">
          {(control) => <Input {...control} value={v.toSiteId} onChange={(e) => setV({ ...v, toSiteId: e.target.value })} />}
        </FormField>
        {order.lines.map((l) => (
          <div key={l.id} className={styles.formActions}>
            <FormField id={`edit-qty-${l.lineNo}`} label={`Line ${l.lineNo}: quantity (${l.unit})`} error={invalid[`qty-${l.id}`]}>
              {(control) => (
                <Input
                  {...control}
                  inputMode="decimal"
                  className={styles.narrow}
                  value={v.qty[l.id] ?? ""}
                  disabled={Boolean(v.remove[l.id])}
                  onChange={(e) => setV({ ...v, qty: { ...v.qty, [l.id]: e.target.value } })}
                  invalid={Boolean(invalid[`qty-${l.id}`])}
                />
              )}
            </FormField>
            <span className={styles.check}>
              <Input type="checkbox" id={`edit-remove-${l.lineNo}`} checked={Boolean(v.remove[l.id])} onChange={(e) => setV({ ...v, remove: { ...v.remove, [l.id]: e.target.checked } })} />
              <label htmlFor={`edit-remove-${l.lineNo}`}>{`Remove line ${l.lineNo}`}</label>
            </span>
          </div>
        ))}
        <FormField id="add-product" label="Add a line: product id" hint="Leave the four fields empty to add nothing." error={invalid.addProduct}>
          {(control) => <Input {...control} value={v.add.productId} onChange={(e) => setV({ ...v, add: { ...v.add, productId: e.target.value } })} invalid={Boolean(invalid.addProduct)} />}
        </FormField>
        <FormField id="add-site" label="Add a line: site" error={invalid.addSite}>
          {(control) => <Input {...control} value={v.add.site} onChange={(e) => setV({ ...v, add: { ...v.add, site: e.target.value } })} invalid={Boolean(invalid.addSite)} />}
        </FormField>
        <FormField id="add-unit" label="Add a line: unit" error={invalid.addUnit}>
          {(control) => <Input {...control} className={styles.narrow} value={v.add.unit} onChange={(e) => setV({ ...v, add: { ...v.add, unit: e.target.value } })} invalid={Boolean(invalid.addUnit)} />}
        </FormField>
        <FormField id="add-qty" label="Add a line: quantity" error={invalid.addQty}>
          {(control) => (
            <Input {...control} inputMode="decimal" className={styles.narrow} value={v.add.qty} onChange={(e) => setV({ ...v, add: { ...v.add, qty: e.target.value } })} invalid={Boolean(invalid.addQty)} />
          )}
        </FormField>
        {invalid.form ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Changes not saved" live="alert">
              {invalid.form}
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save changes
          </Button>
        </div>
      </form>
    </Card>
  );
}
