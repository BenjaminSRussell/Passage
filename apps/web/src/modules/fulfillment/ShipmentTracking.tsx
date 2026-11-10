// Shipment detail additions of slice 3: cancel a shipment (BR-28.6), carrier
// tracking with manual entry (FR-28.10, BR-28.9) and the GS1-128 container
// label as ZPL (FR-28.3).
import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, Select, type DataTableColumn } from "../../shell/ui";
import {
  cancelShipment,
  closeShipmentRemainder,
  containerLabel,
  describeProblem,
  listTracking,
  recordTracking,
  unpackContainer,
  TRACKING_STATUSES,
  type Container,
  type Problem,
  type Shipment,
  type TrackingEvent,
  type TrackingStatus,
} from "./api/client";
import { compareDecimal, formatDateTime, TRACKING_LABELS } from "./format";
import { explain, ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const CANCELLABLE: Shipment["status"][] = ["OPEN", "RELEASED", "PICKING", "PICKED", "PACKED"];

/** BR-28.6: a shipment cancels while nothing has shipped and it holds no container. */
export function canCancelShipment(shipment: Shipment): boolean {
  return (
    CANCELLABLE.includes(shipment.status) &&
    shipment.containers.length === 0 &&
    shipment.lines.every((l) => compareDecimal(l.qtyShipped, "0") === 0)
  );
}

/** The cancel button with its confirmation; the service re-checks BR-28.6. */
export function CancelShipmentPanel({ shipment, onDone }: { shipment: Shipment; onDone(): void }) {
  const [confirming, setConfirming] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  async function cancel() {
    setBusy(true);
    setFailure(null);
    try {
      await cancelShipment(shipment.id, shipment.version, key.current());
      key.renew();
      setConfirming(false);
      onDone();
    } catch (error) {
      const problem = explain(describeProblem(error));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  if (!confirming) {
    return (
      <div className={styles.actions}>
        <Button variant="danger" onClick={() => setConfirming(true)}>
          Cancel shipment
        </Button>
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <Alert
        tone="warning"
        title="Cancel this shipment?"
        action={
          <span className={styles.actions}>
            <Button variant="danger" size="sm" loading={busy} onClick={cancel}>
              Yes, cancel it
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setConfirming(false)}>
              Keep it
            </Button>
          </span>
        }
      >
        Its order lines can be released again, and inventory releases their reservations.
      </Alert>
      <ProblemAlert title="Could not cancel the shipment" problem={failure} />
    </div>
  );
}

const CLOSABLE: Shipment["status"][] = ["PARTIALLY_SHIPPED", "PICKING", "PICKED"];

/** FR-28.8: a shipment that shipped part of its quantity, owes the rest and has nothing packed waiting can give the remainder up. */
export function canCloseRemainder(shipment: Shipment): boolean {
  return (
    CLOSABLE.includes(shipment.status) &&
    shipment.lines.some((l) => compareDecimal(l.qtyShipped, "0") > 0) &&
    shipment.lines.some((l) => compareDecimal(l.qtyPlanned, l.qtyShipped) > 0) &&
    shipment.lines.every((l) => compareDecimal(l.qtyPacked, l.qtyShipped) <= 0)
  );
}

/** The close-remainder button with its confirmation; the service re-checks the rule. */
export function CloseRemainderPanel({ shipment, onDone }: { shipment: Shipment; onDone(): void }) {
  const [confirming, setConfirming] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  async function close() {
    setBusy(true);
    setFailure(null);
    try {
      await closeShipmentRemainder(shipment.id, shipment.version, key.current());
      key.renew();
      setConfirming(false);
      onDone();
    } catch (error) {
      const problem = explain(describeProblem(error));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  if (!confirming) {
    return (
      <div className={styles.actions}>
        <Button variant="secondary" onClick={() => setConfirming(true)}>
          Close remainder
        </Button>
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <Alert
        tone="warning"
        title="Close the remainder of this shipment?"
        action={
          <span className={styles.actions}>
            <Button variant="danger" size="sm" loading={busy} onClick={close}>
              Yes, close it
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setConfirming(false)}>
              Keep it open
            </Button>
          </span>
        }
      >
        The quantity still to ship is given up and the shipment becomes Shipped at what has shipped. Picked stock that was never packed goes back to stock. This cannot be undone.
      </Alert>
      <ProblemAlert title="Could not close the remainder" problem={failure} />
    </div>
  );
}

const trackingColumns: DataTableColumn<TrackingEvent>[] = [
  { key: "eventTime", header: "When", render: (row) => formatDateTime(row.eventTime) },
  { key: "status", header: "Status", render: (row) => TRACKING_LABELS[row.status] ?? row.status },
  { key: "trackingNumber", header: "Tracking number", mono: true },
  { key: "provider", header: "Carrier" },
  { key: "description", header: "Description" },
];

const PROVIDER = /^[a-z0-9-]{2,32}$/;

function localNow(): string {
  const d = new Date();
  d.setSeconds(0, 0);
  return new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
}

/** Tracking events of the shipment, newest first, and a form to enter one by hand. */
export function TrackingCard({ shipment, onRecorded }: { shipment: Shipment; onRecorded(): void }) {
  const state = useLoad((signal) => listTracking(shipment.id, signal), `${shipment.id}-${shipment.version}`);
  const [values, setValues] = useState({ provider: "manual", trackingNumber: "", status: "IN_TRANSIT" as TrackingStatus, description: "", eventTime: localNow() });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  function update(patch: Partial<typeof values>) {
    setValues({ ...values, ...patch });
    key.renew();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!PROVIDER.test(values.provider.trim())) found.provider = "Use 2 to 32 lowercase letters, digits or hyphens, such as ups or manual.";
    if (values.trackingNumber.trim() === "" || values.trackingNumber.trim().length > 64) found.trackingNumber = "Enter the tracking number (at most 64 characters).";
    if (values.description.length > 500) found.description = "Keep the description to 500 characters.";
    const when = new Date(values.eventTime);
    if (values.eventTime === "" || Number.isNaN(when.getTime())) found.eventTime = "Enter when the event happened.";
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const res = await recordTracking(shipment.id, {
        provider: values.provider.trim(),
        // The event key makes a resend of the same form harmless (one event per key).
        eventKey: `manual-${key.current()}`,
        trackingNumber: values.trackingNumber.trim(),
        status: values.status,
        description: values.description.trim(),
        eventTime: when.toISOString(),
      });
      key.renew();
      setNotice(res.created ? "Tracking event recorded." : "This tracking event was already recorded.");
      state.reload();
      onRecorded();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Tracking">
      <div className={styles.stack}>
        {state.error ? (
          <Alert tone="danger" title="Could not load the tracking events" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            <ProblemMessage problem={state.error} />
          </Alert>
        ) : (
          <DataTable
            label="Tracking events"
            columns={trackingColumns}
            rows={state.data ?? []}
            getRowKey={(row) => `${row.provider}-${row.eventKey}`}
            loading={state.loading}
            empty={{ title: "No tracking yet", description: "Carrier events appear here once the carrier reports them, or enter one below." }}
          />
        )}
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Add a tracking event">
          <FormField id="tracking-provider" label="Carrier" hint="Lowercase provider code; manual for a hand-entered event." error={errors.provider}>
            {(control) => <Input {...control} maxLength={32} value={values.provider} invalid={Boolean(errors.provider)} onChange={(event) => update({ provider: event.target.value })} />}
          </FormField>
          <FormField id="tracking-number" label="Tracking number" error={errors.trackingNumber}>
            {(control) => <Input {...control} maxLength={64} value={values.trackingNumber} invalid={Boolean(errors.trackingNumber)} onChange={(event) => update({ trackingNumber: event.target.value })} />}
          </FormField>
          <FormField id="tracking-status" label="Status">
            {(control) => (
              <Select {...control} value={values.status} onChange={(event) => update({ status: event.target.value as TrackingStatus })}>
                {TRACKING_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {TRACKING_LABELS[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="tracking-time" label="Event time" error={errors.eventTime}>
            {(control) => <Input {...control} type="datetime-local" value={values.eventTime} invalid={Boolean(errors.eventTime)} onChange={(event) => update({ eventTime: event.target.value })} />}
          </FormField>
          <FormField id="tracking-description" label="Description" error={errors.description} optional>
            {(control) => <Input {...control} maxLength={500} value={values.description} invalid={Boolean(errors.description)} onChange={(event) => update({ description: event.target.value })} />}
          </FormField>
          <ProblemAlert title="Could not record the tracking event" problem={failure} />
          <div className={styles.formActions}>
            <Button type="submit" loading={busy}>
              Add tracking event
            </Button>
            {notice ? (
              <p role="status" className={styles.note}>
                {notice}
              </p>
            ) : null}
          </div>
        </form>
      </div>
    </Card>
  );
}

/** The GS1-128 label of one container as ZPL, to send to a label printer. */
export function LabelButton({ shipmentId, container, onLoaded }: { shipmentId: string; container: Container; onLoaded(sscc: string, zpl: string | Problem): void }) {
  const [busy, setBusy] = useState(false);
  async function load() {
    setBusy(true);
    try {
      onLoaded(container.sscc, await containerLabel(shipmentId, container.id));
    } catch (error) {
      onLoaded(container.sscc, describeProblem(error));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Button size="sm" variant="ghost" loading={busy} onClick={load} aria-label={`ZPL label ${container.sscc}`}>
      ZPL label
    </Button>
  );
}

const UNPACKABLE: Shipment["status"][] = ["PICKING", "PICKED", "PACKED"];

/** A CLOSED container of a shipment that has not posted it yet can be taken apart. */
export function canUnpack(shipment: Shipment, container: Container): boolean {
  const nested = Boolean(container.parentContainerId) || (container.childContainerIds ?? []).length > 0;
  return UNPACKABLE.includes(shipment.status) && container.status === "CLOSED" && !nested;
}

/** Unpack button with an inline confirmation; the service re-checks the rule. */
export function UnpackButton({ shipment, container, onDone }: { shipment: Shipment; container: Container; onDone(): void }) {
  const [confirming, setConfirming] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  async function unpack() {
    setBusy(true);
    setFailure(null);
    try {
      await unpackContainer(shipment.id, container.id, shipment.version, key.current());
      key.renew();
      onDone();
    } catch (error) {
      const problem = explain(describeProblem(error));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  if (!confirming) {
    return (
      <Button size="sm" variant="ghost" onClick={() => setConfirming(true)} aria-label={`Unpack ${container.sscc}`}>
        Unpack
      </Button>
    );
  }
  return (
    <span className={styles.stack}>
      <span className={styles.actions}>
        <Button size="sm" variant="danger" loading={busy} onClick={unpack} aria-label={`Confirm unpack ${container.sscc}`}>
          Unpack it
        </Button>
        <Button size="sm" variant="ghost" onClick={() => setConfirming(false)}>
          Keep it
        </Button>
      </span>
      <ProblemAlert title="Could not unpack the container" problem={failure} />
    </span>
  );
}

/** Shows a loaded label program, or why it could not be loaded. */
export function LabelPanel({ sscc, label, onClose }: { sscc: string; label: string | Problem; onClose(): void }) {
  return (
    <Card title={`Label ${sscc}`} actions={<Button size="sm" variant="ghost" onClick={onClose}>Close label</Button>}>
      {typeof label === "string" ? (
        <div className={styles.stack}>
          <p className={styles.note}>GS1-128 with AI (00) and the SSCC. Send this ZPL program to the label printer.</p>
          <pre className={styles.file} aria-label={`ZPL program ${sscc}`}>
            {label}
          </pre>
        </div>
      ) : (
        <Alert tone="danger" title="Could not load the label" live="alert">
          <ProblemMessage problem={label} />
        </Alert>
      )}
    </Card>
  );
}
