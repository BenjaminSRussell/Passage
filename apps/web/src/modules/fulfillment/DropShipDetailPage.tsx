import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { cancelDropShip, closeDropShipRemainder, confirmDropShip, describeProblem, getDropShip, type DropShip, type DropShipConfirmation, type JournalEntry, type Problem } from "./api/client";
import { compareDecimal, dash, formatDate, formatDateTime, formatMoney, multiplyDecimal, subtractDecimal, sumDecimals, today } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import { DropShipStatusBadge } from "./DropShipsPage";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Section 5.28.5: the vendor confirmation is a purchasing duty (seeded as procurement.purchase-order.confirm). */
const DUTY_CONFIRM = "procurement.purchase-order.confirm";
const DUTY_RELEASE = "fulfillment.warehouse.release";

const DECIMAL = /^\d+(\.\d{1,6})?$/;

const confirmationColumns: DataTableColumn<DropShipConfirmation>[] = [
  { key: "seq", header: "#", align: "end" },
  { key: "qty", header: "Quantity", align: "end" },
  { key: "vendorShipmentRef", header: "Vendor reference", render: (row) => row.vendorShipmentRef ?? dash },
  { key: "shippedOn", header: "Shipped", render: (row) => formatDate(row.shippedOn) },
  { key: "postingDate", header: "Booked", render: (row) => formatDate(row.postingDate) },
  { key: "journal", header: "Booked amount", align: "end", render: (row) => (row.journal[0] ? formatMoney(row.journal[0].amount, row.journal[0].currency) : dash) },
];

const journalColumns: DataTableColumn<JournalEntry>[] = [
  { key: "account", header: "Account", mono: true },
  { key: "amount", header: "Amount", align: "end", render: (row) => formatMoney(row.amount, row.currency) },
];

/**
 * The amount a confirmation of `qty` books, as the service rounds it: the cost of
 * everything confirmed so far rounded once, less what earlier confirmations booked.
 */
function confirmationAmount(dropShip: DropShip, qty: string): string {
  const before = multiplyDecimal(dropShip.qtyConfirmed, dropShip.unitCost, 2);
  return subtractDecimal(multiplyDecimal(sumDecimals([dropShip.qtyConfirmed, qty], 6), dropShip.unitCost, 2), before);
}

/** A stale version or a drop-ship that is no longer open gets a message that says what to do. */
function explainDropShip(problem: Problem): Problem {
  if (problem.code === "conflict_version") return { ...problem, message: "Someone else changed this drop-ship. Reload it and try again." };
  if (problem.code === "FUL-0001") return { ...problem, message: "This drop-ship is not in a state that allows that; a vendor confirmation is booked and cannot be cancelled. Reload to see its current state." };
  if (problem.code === "FUL-0063") return { ...problem, message: "The quantity is more than is still open on the drop-ship. Reload to see what is open." };
  if (problem.code === "FUL-0064") return { ...problem, message: "That vendor shipment reference is already booked on this drop-ship." };
  return problem;
}

function ConfirmCard({ dropShip, onDone }: { dropShip: DropShip; onDone(): void }) {
  const [shippedOn, setShippedOn] = useState(today());
  const [postingDate, setPostingDate] = useState("");
  const [ref, setRef] = useState("");
  const [qty, setQty] = useState(dropShip.qtyOpen);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();
  const date = /^\d{4}-\d{2}-\d{2}$/;

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const found: Record<string, string> = {};
    if (!date.test(shippedOn)) found.shippedOn = "Enter the day the vendor shipped.";
    else if (shippedOn > today()) found.shippedOn = "The ship date cannot be in the future.";
    if (!DECIMAL.test(qty.trim()) || compareDecimal(qty.trim(), "0") <= 0) found.qty = "Enter a quantity above 0, with at most 6 decimals.";
    else if (compareDecimal(qty.trim(), dropShip.qtyOpen) > 0) found.qty = `Only ${dropShip.qtyOpen} ${dropShip.unit} are open.`;
    if (postingDate !== "" && !date.test(postingDate)) found.postingDate = "Enter a date, or leave it empty to book on the ship date.";
    if ([...ref].length > 64) found.ref = "Use at most 64 characters.";
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await confirmDropShip(dropShip.id, dropShip.version, { shippedOn, qty: qty.trim(), ...(ref.trim() ? { vendorShipmentRef: ref.trim() } : {}), ...(postingDate ? { postingDate } : {}) }, key.current());
      key.renew();
      onDone();
    } catch (error) {
      const problem = explainDropShip(describeProblem(error));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Vendor confirmation">
      <form className={styles.form} onSubmit={submit} noValidate>
        <p className={styles.note}>
          Confirming books Dr 5000 / Cr 2110 for {formatMoney(confirmationAmount(dropShip, DECIMAL.test(qty.trim()) ? qty.trim() : "0"), dropShip.currency)} at the purchase order cost. No stock moves.
          {" "}Confirm part of the {dropShip.qtyOpen} {dropShip.unit} still open if the vendor ships in parts.
        </p>
        <FormField id="ds-qty" label="Quantity shipped" error={errors.qty} hint={`Up to ${dropShip.qtyOpen} ${dropShip.unit}.`}>
          {(control) => <Input {...control} inputMode="decimal" value={qty} invalid={Boolean(errors.qty)} onChange={(e) => { setQty(e.target.value); key.renew(); }} />}
        </FormField>
        <FormField id="ds-shipped-on" label="Vendor shipped on" error={errors.shippedOn}>
          {(control) => <Input {...control} type="date" value={shippedOn} invalid={Boolean(errors.shippedOn)} onChange={(e) => { setShippedOn(e.target.value); key.renew(); }} />}
        </FormField>
        <FormField id="ds-posting-date" label="Booking date" error={errors.postingDate} hint="Leave empty to book on the ship date.">
          {(control) => <Input {...control} type="date" value={postingDate} invalid={Boolean(errors.postingDate)} onChange={(e) => { setPostingDate(e.target.value); key.renew(); }} />}
        </FormField>
        <FormField id="ds-ref" label="Vendor shipment reference" error={errors.ref}>
          {(control) => <Input {...control} value={ref} maxLength={80} invalid={Boolean(errors.ref)} onChange={(e) => { setRef(e.target.value); key.renew(); }} />}
        </FormField>
        <ProblemAlert title="Could not confirm the drop-ship" problem={failure} />
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Confirm shipment
          </Button>
        </div>
      </form>
    </Card>
  );
}

function ClosePanel({ dropShip, onDone }: { dropShip: DropShip; onDone(): void }) {
  const [confirming, setConfirming] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  async function close() {
    setBusy(true);
    setFailure(null);
    try {
      await closeDropShipRemainder(dropShip.id, dropShip.version, key.current());
      key.renew();
      setConfirming(false);
      onDone();
    } catch (error) {
      const problem = explainDropShip(describeProblem(error));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <ProblemAlert title="Could not close the remainder" problem={failure} />
      {confirming ? (
        <Alert tone="warning" title="Close the remainder?" live="status" action={<><Button size="sm" variant="danger" loading={busy} onClick={close}>Close remainder</Button><Button size="sm" variant="secondary" onClick={() => setConfirming(false)}>Keep it open</Button></>}>
          The vendor will not ship the {dropShip.qtyOpen} {dropShip.unit} still open. Nothing is booked for them and the drop-ship is confirmed as it stands.
        </Alert>
      ) : (
        <div className={styles.actions}>
          <Button variant="secondary" onClick={() => setConfirming(true)}>
            Close remainder
          </Button>
        </div>
      )}
    </div>
  );
}

function CancelPanel({ dropShip, onDone }: { dropShip: DropShip; onDone(): void }) {
  const [confirming, setConfirming] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  async function cancel() {
    setBusy(true);
    setFailure(null);
    try {
      await cancelDropShip(dropShip.id, dropShip.version, key.current());
      key.renew();
      setConfirming(false);
      onDone();
    } catch (error) {
      const problem = explainDropShip(describeProblem(error));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <ProblemAlert title="Could not cancel the drop-ship" problem={failure} />
      {confirming ? (
        <Alert tone="warning" title="Cancel this drop-ship?" live="status" action={<><Button size="sm" variant="danger" loading={busy} onClick={cancel}>Cancel drop-ship</Button><Button size="sm" variant="secondary" onClick={() => setConfirming(false)}>Keep it</Button></>}>
          Nothing has been booked. The order line can be drop-shipped again afterwards.
        </Alert>
      ) : (
        <div className={styles.actions}>
          <Button variant="secondary" onClick={() => setConfirming(true)}>
            Cancel drop-ship
          </Button>
        </div>
      )}
    </div>
  );
}

/** Drop-ship detail (FR-28.7): the PO link, the vendor confirmation form for an open one, and the journal once confirmed. */
export function DropShipDetailPage() {
  const { id = "" } = useParams();
  const duties = usePrincipalDuties();
  const state = useLoad((signal) => getDropShip(id, signal), id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Drop-ship" />
        <Alert tone="danger" title="Could not load the drop-ship" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Drop-ship" />
        <span className="visually-hidden">Loading the drop-ship…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const d = state.data;
  const open = d.status === "OPEN";
  const partial = d.status === "PARTIALLY_CONFIRMED";
  const canConfirm = holdsDuty(duties, DUTY_CONFIRM);
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Drop-ship ${d.dropShipNumber}`}
        description={`${d.qty} ${d.unit} at ${formatMoney(d.unitCost, d.currency)} each, shipped by the vendor to the customer`}
        meta={<DropShipStatusBadge status={d.status} />}
        actions={
          <Link className={buttonClassName("secondary")} to="/fulfillment/drop-ships">
            All drop-ships
          </Link>
        }
      />
      <Card>
        <dl className={styles.details}>
          {[
            ["Order", d.orderId],
            ["Order line", d.orderLineId],
            ["Customer", d.customerId],
            ["Product", d.productId],
            ["Vendor", d.vendorId],
            ["Purchase order", d.purchaseOrderId],
            ["Purchase order line", d.purchaseOrderLineId],
            ["Legal entity", d.legalEntityId],
            ["Confirmed by the vendor", `${d.qtyConfirmed} ${d.unit}`],
            ["Still open", `${d.qtyOpen} ${d.unit}`],
            ["Closed without shipping", `${d.qtyClosed} ${d.unit}`],
            ["Vendor shipment reference", d.vendorShipmentRef ?? dash],
            ["Shipped on", d.shippedOn ? formatDate(d.shippedOn) : dash],
            ["Booked on", d.postingDate ? formatDate(d.postingDate) : dash],
            ["Created", formatDateTime(d.createdAt)],
            ["Version", String(d.version)],
          ].map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={`${styles.value} ${styles.mono}`}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      {d.confirmations.length > 0 ? (
        <Card title="Vendor shipments">
          <DataTable label="Vendor shipments" columns={confirmationColumns} rows={d.confirmations} getRowKey={(row) => String(row.seq)} empty={{ title: "No vendor shipments", description: "The vendor has not shipped yet." }} />
        </Card>
      ) : null}
      {d.status === "CONFIRMED" || partial ? (
        <Card title="Journal">
          <DataTable label="Drop-ship journal" columns={journalColumns} rows={d.journal} getRowKey={(row) => `${row.account}-${d.journal.indexOf(row)}`} empty={{ title: "Nothing was booked", description: "The purchase order cost is zero." }} />
        </Card>
      ) : null}
      {(open || partial) && canConfirm ? <ConfirmCard key={d.version} dropShip={d} onDone={state.reload} /> : null}
      {(open || partial) && !canConfirm ? <p className={styles.note}>Confirming the vendor's shipment books a journal and needs the purchase order confirmation duty.</p> : null}
      {d.status === "CONFIRMED" ? <p className={styles.note}>The vendor shipment is booked. A confirmed drop-ship is final and cannot be cancelled.</p> : null}
      {partial ? <p className={styles.note}>Part of the vendor shipment is booked, so the drop-ship cannot be cancelled. Confirm the next shipment, or close the remainder if the vendor will not ship it.</p> : null}
      {partial && holdsDuty(duties, DUTY_RELEASE) ? <ClosePanel dropShip={d} onDone={state.reload} /> : null}
      {open && holdsDuty(duties, DUTY_RELEASE) ? <CancelPanel dropShip={d} onDone={state.reload} /> : null}
    </div>
  );
}
