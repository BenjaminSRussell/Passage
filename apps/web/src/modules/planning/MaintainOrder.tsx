import { useEffect, useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input } from "../../shell/ui";
import { deletePlannedOrder, isOpen, updatePlannedOrder, type PlannedOrder, type PlannedOrderPatch } from "./api/client";
import { describeError, type LoadError } from "./hooks";
import styles from "./pages.module.css";

const UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
/** A positive decimal with at most 6 places (the service's quantity precision). */
const QTY = /^(?:0|[1-9]\d{0,11})(?:\.\d{1,6})?$/;

export interface OrderDraft {
  qty: string;
  orderDate: string;
  deliveryDate: string;
  vendorId: string;
}

type DraftErrors = Partial<Record<keyof OrderDraft | "form", string>>;

const draftOf = (o: PlannedOrder): OrderDraft => ({ qty: o.qty, orderDate: o.orderDate, deliveryDate: o.deliveryDate, vendorId: o.vendorId ?? "" });

/**
 * Checks a planner's change against the order it starts from and returns the
 * PATCH body of the fields that changed, or the field errors.
 */
export function validateOrderChange(o: PlannedOrder, d: OrderDraft): { errors: DraftErrors; patch: PlannedOrderPatch } {
  const errors: DraftErrors = {};
  const patch: PlannedOrderPatch = {};
  const qty = d.qty.trim();
  if (!QTY.test(qty) || Number(qty) <= 0) errors.qty = "Enter a quantity above 0 with at most 6 decimals.";
  else if (Number(qty) !== Number(o.qty)) patch.qty = qty;
  if (!DATE.test(d.orderDate)) errors.orderDate = "Enter the order date.";
  else if (d.orderDate !== o.orderDate) patch.orderDate = d.orderDate;
  if (!DATE.test(d.deliveryDate)) errors.deliveryDate = "Enter the delivery date.";
  else if (d.deliveryDate !== o.deliveryDate) patch.deliveryDate = d.deliveryDate;
  if (!errors.orderDate && !errors.deliveryDate && d.orderDate > d.deliveryDate) {
    errors.orderDate = "The order date must not be after the delivery date.";
  }
  const vendor = d.vendorId.trim();
  if (o.orderType === "purchase" && vendor !== (o.vendorId ?? "")) {
    if (!UUID.test(vendor)) errors.vendorId = "Enter the vendor id as a UUID.";
    else patch.vendorId = vendor;
  }
  if (Object.keys(errors).length === 0 && Object.keys(patch).length === 0) errors.form = "Change at least one field before saving.";
  return { errors, patch };
}

interface Props {
  order: PlannedOrder;
  /** Called with the order the service returned after a change. */
  onChanged: (o: PlannedOrder) => void;
  onDeleted: () => void;
  /** Reloads the order, e.g. after a 412 because someone else changed it. */
  onReload: () => void;
}

type Busy = "save" | "approve" | "delete" | null;

/**
 * Planned-order maintenance (FR-22.11): change quantity, dates or vendor,
 * approve, or delete. Every write sends the order's version as If-Match.
 */
export function MaintainOrderCard({ order, onChanged, onDeleted, onReload }: Props) {
  const [draft, setDraft] = useState<OrderDraft>(() => draftOf(order));
  const [errors, setErrors] = useState<DraftErrors>({});
  const [failure, setFailure] = useState<LoadError | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState<Busy>(null);
  const [confirming, setConfirming] = useState(false);

  // A new version (saved here or reloaded) becomes the starting point of the form.
  useEffect(() => {
    setDraft(draftOf(order));
    setErrors({});
  }, [order.id, order.version]);

  if (order.status === "deleted") {
    return (
      <Alert tone="info" title="This planned order is deleted" live="off">
        It no longer appears in the planned orders of the plan.
      </Alert>
    );
  }
  if (!isOpen(order)) {
    return (
      <Alert tone="info" title="This planned order is firmed" live="off">
        Firmed orders are changed on the purchase, production or transfer order they became.
      </Alert>
    );
  }

  const set = (field: keyof OrderDraft) => (value: string) => setDraft((d) => ({ ...d, [field]: value }));

  async function act(kind: Exclude<Busy, null>, call: () => Promise<void>) {
    if (busy) return;
    setBusy(kind);
    setFailure(null);
    setDone(null);
    try {
      await call();
    } catch (err) {
      setFailure(describeError(err));
    } finally {
      setBusy(null);
    }
  }

  function save(e: FormEvent) {
    e.preventDefault();
    const { errors: found, patch } = validateOrderChange(order, draft);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void act("save", async () => {
      onChanged(await updatePlannedOrder(order.id, order.version, patch));
      setDone("Changes saved.");
    });
  }

  const approve = () =>
    act("approve", async () => {
      onChanged(await updatePlannedOrder(order.id, order.version, { status: "approved" }));
      setDone("Order approved. Firm it from the planned orders list.");
    });

  const remove = () =>
    act("delete", async () => {
      await deletePlannedOrder(order.id, order.version);
      setConfirming(false);
      onDeleted();
    });

  const stale = failure?.status === 412;
  return (
    <Card title="Maintain order">
      <div className={styles.stack}>
        <Alert
          tone="danger"
          title={failure ? (stale ? "Someone else changed this order" : "The change was not saved") : undefined}
          live="alert"
          action={
            stale ? (
              <Button size="sm" onClick={onReload}>
                Reload order
              </Button>
            ) : undefined
          }
        >
          {failure ? `${failure.message} (${failure.code})` : null}
        </Alert>
        <Alert tone="success" live="status">
          {done}
        </Alert>
        {confirming ? (
          <Alert
            tone="warning"
            title={`Delete planned order ${order.number}?`}
            live="alert"
            action={
              <div className={styles.inlineActions}>
                <Button variant="danger" size="sm" loading={busy === "delete"} onClick={remove}>
                  Delete order
                </Button>
                <Button size="sm" onClick={() => setConfirming(false)}>
                  Keep order
                </Button>
              </div>
            }
          >
            The next plan run proposes it again if the demand still needs it.
          </Alert>
        ) : null}
        <form className={styles.form} onSubmit={save} noValidate aria-label="Change planned order">
          <FormField id="order-qty" label="Quantity" error={errors.qty}>
            {(a11y) => <Input {...a11y} inputMode="decimal" value={draft.qty} onChange={(e) => set("qty")(e.target.value)} />}
          </FormField>
          <FormField id="order-date" label="Order date" error={errors.orderDate}>
            {(a11y) => <Input {...a11y} type="date" value={draft.orderDate} onChange={(e) => set("orderDate")(e.target.value)} />}
          </FormField>
          <FormField id="order-delivery" label="Delivery date" error={errors.deliveryDate}>
            {(a11y) => <Input {...a11y} type="date" value={draft.deliveryDate} onChange={(e) => set("deliveryDate")(e.target.value)} />}
          </FormField>
          {order.orderType === "purchase" ? (
            <FormField id="order-vendor" label="Vendor" hint="Vendor id (UUID) for the planned purchase." optional error={errors.vendorId}>
              {(a11y) => <Input {...a11y} value={draft.vendorId} onChange={(e) => set("vendorId")(e.target.value)} />}
            </FormField>
          ) : null}
          {errors.form ? (
            <p className={`${styles.wide} ${styles.formError}`} role="alert">
              {errors.form}
            </p>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy === "save"}>
              Save changes
            </Button>
            {order.status === "unprocessed" ? (
              <Button loading={busy === "approve"} onClick={approve}>
                Approve
              </Button>
            ) : null}
            <Button variant="danger" onClick={() => setConfirming(true)} disabled={confirming}>
              Delete
            </Button>
          </div>
        </form>
      </div>
    </Card>
  );
}
