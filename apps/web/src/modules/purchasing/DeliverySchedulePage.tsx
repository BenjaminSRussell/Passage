import { useEffect, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, buttonClassName, type DataTableColumn } from "../../shell/ui";
import {
  ProcurementApiError,
  confirmDeliveryDate,
  deleteDeliverySchedule,
  describeProblem,
  getDeliverySchedule,
  getPurchaseOrder,
  putDeliverySchedule,
  type DeliverySchedule,
  type DeliveryScheduleInput,
  type DeliveryScheduleRow,
  type PurchaseOrderLine,
} from "./api/client";
import { Details, LoadError, ShortId, StatusBadge, datePattern, dash, decimalPattern, useResource, type Failure } from "./parts";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

export interface ScheduleRowDraft {
  key: string;
  qty: string;
  date: string;
}

let rowSeq = 0;
const emptyRow = (): ScheduleRowDraft => ({ key: `r${++rowSeq}`, qty: "", date: "" });

export function emptyScheduleDraft(): ScheduleRowDraft[] {
  return [emptyRow()];
}

/** A draft that shows an existing schedule, so replacing it starts from what is there. */
export function draftFromSchedule(schedule: DeliverySchedule): ScheduleRowDraft[] {
  return schedule.rows.map((r) => ({ key: `r${++rowSeq}`, qty: r.qty, date: r.deliveryDate }));
}

const SCALE = 6;
const MAX_ROWS = 100;

/** A decimal string as an integer count of millionths, so sums are exact; null when it is not a decimal. */
export function toMicro(value: string): bigint | null {
  const v = value.trim();
  if (!decimalPattern.test(v)) return null;
  const [whole, frac = ""] = v.split(".");
  if (frac.length > SCALE) return null;
  return BigInt(whole) * 10n ** BigInt(SCALE) + BigInt(frac.padEnd(SCALE, "0"));
}

/** Millionths back to a decimal string without trailing zeros. */
export function fromMicro(value: bigint): string {
  const negative = value < 0n;
  const abs = negative ? -value : value;
  const whole = abs / 10n ** BigInt(SCALE);
  const frac = (abs % 10n ** BigInt(SCALE)).toString().padStart(SCALE, "0").replace(/0+$/, "");
  return `${negative ? "-" : ""}${whole}${frac ? `.${frac}` : ""}`;
}

/** The quantity still to be scheduled: line quantity minus the valid rows typed so far. */
export function unscheduled(lineQty: string, rows: ScheduleRowDraft[]): string | null {
  const total = toMicro(lineQty);
  if (total === null) return null;
  let sum = 0n;
  for (const r of rows) sum += toMicro(r.qty) ?? 0n;
  return fromMicro(total - sum);
}

/**
 * Checks the form's shape and builds the PUT body. The quantities are decimal
 * strings end to end; the sum is compared in integer millionths here only to
 * save a round trip, and the service's check (PRC-DS-001) is the one that counts.
 */
export function toScheduleInput(rows: ScheduleRowDraft[], lineQty: string): { input?: DeliveryScheduleInput; errors: Errors } {
  const errors: Errors = {};
  if (rows.length === 0) errors.rows = "Add at least one delivery.";
  rows.forEach((r, i) => {
    const micro = toMicro(r.qty);
    if (micro === null || micro <= 0n) errors[`rows[${i}].qty`] = "Enter a quantity above 0 with at most six decimal places.";
    if (!datePattern.test(r.date)) errors[`rows[${i}].deliveryDate`] = "Enter a delivery date.";
  });
  if (Object.keys(errors).length === 0 && unscheduled(lineQty, rows) !== "0") {
    errors.rows = `The deliveries must add up to the line quantity, ${lineQty}.`;
  }
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { rows: rows.map((r) => ({ qty: r.qty.trim(), deliveryDate: r.date })) } };
}

/** Loads the schedule of one line; a line without one is null, not an error. */
async function loadSchedule(poId: string, lineNo: number, signal: AbortSignal) {
  try {
    return (await getDeliverySchedule(poId, lineNo, signal)).body;
  } catch (error) {
    if (error instanceof ProcurementApiError && error.problem.code === "PRC-DS-NOT-FOUND") return null;
    throw error;
  }
}

const rowColumns = (unit: string): DataTableColumn<DeliveryScheduleRow>[] => [
  { key: "seq", header: "Delivery", align: "end" },
  { key: "qty", header: "Quantity", align: "end", render: (r) => `${r.qty} ${unit}` },
  { key: "deliveryDate", header: "Requested", mono: true },
  { key: "confirmedDate", header: "Vendor confirmed", mono: true, render: (r) => r.confirmedDate ?? dash },
];

/**
 * The delivery schedule of one PO line (FR-17.10): its rows, a form that
 * replaces them while the order is a draft, removal, and the vendor's
 * confirmed dates once the order is confirmed. Each row later becomes its own
 * ORDERED inventory transaction.
 */
export function DeliverySchedulePage() {
  const { id = "", lineNo: rawLineNo = "" } = useParams();
  const lineNo = Number(rawLineNo);
  const po = useResource((signal) => getPurchaseOrder(id, signal), [id]);
  const loaded = useResource((signal) => loadSchedule(id, lineNo, signal), [id, lineNo]);
  const [schedule, setSchedule] = useState<DeliverySchedule | null | undefined>(undefined);
  const [rowVersion, setRowVersion] = useState<number | null>(null);
  const [draft, setDraft] = useState<ScheduleRowDraft[]>(emptyScheduleDraft);
  const [dates, setDates] = useState<Record<number, string>>({});
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);

  useEffect(() => {
    setRowVersion(null);
  }, [id, lineNo]);
  useEffect(() => {
    if (loaded.data !== null && loaded.data !== undefined) {
      setSchedule(loaded.data);
      setRowVersion(loaded.data.poRowVersion);
      setDraft(draftFromSchedule(loaded.data));
    } else if (!loaded.loading && !loaded.error) {
      setSchedule(null);
    }
  }, [loaded.data, loaded.loading, loaded.error]);
  useEffect(() => {
    const poRowVersion = po.data?.body.rowVersion;
    if (poRowVersion !== undefined) setRowVersion((current) => current ?? poRowVersion);
  }, [po.data]);

  const order = po.data?.body ?? null;
  const line: PurchaseOrderLine | undefined = order?.lines.find((l) => l.lineNo === lineNo);
  const setRow = (i: number, patch: Partial<ScheduleRowDraft>) => setDraft((d) => d.map((r, j) => (j === i ? { ...r, ...patch } : r)));

  function fail(error: unknown) {
    const problem = describeProblem(error);
    setFailure(problem);
    if (problem.errors) setErrors(problem.errors);
  }

  async function onSave(event: FormEvent) {
    event.preventDefault();
    if (!line) return;
    const built = toScheduleInput(draft, line.qty);
    setErrors(built.errors);
    setFailure(null);
    setNotice(null);
    if (!built.input || rowVersion === null) return;
    setBusy("save");
    try {
      const { body } = await putDeliverySchedule(id, lineNo, rowVersion, built.input);
      setSchedule(body);
      setRowVersion(body.poRowVersion);
      setDraft(draftFromSchedule(body));
      setNotice(`Delivery schedule saved: ${body.rows.length} deliver${body.rows.length === 1 ? "y" : "ies"} adding up to ${body.lineQty} ${body.unit}.`);
    } catch (error) {
      fail(error);
    } finally {
      setBusy(null);
    }
  }

  async function onDelete() {
    if (rowVersion === null || !schedule) return;
    setBusy("delete");
    setFailure(null);
    setNotice(null);
    try {
      setRowVersion(await deleteDeliverySchedule(id, lineNo, rowVersion));
      setSchedule(null);
      setDraft(emptyScheduleDraft());
      setConfirmDelete(false);
      setNotice("Delivery schedule removed.");
    } catch (error) {
      fail(error);
    } finally {
      setBusy(null);
    }
  }

  async function onConfirmDate(row: DeliveryScheduleRow) {
    const date = dates[row.seq] ?? row.confirmedDate ?? row.deliveryDate;
    if (rowVersion === null) return;
    if (!datePattern.test(date)) {
      setErrors({ [`confirm-${row.seq}`]: "Enter the confirmed date." });
      return;
    }
    setErrors({});
    setBusy(`confirm-${row.seq}`);
    setFailure(null);
    setNotice(null);
    try {
      const { body } = await confirmDeliveryDate(id, lineNo, row.seq, rowVersion, date);
      setSchedule(body);
      setRowVersion(body.poRowVersion);
      setNotice(`Delivery ${row.seq} confirmed for ${date}.`);
    } catch (error) {
      fail(error);
    } finally {
      setBusy(null);
    }
  }

  const title = order ? `Delivery schedule: ${order.number} line ${lineNo}` : "Delivery schedule";
  const back = (
    <Link className={buttonClassName("secondary")} to={`/purchasing/purchase-orders/${id}`}>
      Back to the purchase order
    </Link>
  );
  if (po.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Delivery schedule" />
        {po.error.code === "PRC-PO-NOT-FOUND" ? (
          <Card>
            <EmptyState icon="receipt" title="Purchase order not found" description="It may belong to another legal entity, or the link is wrong." />
          </Card>
        ) : (
          <LoadError what="the purchase order" error={po.error} onRetry={po.reload} />
        )}
      </div>
    );
  }
  if (!order || schedule === undefined) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Delivery schedule" />
        {loaded.error ? <LoadError what="the delivery schedule" error={loaded.error} onRetry={loaded.reload} /> : null}
        <p className={styles.muted} role="status">
          Loading delivery schedule…
        </p>
      </div>
    );
  }
  if (!line) {
    return (
      <div className={styles.stack}>
        <PageHeader title={title} actions={back} />
        <Card>
          <EmptyState icon="receipt" title={`Line ${rawLineNo} not found`} description={`${order.number} has no line with that number.`} />
        </Card>
      </div>
    );
  }

  const editable = (order.approvalStatus === "DRAFT" || order.approvalStatus === "REJECTED") && line.status !== "CANCELLED";
  const confirmable = order.approvalStatus === "CONFIRMED" && line.status !== "CANCELLED";
  const left = unscheduled(line.qty, draft);
  return (
    <div className={styles.stack}>
      <PageHeader
        title={title}
        description="Split this line into deliveries. The quantities always add up to the line quantity, and each delivery becomes its own expected receipt when the order is confirmed."
        actions={back}
      />
      <Alert tone="danger" live="alert">
        {failure
          ? failure.code === "PRC-PO-009"
            ? <ProblemMessage problem={failure} message="Someone else changed this order. Reload the page and try again." />
            : <ProblemMessage problem={failure} />
          : null}
      </Alert>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      <Card title="Line">
        <Details
          items={[
            { label: "Item", value: line.description || <ShortId id={line.productId} /> },
            { label: "Quantity", value: `${line.qty} ${line.unit}`, mono: true },
            { label: "Line delivery date", value: line.deliveryDate, mono: true },
            { label: "Line status", value: <StatusBadge status={line.status} /> },
            { label: "Order status", value: <StatusBadge status={order.approvalStatus} /> },
          ]}
        />
      </Card>
      <Card
        title="Deliveries"
        actions={
          schedule && editable ? (
            <Button size="sm" variant="ghost" onClick={() => setConfirmDelete(true)}>
              Remove schedule
            </Button>
          ) : undefined
        }
      >
        {confirmDelete ? (
          <Alert
            tone="warning"
            title="Remove this delivery schedule?"
            action={
              <div className={styles.actions}>
                <Button size="sm" variant="danger" loading={busy === "delete"} onClick={() => void onDelete()}>
                  Yes, remove it
                </Button>
                <Button size="sm" variant="ghost" disabled={busy === "delete"} onClick={() => setConfirmDelete(false)}>
                  Keep it
                </Button>
              </div>
            }
          >
            The whole line is then expected on its own delivery date.
          </Alert>
        ) : null}
        {schedule ? (
          <DataTable label="Delivery rows" columns={rowColumns(schedule.unit)} rows={schedule.rows} getRowKey={(r) => String(r.seq)} />
        ) : (
          <EmptyState
            icon="receipt"
            title="No delivery schedule"
            description={`The whole line is expected on ${line.deliveryDate}. Split it into several deliveries below.`}
            compact
          />
        )}
      </Card>
      {confirmable && schedule ? (
        <Card title="Vendor confirmed dates">
          <div className={styles.form}>
            {schedule.rows.map((r) => (
              <fieldset key={r.seq} className={styles.lineCard} aria-label={`Delivery ${r.seq}`}>
                <div className={styles.lineHead}>
                  <p className={styles.lineTitle}>
                    Delivery {r.seq}: {r.qty} {schedule.unit}, requested {r.deliveryDate}
                  </p>
                </div>
                <div className={styles.grid}>
                  <FormField id={`confirm-${r.seq}`} label="Confirmed date" error={errors[`confirm-${r.seq}`]}>
                    {(c) => (
                      <Input
                        {...c}
                        type="date"
                        value={dates[r.seq] ?? r.confirmedDate ?? r.deliveryDate}
                        onChange={(e) => setDates((d) => ({ ...d, [r.seq]: e.target.value }))}
                      />
                    )}
                  </FormField>
                </div>
                <div className={styles.actions}>
                  <Button size="sm" loading={busy === `confirm-${r.seq}`} onClick={() => void onConfirmDate(r)}>
                    Confirm delivery {r.seq}
                  </Button>
                </div>
              </fieldset>
            ))}
          </div>
        </Card>
      ) : null}
      {!editable ? (
        <Alert tone="info" title="The deliveries are fixed">
          {line.status === "CANCELLED"
            ? "A cancelled line keeps no delivery schedule."
            : "Quantities and dates of an order that is in review, approved, confirmed, finalized or cancelled change through the order's change management, not here."}
        </Alert>
      ) : (
        <form className={styles.form} onSubmit={onSave} noValidate>
          <Card title={schedule ? "Replace the schedule" : "Set up a schedule"}>
            <div className={styles.form}>
              {draft.map((r, i) => (
                <fieldset key={r.key} className={styles.lineCard} aria-label={`Delivery ${i + 1}`}>
                  <div className={styles.lineHead}>
                    <p className={styles.lineTitle}>Delivery {i + 1}</p>
                    {draft.length > 1 ? (
                      <Button size="sm" variant="ghost" onClick={() => setDraft(draft.filter((_, j) => j !== i))}>
                        Remove delivery {i + 1}
                      </Button>
                    ) : null}
                  </div>
                  <div className={styles.grid}>
                    <FormField id={`delivery-${i}-qty`} label={`Quantity (${line.unit})`} error={errors[`rows[${i}].qty`]}>
                      {(c) => <Input {...c} inputMode="decimal" value={r.qty} onChange={(e) => setRow(i, { qty: e.target.value })} />}
                    </FormField>
                    <FormField id={`delivery-${i}-date`} label="Delivery date" error={errors[`rows[${i}].deliveryDate`]}>
                      {(c) => <Input {...c} type="date" value={r.date} onChange={(e) => setRow(i, { date: e.target.value })} />}
                    </FormField>
                  </div>
                </fieldset>
              ))}
              <div className={styles.actions}>
                <Button size="sm" disabled={draft.length >= MAX_ROWS} onClick={() => setDraft([...draft, emptyRow()])}>
                  Add delivery
                </Button>
              </div>
              <p className={styles.muted} role="status">
                {left === "0" ? `All ${line.qty} ${line.unit} are scheduled.` : `Still to schedule: ${left} of ${line.qty} ${line.unit}.`}
              </p>
              {errors.rows ? (
                <p className={styles.muted} role="alert">
                  {errors.rows}
                </p>
              ) : null}
            </div>
          </Card>
          <div className={styles.actions}>
            <Button type="submit" variant="primary" loading={busy === "save"}>
              {schedule ? "Replace schedule" : "Save schedule"}
            </Button>
          </div>
        </form>
      )}
    </div>
  );
}
