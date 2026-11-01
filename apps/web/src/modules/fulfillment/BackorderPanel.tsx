// Shipment detail: move the open remainder of a partially shipped shipment to a
// new backorder shipment (FR-28.8, short pick action REMAINDER_BACKORDER).
import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, FormField, Input } from "../../shell/ui";
import { backorderShipmentRemainder, describeProblem, type Problem, type Shipment } from "./api/client";
import { explain, ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";

const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** The backorder button with its confirmation and an optional planned ship date; the service re-checks the rule. */
export function BackorderRemainderPanel({ shipment, onDone }: { shipment: Shipment; onDone(): void }) {
  const [open, setOpen] = useState(false);
  const [date, setDate] = useState("");
  const [dateError, setDateError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [created, setCreated] = useState<Shipment | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  async function move() {
    if (date !== "" && !DATE.test(date)) {
      setDateError("Enter the date as YYYY-MM-DD, or leave it empty for today.");
      return;
    }
    setDateError(undefined);
    setBusy(true);
    setFailure(null);
    try {
      const result = await backorderShipmentRemainder(shipment.id, shipment.version, date === "" ? {} : { plannedShipDate: date }, key.current());
      key.renew();
      setCreated(result.backorder);
      setOpen(false);
      onDone();
    } catch (error) {
      const problem = explain(describeProblem(error));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  if (created) {
    return (
      <Alert tone="success" title="Backorder shipment created" live="status">
        The remainder moved to <Link to={`/fulfillment/shipments/${created.id}`}>{created.shipmentNumber}</Link>, released and ready to pick.
      </Alert>
    );
  }
  if (!open) {
    return (
      <div className={styles.actions}>
        <Button variant="secondary" onClick={() => setOpen(true)}>
          Backorder remainder
        </Button>
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <Alert
        tone="warning"
        title="Move the remainder to a backorder shipment?"
        action={
          <span className={styles.actions}>
            <Button variant="primary" size="sm" loading={busy} onClick={move}>
              Yes, create the backorder
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setOpen(false)}>
              Keep it open
            </Button>
          </span>
        }
      >
        This shipment becomes Shipped at what has shipped, and the quantity still to ship moves to a new shipment of the same order. Picked stock that was never packed goes back to stock. This cannot be undone.
      </Alert>
      <FormField id="backorder-date" label="Planned ship date of the backorder" hint="Leave empty for today." error={dateError} optional>
        {(control) => <Input {...control} type="date" value={date} invalid={Boolean(dateError)} onChange={(event) => { setDate(event.target.value); key.renew(); }} />}
      </FormField>
      <ProblemAlert title="Could not create the backorder" problem={failure} />
    </div>
  );
}
