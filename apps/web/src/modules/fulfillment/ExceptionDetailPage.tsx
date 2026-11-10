import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, FormField, PageHeader, Skeleton, Textarea } from "../../shell/ui";
import { describeProblem, getException, resolveException, type Problem, type WarehouseException } from "./api/client";
import { ExceptionStatusBadge } from "./ExceptionsPage";
import { EXCEPTION_KIND_LABELS, formatDateTime } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** What the supervisor does about each kind of case. */
const ADVICE: Record<string, string> = {
  SLIP_POSTED_BEYOND_KEPT:
    "The goods have left the warehouse. Take them back through a customer return, or raise a new order for the customer, then record what you did.",
  PACKED_BEYOND_KEPT:
    "Posting this shipment is refused (FUL-0066) until it holds no more than the order line keeps. Unpack the containers on the shipment and put the goods back: unpacking cuts the quantity above what the order line keeps, so the rest can be repacked and posted. Then record what you did.",
  DROPSHIP_CONFIRMED_BEYOND_KEPT:
    "The vendor shipped, and the cost was booked, before sales cancelled the remainder. Nothing is reversed here: arrange a vendor return or raise a new order for the customer, then record what you did.",
};

export function validateNote(value: string): string | undefined {
  const v = value.trim();
  if (v === "") return "Say what was done about it.";
  if ([...v].length > 500) return "Use at most 500 characters.";
  return undefined;
}

function ResolveCard({ exception, onDone }: { exception: WarehouseException; onDone(): void }) {
  const [note, setNote] = useState("");
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateNote(note);
    setError(found);
    setFailure(null);
    if (found) return;
    setBusy(true);
    try {
      await resolveException(exception.id, exception.version, note.trim(), key.current());
      key.renew();
      onDone();
    } catch (e) {
      const problem = describeProblem(e);
      setFailure(problem.code === "conflict_version" ? { ...problem, message: "Someone else changed this exception. Reload it and try again." } : problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Resolve the exception">
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="exc-note" label="What was done" error={error}>
          {(control) => (
            <Textarea
              {...control}
              value={note}
              invalid={Boolean(error)}
              maxLength={500}
              onChange={(event) => {
                setNote(event.target.value);
                key.renew();
              }}
            />
          )}
        </FormField>
        <ProblemAlert title="Could not resolve the exception" problem={failure} />
        <div className={styles.actions}>
          <Button type="submit" disabled={busy}>
            {busy ? "Saving…" : "Mark resolved"}
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** One exception with what to do about it, and the form that closes it (fulfillment.warehouse.release). */
export function ExceptionDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getException(id, signal), id);
  const canResolve = holdsDuty(usePrincipalDuties(), "fulfillment.warehouse.release");

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Warehouse exception" />
        <Alert tone="danger" title="Could not load the exception" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Warehouse exception" />
        <span className="visually-hidden">Loading the exception…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const e = state.data;
  const rows: [string, string, boolean][] = [
    ["Order", e.orderId, true],
    ["Order line", e.orderLineId, true],
    ["Quantity beyond", e.qty, false],
    ["Order line keeps", e.keptQty, false],
    ["Raised", formatDateTime(e.createdAt), false],
    ...(e.resolvedAt ? ([["Resolved", `${formatDateTime(e.resolvedAt)} by ${e.resolvedBy ?? "unknown"}`, false]] as [string, string, boolean][]) : []),
    ...(e.resolutionNote ? ([["Resolution", e.resolutionNote, false]] as [string, string, boolean][]) : []),
  ];
  return (
    <div className={styles.stack}>
      <PageHeader
        title={EXCEPTION_KIND_LABELS[e.kind] ?? e.kind}
        description={e.detail}
        actions={
          <span className={styles.actions}>
            <ExceptionStatusBadge status={e.status} />
            <Link className={buttonClassName("secondary")} to="/fulfillment/exceptions">
              All exceptions
            </Link>
          </span>
        }
      />
      <Card>
        <dl className={styles.details}>
          {rows.map(([term, value, mono]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
        <p className={styles.note}>{ADVICE[e.kind]}</p>
        <div className={styles.actions}>
          {e.shipmentId ? (
            <Link className={buttonClassName("secondary")} to={`/fulfillment/shipments/${e.shipmentId}`}>
              Open the shipment
            </Link>
          ) : null}
          {e.packingSlipId ? (
            <Link className={buttonClassName("secondary")} to={`/fulfillment/packing-slips/${e.packingSlipId}`}>
              Open the packing slip
            </Link>
          ) : null}
        </div>
      </Card>
      {e.status === "OPEN" && canResolve ? <ResolveCard key={e.version} exception={e} onDone={state.reload} /> : null}
    </div>
  );
}
