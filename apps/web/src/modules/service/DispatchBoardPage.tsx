import { useMemo, useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  assignWorkOrder,
  describeProblem,
  getDispatchBoard,
  newKey,
  publishDispatch,
  unassignWorkOrder,
  type BoardQuery,
  type DispatchBoard,
  type Problem,
  type WorkOrder,
} from "./api/client";
import { DATE, dash, duration, formatDateTime, shortId, today, utcInstant, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export interface BoardValues {
  territoryId: string;
  date: string;
  timeZone: string;
}

type Errors = Record<string, string>;

/** Checks that mirror the service (api/dispatch.go dispatchBoard). */
export function validateBoard(values: BoardValues): { errors: Errors; query?: BoardQuery } {
  const errors: Errors = {};
  if (!UUID.test(values.territoryId.trim())) errors.territoryId = "Enter a territory UUID.";
  if (!DATE.test(values.date) || Number.isNaN(Date.parse(`${values.date}T00:00:00Z`))) errors.date = "Enter a date as YYYY-MM-DD.";
  const zone = values.timeZone.trim() || "UTC";
  if (zone === "Local") errors.timeZone = "Enter an IANA time zone, such as America/Chicago.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, query: { territoryId: values.territoryId.trim(), date: values.date, timeZone: zone } };
}

export interface AssignValues {
  workOrderId: string;
  technicianId: string;
  start: string;
  pinned: boolean;
}

/** The request the assign form sends; the start is read as UTC. */
export function validateAssign(values: AssignValues): { errors: Errors; start?: string } {
  const errors: Errors = {};
  if (!values.workOrderId) errors.workOrderId = "Pick a work order.";
  if (!values.technicianId) errors.technicianId = "Pick a technician.";
  const start = utcInstant(values.start);
  if (!start) errors.start = "Enter the start date and time.";
  return { errors, start: start ?? undefined };
}

/** The dispatch rules a refused assignment broke, one line per code, in the service's words. */
export function ruleLines(problem: Problem): string[] {
  const entries = Object.entries(problem.fields);
  return entries.length > 0 ? entries.map(([code, detail]) => `${code}: ${detail}`) : [`${problem.message} (${problem.code})`];
}

const etagOf = (wo: WorkOrder) => `"${wo.version}"`;

/** The duty the service enforces on assign, unassign and publish (and on technician changes). */
export const DUTY_DISPATCH = "fieldservice.technician.dispatch";

/**
 * The dispatch board of one territory and day (PRD 8.45.6): technicians with their
 * assignments, the unscheduled work orders, assignment under the BR-45.3 hard rules and
 * publishing the day (FR-45.7).
 */
export function DispatchBoardPage() {
  const [values, setValues] = useState<BoardValues>({ territoryId: "", date: today(), timeZone: "UTC" });
  const [errors, setErrors] = useState<Errors>({});
  const [query, setQuery] = useState<BoardQuery | null>(null);
  const state = useLoad<DispatchBoard | null>((signal) => (query ? getDispatchBoard(query, signal) : Promise.resolve(null)), query ? `${query.territoryId}|${query.date}|${query.timeZone}` : "none");

  function submit(event: FormEvent) {
    event.preventDefault();
    const result = validateBoard(values);
    setErrors(result.errors);
    if (result.query) setQuery(result.query);
  }

  const board = state.data;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Dispatch board"
        description="Give unscheduled work orders to technicians for a day, then publish the day to dispatch them. A visit must fit the technician's skills, territory, shift, buffer and daily maximum."
        actions={
          <Link className={buttonClassName("secondary")} to="/service/technicians">
            Technicians
          </Link>
        }
      />
      <form className={styles.toolbar} onSubmit={submit} noValidate aria-label="Board day">
        <FormField id="board-territory" label="Territory" hint="UUID" error={errors.territoryId}>
          {(control) => <Input {...control} className={styles.mono} value={values.territoryId} onChange={(e) => setValues({ ...values, territoryId: e.target.value })} invalid={Boolean(errors.territoryId)} />}
        </FormField>
        <FormField id="board-date" label="Day" error={errors.date}>
          {(control) => <Input {...control} type="date" value={values.date} onChange={(e) => setValues({ ...values, date: e.target.value })} invalid={Boolean(errors.date)} />}
        </FormField>
        <FormField id="board-zone" label="Time zone" hint="The day is read in this zone." error={errors.timeZone}>
          {(control) => <Input {...control} value={values.timeZone} onChange={(e) => setValues({ ...values, timeZone: e.target.value })} invalid={Boolean(errors.timeZone)} />}
        </FormField>
        <Button type="submit" variant="primary" loading={Boolean(query) && state.loading}>
          Show board
        </Button>
      </form>
      {!query ? (
        <EmptyState title="Pick a territory and a day" description="The board lists the technicians who cover the territory and the work orders waiting to be scheduled." />
      ) : state.error ? (
        <Alert tone="danger" title="Could not load the dispatch board" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : !board ? (
        <div className={styles.loading} aria-busy="true">
          <span className="visually-hidden">Loading the dispatch board…</span>
          <Skeleton height="tile" />
          <Skeleton height="tile" />
        </div>
      ) : (
        <BoardView board={board} reload={state.reload} />
      )}
    </div>
  );
}

function BoardView({ board, reload }: { board: DispatchBoard; reload(): void }) {
  const canDispatch = holdsDuty(usePrincipalDuties(), DUTY_DISPATCH);
  const [assign, setAssign] = useState<AssignValues>({ workOrderId: "", technicianId: "", start: `${board.date}T08:00`, pinned: false });
  const [assignErrors, setAssignErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [legalEntityId, setLegalEntityId] = useState("");
  const publishKey = useRef(newKey());

  const scheduled = useMemo(() => board.technicians.flatMap((row) => row.assignments.filter((wo) => wo.status === "SCHEDULED")), [board]);
  const suggestedEntity = board.technicians[0]?.technician.legalEntityId ?? board.unscheduled[0]?.legalEntityId ?? "";
  const entity = legalEntityId || suggestedEntity;
  const technicianCode = (id?: string | null) => board.technicians.find((row) => row.technician.id === id)?.technician.code ?? shortId(id);

  async function run(action: string, call: () => Promise<string>) {
    setBusy(action);
    setFailure(null);
    setNotice("");
    try {
      setNotice(await call());
      reload();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "conflict_version") reload();
    } finally {
      setBusy(null);
    }
  }

  function submitAssign(event: FormEvent) {
    event.preventDefault();
    const { errors, start } = validateAssign(assign);
    setAssignErrors(errors);
    const wo = board.unscheduled.find((w) => w.id === assign.workOrderId);
    if (!start || !wo) return;
    void run("assign", async () => {
      await assignWorkOrder(wo.id, etagOf(wo), { technicianId: assign.technicianId, start, pinned: assign.pinned });
      setAssign({ ...assign, workOrderId: "" });
      return `${wo.number} is scheduled with ${technicianCode(assign.technicianId)}.`;
    });
  }

  function publish() {
    void run("publish", async () => {
      const key = publishKey.current;
      const result = await publishDispatch({ legalEntityId: entity, territoryId: board.territoryId, date: board.date, timeZone: board.timeZone }, key);
      publishKey.current = newKey();
      return `Dispatched ${result.dispatchedCount} work order${result.dispatchedCount === 1 ? "" : "s"} for ${board.date}.`;
    });
  }

  const columns: DataTableColumn<WorkOrder>[] = [
    { key: "time", header: "Visit (UTC)", mono: true, render: (wo) => `${formatDateTime(wo.assignedStart)} to ${formatDateTime(wo.assignedEnd).slice(11, 16)}` },
    { key: "number", header: "Work order", render: (wo) => <Link to={`/service/work-orders/${wo.id}`}>{wo.number}</Link> },
    { key: "address", header: "Address", render: (wo) => wo.address || dash },
    { key: "status", header: "Status", render: (wo) => <StatusPill status={wo.status} /> },
    { key: "pinned", header: "Pinned", render: (wo) => (wo.assignmentPinned ? "Pinned" : dash) },
    {
      key: "action",
      header: "Action",
      render: (wo) =>
        wo.status === "SCHEDULED" && canDispatch ? (
          <Button size="sm" variant="ghost" loading={busy === `unassign-${wo.id}`} onClick={() => run(`unassign-${wo.id}`, async () => {
            await unassignWorkOrder(wo.id, etagOf(wo));
            return `${wo.number} is unscheduled.`;
          })}>
            {`Unassign ${wo.number}`}
          </Button>
        ) : (
          dash
        ),
    },
  ];

  const waiting: DataTableColumn<WorkOrder>[] = [
    { key: "number", header: "Work order", render: (wo) => <Link to={`/service/work-orders/${wo.id}`}>{wo.number}</Link> },
    { key: "estimate", header: "Estimate", align: "end", render: (wo) => duration(wo.estDurationMinutes) },
    { key: "skills", header: "Skills", render: (wo) => ((wo.requiredSkills ?? []).length > 0 ? (wo.requiredSkills ?? []).join(", ") : dash) },
    { key: "window", header: "Customer window (UTC)", mono: true, render: (wo) => (wo.windowStart ? `${formatDateTime(wo.windowStart)} to ${formatDateTime(wo.windowEnd).slice(11, 16)}` : dash) },
    { key: "address", header: "Address", render: (wo) => wo.address || dash },
  ];

  return (
    <>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert
          tone="danger"
          title={failure.code === "conflict_version" ? "Someone else changed this work order" : failure.code.startsWith("SVC-DSP") ? "That assignment breaks a dispatch rule" : "The action failed"}
          live="alert"
        >
          <ul>
            {ruleLines(failure).map((line) => (
              <li key={line}>{line}</li>
            ))}
          </ul>
        </Alert>
      ) : null}
      <Card title="Unscheduled work orders" headingLevel={3}>
        <DataTable label="Unscheduled work orders" columns={waiting} rows={board.unscheduled} getRowKey={(wo) => wo.id} empty={{ title: "Nothing waiting", description: "Every work order of this territory is scheduled." }} />
        {board.unscheduled.length > 0 && board.technicians.length > 0 && canDispatch ? (
          <form className={styles.form} onSubmit={submitAssign} noValidate aria-label="Assign a work order">
            <FormField id="assign-wo" label="Work order" error={assignErrors.workOrderId}>
              {(control) => (
                <Select {...control} value={assign.workOrderId} onChange={(e) => setAssign({ ...assign, workOrderId: e.target.value })}>
                  <option value="">Choose…</option>
                  {board.unscheduled.map((wo) => (
                    <option key={wo.id} value={wo.id}>
                      {`${wo.number} · ${duration(wo.estDurationMinutes)}`}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="assign-tech" label="Technician" error={assignErrors.technicianId}>
              {(control) => (
                <Select {...control} value={assign.technicianId} onChange={(e) => setAssign({ ...assign, technicianId: e.target.value })}>
                  <option value="">Choose…</option>
                  {board.technicians.map(({ technician }) => (
                    <option key={technician.id} value={technician.id}>
                      {`${technician.code} · ${technician.name}`}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="assign-start" label="Start" hint="UTC" error={assignErrors.start}>
              {(control) => <Input {...control} type="datetime-local" value={assign.start} onChange={(e) => setAssign({ ...assign, start: e.target.value })} invalid={Boolean(assignErrors.start)} />}
            </FormField>
            <div className={styles.formActions}>
              <span className={styles.check}>
                <Input id="assign-pinned" type="checkbox" checked={assign.pinned} onChange={(e) => setAssign({ ...assign, pinned: e.target.checked })} />
                <label htmlFor="assign-pinned">Pin the assignment</label>
              </span>
              <Button type="submit" variant="primary" loading={busy === "assign"}>
                Assign
              </Button>
            </div>
          </form>
        ) : null}
      </Card>
      {board.technicians.length === 0 ? (
        <EmptyState title="No active technician covers this territory" description="Register a technician for the territory, or activate one, before you schedule work." />
      ) : (
        board.technicians.map(({ technician, assignments, bookedMinutes }) => (
          <Card key={technician.id} title={`${technician.code} · ${technician.name}`} headingLevel={3}>
            <p className={styles.note}>
              {`${duration(bookedMinutes)} booked of ${duration(technician.maxDailyMinutes)}. Skills: ${technician.skills.length > 0 ? technician.skills.join(", ") : "none"}.`}
            </p>
            <DataTable
              label={`Assignments of ${technician.code}`}
              columns={columns}
              rows={assignments}
              getRowKey={(wo) => wo.id}
              empty={{ title: "Nothing scheduled", description: `${technician.name} has no visits on ${board.date}.` }}
            />
          </Card>
        ))
      )}
      {!canDispatch ? (
        <Alert tone="info" title="View only">
          Assigning, unassigning and publishing need the {DUTY_DISPATCH} duty.
        </Alert>
      ) : (
      <Card title="Publish the day" headingLevel={3}>
        <p className={styles.note}>
          Publishing dispatches every scheduled visit of {board.date} in this territory to its technician. {scheduled.length === 0 ? "Nothing is scheduled yet." : `${scheduled.length} scheduled.`}
        </p>
        <div className={styles.toolbar}>
          <FormField id="publish-entity" label="Legal entity" hint="UUID">
            {(control) => <Input {...control} className={styles.mono} value={entity} onChange={(e) => setLegalEntityId(e.target.value)} invalid={entity !== "" && !UUID.test(entity)} />}
          </FormField>
          <Button variant="primary" loading={busy === "publish"} disabled={scheduled.length === 0 || !UUID.test(entity)} onClick={publish}>
            Publish day
          </Button>
        </div>
      </Card>
      )}
    </>
  );
}
