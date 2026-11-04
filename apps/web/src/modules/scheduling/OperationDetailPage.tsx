import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, FormField, Input, PageHeader, Skeleton, type BadgeTone } from "../../shell/ui";
import {
  deleteOperation,
  describeProblem,
  getOperation,
  listResources,
  newKey,
  unscheduleOrder,
  updateOperation,
  type Operation,
  type OperationPatch,
  type OperationStatus,
  type OperationUpdate,
  type OrderUnschedule,
  type Problem,
} from "./api/client";
import { fromUtcInput, toUtcInput, dash, formatDateTime, formatMinutes, sentence, shortId } from "./format";
import { compareDecimal, decimalText, optionalWhole, positive, trimDecimal, wholeNumber, WEEK_MINUTES } from "./forms";
import { describeAttributes } from "./OperationsPage";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const statusTone: Record<OperationStatus, BadgeTone> = { open: "neutral", scheduled: "success", unplaced: "warning" };

export interface OperationEditValues {
  minutes: string;
  /** datetime-local value read as UTC; blank leaves the date as it is. */
  dueAt: string;
  readyAt: string;
  priority: string;
  setupMinutes: string;
  queueBeforeMinutes: string;
  queueAfterMinutes: string;
  /** Decimal text; blank means none. A quantity cannot be removed once set. */
  quantity: string;
  overlapQuantity: string;
  /** Candidate resource codes in priority order, separated by commas. */
  candidates: string;
}

type Field = "minutes" | "dueAt" | "readyAt" | "priority" | "setupMinutes" | "queueBeforeMinutes" | "queueAfterMinutes" | "quantity" | "overlapQuantity" | "candidates" | "form";
type Errors = Partial<Record<Field, string>>;

export function initialValues(op: Operation, codes: Record<string, string>): OperationEditValues {
  return {
    minutes: String(op.minutes),
    dueAt: op.dueAt ? toUtcInput(op.dueAt) : "",
    readyAt: op.readyAt ? toUtcInput(op.readyAt) : "",
    priority: String(op.priority),
    setupMinutes: String(op.setupMinutes),
    queueBeforeMinutes: String(op.queueBeforeMinutes ?? 0),
    queueAfterMinutes: String(op.queueAfterMinutes ?? 0),
    quantity: op.quantity ? trimDecimal(op.quantity) : "",
    overlapQuantity: op.overlapQuantity ? trimDecimal(op.overlapQuantity) : "",
    candidates: op.resourceIds.map((id) => codes[id] ?? id).join(", "),
  };
}

/** Checks that mirror the service, and the patch the form sends: only what changed. */
export function validateOperationEdit(values: OperationEditValues, current: Operation, idsByCode: Record<string, string>): { errors: Errors; patch?: OperationPatch } {
  const errors: Errors = {};
  const minutes = wholeNumber(values.minutes, 1, 100_000);
  if (minutes === null) errors.minutes = "Enter whole minutes from 1 to 100000.";
  const priority = wholeNumber(values.priority, 0, 1000);
  if (priority === null) errors.priority = "Enter a whole number from 0 to 1000.";
  const setupMinutes = optionalWhole(values.setupMinutes, 0, WEEK_MINUTES);
  if (setupMinutes === null || setupMinutes === undefined) errors.setupMinutes = `Enter whole minutes from 0 to ${WEEK_MINUTES}.`;
  const queueBefore = optionalWhole(values.queueBeforeMinutes, 0, WEEK_MINUTES);
  if (queueBefore === null || queueBefore === undefined) errors.queueBeforeMinutes = `Enter whole minutes from 0 to ${WEEK_MINUTES}.`;
  const queueAfter = optionalWhole(values.queueAfterMinutes, 0, WEEK_MINUTES);
  if (queueAfter === null || queueAfter === undefined) errors.queueAfterMinutes = `Enter whole minutes from 0 to ${WEEK_MINUTES}.`;
  const quantity = values.quantity.trim();
  if (quantity !== "" && (decimalText(quantity, 4, 14) === null || !positive(quantity))) errors.quantity = "Enter a quantity above zero with at most 4 decimals.";
  else if (quantity === "" && current.quantity) errors.quantity = "A quantity cannot be removed; change it instead.";
  const overlap = values.overlapQuantity.trim();
  if (overlap !== "") {
    if (decimalText(overlap, 4, 14) === null || !positive(overlap)) errors.overlapQuantity = "Enter a quantity above zero with at most 4 decimals, or leave it blank.";
    else if (quantity === "") errors.overlapQuantity = "An overlap needs a quantity.";
    else if (!errors.quantity && compareDecimal(overlap, quantity) > 0) errors.overlapQuantity = "The overlap cannot exceed the quantity.";
  }
  const dueAt = values.dueAt === "" ? undefined : fromUtcInput(values.dueAt);
  if (dueAt === null) errors.dueAt = "Enter a valid date and time (UTC).";
  const readyAt = values.readyAt === "" ? undefined : fromUtcInput(values.readyAt);
  if (readyAt === null) errors.readyAt = "Enter a valid date and time (UTC).";
  const wanted = values.candidates.split(",").map((c) => c.trim()).filter((c) => c !== "");
  const resourceIds: string[] = [];
  for (const code of wanted) {
    const id = idsByCode[code.toUpperCase()];
    if (!id) {
      errors.candidates = `No resource has the code ${code}.`;
      break;
    }
    if (resourceIds.includes(id)) {
      errors.candidates = `${code} is listed twice.`;
      break;
    }
    if (current.secondaryResourceIds.includes(id)) {
      errors.candidates = `${code} is a secondary resource of this operation; it cannot also be a candidate.`;
      break;
    }
    resourceIds.push(id);
  }
  if (wanted.length === 0) errors.candidates = "List 1 to 50 resource codes in priority order.";
  if (wanted.length > 50) errors.candidates = "List at most 50 resources.";
  if (Object.keys(errors).length > 0) return { errors };
  const patch: OperationPatch = {};
  if (minutes !== current.minutes) patch.minutes = minutes as number;
  if (priority !== current.priority) patch.priority = priority as number;
  if (setupMinutes !== current.setupMinutes) patch.setupMinutes = setupMinutes as number;
  if (queueBefore !== (current.queueBeforeMinutes ?? 0)) patch.queueBeforeMinutes = queueBefore as number;
  if (queueAfter !== (current.queueAfterMinutes ?? 0)) patch.queueAfterMinutes = queueAfter as number;
  const wasQuantity = current.quantity ? trimDecimal(current.quantity) : "";
  const wasOverlap = current.overlapQuantity ? trimDecimal(current.overlapQuantity) : "";
  if (quantity !== "" && trimDecimal(quantity) !== wasQuantity) patch.quantity = quantity;
  if (overlap !== wasOverlap) patch.overlapQuantity = overlap === "" ? "0" : overlap;
  if (dueAt && (!current.dueAt || toUtcInput(current.dueAt) !== values.dueAt)) patch.dueAt = dueAt;
  if (readyAt && (!current.readyAt || toUtcInput(current.readyAt) !== values.readyAt)) patch.readyAt = readyAt;
  if (JSON.stringify(resourceIds) !== JSON.stringify(current.resourceIds)) patch.resourceIds = resourceIds;
  if (Object.keys(patch).length === 0) {
    errors.form = "Nothing changed.";
    return { errors };
  }
  return { errors, patch };
}

interface View {
  op: Operation;
  codes: Record<string, string>;
}

const failureTitles: Record<string, string> = {
  "SCHED-0137": "Someone else changed this operation",
  "SCHED-0138": "A firmed job holds this operation",
};

interface PanelProps {
  op: Operation;
  codes: Record<string, string>;
  onReload: () => void;
}

function Panel({ op: initial, codes, onReload }: PanelProps) {
  const navigate = useNavigate();
  const [op, setOp] = useState<Operation>(initial);
  const [values, setValues] = useState<OperationEditValues>(initialValues(initial, codes));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState<"save" | "delete" | "unschedule" | null>(null);
  const [confirm, setConfirm] = useState<"delete" | "unschedule" | null>(null);
  const [saved, setSaved] = useState<OperationUpdate | null>(null);
  const [unscheduled, setUnscheduled] = useState<OrderUnschedule | null>(null);
  const key = useRef(newKey());
  const idsByCode = Object.fromEntries(Object.entries(codes).map(([id, code]) => [code.toUpperCase(), id]));

  function change(patch: Partial<OperationEditValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  function fail(error: unknown) {
    const problem = describeProblem(error);
    const found: Errors = {};
    for (const name of ["minutes", "dueAt", "readyAt", "priority", "setupMinutes", "queueBeforeMinutes", "queueAfterMinutes", "quantity", "overlapQuantity"] as const) {
      if (problem.fields[name]) found[name] = problem.fields[name];
    }
    if (problem.fields.resourceIds) found.candidates = problem.fields.resourceIds;
    if (Object.keys(found).length > 0) setErrors(found);
    else setFailure(problem);
    if (problem.code === "idempotency_key_reuse") key.current = newKey();
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    const { errors: found, patch } = validateOperationEdit(values, op, idsByCode);
    setErrors(found);
    setFailure(null);
    setSaved(null);
    setUnscheduled(null);
    if (!patch) return;
    setBusy("save");
    try {
      const result = await updateOperation(op.id, op.version, patch, key.current);
      const { jobsRemoved, ...next } = result;
      setOp(next);
      setValues(initialValues(next, codes));
      setSaved(result);
      key.current = newKey();
    } catch (error) {
      fail(error);
    } finally {
      setBusy(null);
    }
  }

  async function remove() {
    setBusy("delete");
    setFailure(null);
    try {
      await deleteOperation(op.id, op.version);
      navigate("/scheduling/operations");
    } catch (error) {
      setConfirm(null);
      fail(error);
    } finally {
      setBusy(null);
    }
  }

  async function takeOff() {
    setBusy("unschedule");
    setFailure(null);
    setSaved(null);
    try {
      const result = await unscheduleOrder(op.orderId);
      setUnscheduled(result);
      setConfirm(null);
      const fresh = await getOperation(op.id);
      setOp(fresh);
      setValues(initialValues(fresh, codes));
    } catch (error) {
      setConfirm(null);
      fail(error);
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className={styles.stack}>
      <Card title="Operation">
        <dl className={styles.details}>
          {(
            [
              ["Order", <Link key="o" to={`/scheduling/order-imports/${op.orderId}`} title={op.orderId}>{shortId(op.orderId)}</Link>],
              ["Operation", op.operNumber],
              ["Status", <Badge key="s" tone={statusTone[op.status] ?? "neutral"}>{sentence(op.status)}</Badge>],
              ["Duration", formatMinutes(op.minutes)],
              ["Ready no earlier than", op.readyAt ? formatDateTime(op.readyAt) : dash],
              ["Due", op.dueAt ? formatDateTime(op.dueAt) : dash],
              ["Priority", op.priority],
              ["Candidates", op.resourceIds.map((id) => codes[id] ?? shortId(id)).join(", ")],
              ["Secondary", op.secondaryResourceIds.length > 0 ? op.secondaryResourceIds.map((id) => codes[id] ?? shortId(id)).join(", ") : dash],
              ["Setup group", op.setupGroup ?? dash],
              ["Route setup", formatMinutes(op.setupMinutes)],
              ["Queue before", formatMinutes(op.queueBeforeMinutes ?? 0)],
              ["Queue after", formatMinutes(op.queueAfterMinutes ?? 0)],
              ["Quantity", op.quantity ? trimDecimal(op.quantity) : dash],
              ["Overlap quantity", op.overlapQuantity ? trimDecimal(op.overlapQuantity) : dash],
              ["Attributes", describeAttributes(op.attributes)],
              ["Version", op.version],
            ] as [string, ReactNode][]
          ).map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>

      {failure ? (
        <Alert
          tone="danger"
          title={failureTitles[failure.code] ?? "The change failed"}
          live="alert"
          action={failure.code === "SCHED-0137" ? <Button size="sm" onClick={onReload}>Load the latest version</Button> : undefined}
        >
          {failure.message} ({failure.code})
          {failure.code === "SCHED-0138" ? " Unfirm the job on the Gantt workspace first." : ""}
        </Alert>
      ) : null}
      <Alert tone="success" live="status">
        {saved ? `The operation was saved (version ${saved.version}).` : unscheduled ? `Order taken off the schedule: ${unscheduled.operations.length} operation(s) are open again.` : ""}
      </Alert>
      {saved?.jobsRemoved ? (
        <Alert tone="warning" title="The operation is off the schedule">
          Its placement was out of date, so its jobs were removed. Run the schedule to place it again.
        </Alert>
      ) : null}
      {unscheduled && unscheduled.keptFirmed.length > 0 ? (
        <Alert tone="warning" title="Firmed operations stayed where they are">
          {`Operation ${unscheduled.keptFirmed.join(", ")} ${unscheduled.keptFirmed.length === 1 ? "is" : "are"} held by a firmed job. Unfirm on the Gantt workspace to take it off too.`}
        </Alert>
      ) : null}

      <Card title="Change this operation">
        <form className={styles.form} onSubmit={save} noValidate aria-label="Change the operation">
          <FormField id="op-minutes" label="Duration (minutes)" error={errors.minutes}>
            {(control) => <Input {...control} inputMode="numeric" value={values.minutes} onChange={(e) => change({ minutes: e.target.value })} invalid={Boolean(errors.minutes)} />}
          </FormField>
          <FormField id="op-ready" label="Ready no earlier than (UTC)" hint="Leave as it is to keep the date; a date cannot be cleared." error={errors.readyAt}>
            {(control) => <Input {...control} type="datetime-local" value={values.readyAt} onChange={(e) => change({ readyAt: e.target.value })} invalid={Boolean(errors.readyAt)} />}
          </FormField>
          <FormField id="op-due" label="Due (UTC)" hint="Leave as it is to keep the date; a date cannot be cleared." error={errors.dueAt}>
            {(control) => <Input {...control} type="datetime-local" value={values.dueAt} onChange={(e) => change({ dueAt: e.target.value })} invalid={Boolean(errors.dueAt)} />}
          </FormField>
          <FormField id="op-priority" label="Priority" hint="0 to 1000. Changing only the priority keeps the jobs where they are." error={errors.priority}>
            {(control) => <Input {...control} inputMode="numeric" value={values.priority} onChange={(e) => change({ priority: e.target.value })} invalid={Boolean(errors.priority)} />}
          </FormField>
          <FormField id="op-setup" label="Route setup (minutes)" error={errors.setupMinutes}>
            {(control) => <Input {...control} inputMode="numeric" value={values.setupMinutes} onChange={(e) => change({ setupMinutes: e.target.value })} invalid={Boolean(errors.setupMinutes)} />}
          </FormField>
          <FormField id="op-queue-before" label="Queue before (minutes)" hint="Elapsed wait in front of the operation; no resource is booked." error={errors.queueBeforeMinutes}>
            {(control) => <Input {...control} inputMode="numeric" value={values.queueBeforeMinutes} onChange={(e) => change({ queueBeforeMinutes: e.target.value })} invalid={Boolean(errors.queueBeforeMinutes)} />}
          </FormField>
          <FormField id="op-queue-after" label="Queue after (minutes)" hint="Elapsed wait behind the operation, before the next one may start." error={errors.queueAfterMinutes}>
            {(control) => <Input {...control} inputMode="numeric" value={values.queueAfterMinutes} onChange={(e) => change({ queueAfterMinutes: e.target.value })} invalid={Boolean(errors.queueAfterMinutes)} />}
          </FormField>
          <FormField id="op-quantity" label="Quantity" hint="The order quantity this operation works on; it cannot be removed once set." error={errors.quantity}>
            {(control) => <Input {...control} inputMode="decimal" value={values.quantity} onChange={(e) => change({ quantity: e.target.value })} invalid={Boolean(errors.quantity)} />}
          </FormField>
          <FormField id="op-overlap" label="Overlap quantity" hint="The next operation may start once this part of the quantity is through. Blank removes the overlap." error={errors.overlapQuantity}>
            {(control) => <Input {...control} inputMode="decimal" value={values.overlapQuantity} onChange={(e) => change({ overlapQuantity: e.target.value })} invalid={Boolean(errors.overlapQuantity)} />}
          </FormField>
          <FormField id="op-candidates" label="Candidate resources" hint="Resource codes in priority order, separated by commas." error={errors.candidates}>
            {(control) => <Input {...control} value={values.candidates} onChange={(e) => change({ candidates: e.target.value })} invalid={Boolean(errors.candidates)} />}
          </FormField>
          <p className={styles.note}>Changing anything but the priority takes the operation off the schedule; run the schedule to place it again. A firmed operation cannot be changed.</p>
          {errors.form ? (
            <p role="alert" className={styles.fieldError}>
              {errors.form}
            </p>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy === "save"} disabled={busy !== null && busy !== "save"}>
              Save changes
            </Button>
            <Link className={buttonClassName("ghost")} to="/scheduling/operations">
              All operations
            </Link>
          </div>
        </form>
      </Card>

      <Card title="Take off the schedule">
        {confirm === null ? (
          <div className={styles.formActions}>
            <Button variant="secondary" disabled={busy !== null} onClick={() => setConfirm("unschedule")}>
              Take the order off the schedule
            </Button>
            <Button variant="danger" disabled={busy !== null} onClick={() => setConfirm("delete")}>
              Delete this operation
            </Button>
          </div>
        ) : confirm === "unschedule" ? (
          <div className={styles.stack}>
            <p className={styles.note}>
              Every operation of the order loses its jobs and is open again, except the ones a firmed job holds. The operations stay; run the schedule to place the order again.
            </p>
            <div className={styles.formActions}>
              <Button variant="primary" loading={busy === "unschedule"} onClick={takeOff}>
                Take the order off the schedule
              </Button>
              <Button variant="ghost" disabled={busy !== null} onClick={() => setConfirm(null)}>
                Keep it scheduled
              </Button>
            </div>
          </div>
        ) : (
          <div className={styles.stack}>
            <p className={styles.note}>The operation and its jobs are removed. An operation registered from a production order comes back with that order's next event or re-import.</p>
            <div className={styles.formActions}>
              <Button variant="danger" loading={busy === "delete"} onClick={remove}>
                Delete the operation
              </Button>
              <Button variant="ghost" disabled={busy !== null} onClick={() => setConfirm(null)}>
                Keep the operation
              </Button>
            </div>
          </div>
        )}
      </Card>
    </div>
  );
}

/** One operation: its facts, a change form, and taking the order off the schedule or deleting the operation (FR-23.4, FR-23.11). */
export function OperationDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad<View>(async (signal) => {
    const [op, resources] = await Promise.all([getOperation(id, signal), listResources(signal)]);
    return { op, codes: Object.fromEntries(resources.map((r) => [r.id, r.code])) };
  }, id);
  return (
    <div className={styles.stack}>
      <PageHeader title="Operation" description="Change an operation's duration, dates, priority or candidates, take its order off the schedule, or delete it." />
      {state.error ? (
        <Alert tone="danger" title="Could not load the operation" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : !state.data ? (
        <div aria-busy="true">
          <span className="visually-hidden">Loading the operation…</span>
          <Skeleton height="tile" />
        </div>
      ) : (
        <Panel key={`${state.data.op.id}:${state.data.op.version}`} op={state.data.op} codes={state.data.codes} onReload={state.reload} />
      )}
    </div>
  );
}
