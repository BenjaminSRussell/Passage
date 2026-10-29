import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  FormField,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  type DataTableColumn,
} from "../../shell/ui";
import {
  approveCourseRegistration,
  cancelCourseRegistration,
  completeCourseSession,
  describeProblem,
  getCourseSession,
  listCourseRegistrations,
  listWorkers,
  registerForCourseSession,
  type CompletionLine,
  type CourseRegistration,
  type CourseSession,
  type Problem,
} from "./api/client";
import { dash, formatRate, formatUtc, registrationStatusLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

function Entry({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

export function RegistrationStatus({ status }: { status: string }) {
  const known = registrationStatusLabel[status];
  return <Badge tone={known?.tone ?? "neutral"}>{known?.label ?? status}</Badge>;
}

/** An attendance choice for one row; "" records nothing for that worker. */
export type Attendance = "" | "PASS" | "FAIL" | "ATTENDED" | "NO_SHOW";

/** Which rows can take an attendance line: confirmed ones, and attended ones still without a result. */
export function canRecord(r: CourseRegistration): boolean {
  return r.status === "CONFIRMED" || (r.status === "ATTENDED" && !r.result);
}

/** The completion lines the chosen attendance sends (FR-42.14). */
export function completionLines(choices: Record<string, Attendance>, rows: CourseRegistration[]): CompletionLine[] {
  const lines: CompletionLine[] = [];
  for (const row of rows) {
    const choice = choices[row.id] ?? "";
    if (choice === "" || !canRecord(row)) continue;
    if (choice === "NO_SHOW") lines.push({ workerId: row.workerId, outcome: "NO_SHOW" });
    else if (choice === "ATTENDED") lines.push({ workerId: row.workerId, outcome: "ATTENDED" });
    else lines.push({ workerId: row.workerId, outcome: "ATTENDED", result: choice });
  }
  return lines;
}

function RegisterForm({ session, onRegistered }: { session: CourseSession; onRegistered(message: string): void }) {
  const workers = useLoad((signal) => listWorkers({}, signal), "course-register-workers");
  const [workerId, setWorkerId] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    if (!workerId) {
      setError("Choose the worker to register.");
      return;
    }
    setError(null);
    setBusy(true);
    try {
      const reg = await registerForCourseSession(session.id, workerId);
      const status = (registrationStatusLabel[reg.status]?.label ?? reg.status).toLowerCase();
      const warning = reg.warnings.length > 0 ? ` ${reg.warnings.map((w) => w.detail).join(" ")}` : "";
      onRegistered(`${reg.legalName} is ${status}.${warning}`);
      setWorkerId("");
    } catch (failed) {
      const problem = describeProblem(failed);
      if (problem.code === "HCM-CRS-001") setError("The session and its waitlist are full. Pick another session.");
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.toolbar} onSubmit={submit} noValidate aria-label="Register a worker">
      <FormField id="register-worker" label="Worker" error={error ?? undefined}>
        {(control) => (
          <Select {...control} value={workerId} invalid={Boolean(error)} disabled={!workers.data} onChange={(event) => setWorkerId(event.target.value)}>
            <option value="">{workers.loading ? "Loading workers…" : "Choose a worker"}</option>
            {(workers.data?.items ?? []).map((w) => (
              <option key={w.id} value={w.id}>
                {w.legalName} ({w.personnelNumber})
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <Button type="submit" variant="primary" loading={busy}>
        Register
      </Button>
      {workers.error ? (
        <Alert tone="danger" title="Could not load workers" live="alert" action={<Button size="sm" onClick={workers.reload}>Try again</Button>}>
          {workers.error.message} ({workers.error.code})
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="Could not register the worker" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
    </form>
  );
}

/**
 * One session: seats, the attendance list in registration order, registering
 * a worker, approving or cancelling, and recording attendance and results.
 */
export function CourseSessionPage({ now = () => new Date() }: { now?: () => Date }) {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getCourseSession(id, signal), id);
  const regs = useLoad((signal) => listCourseRegistrations(id, signal), `registrations|${id}`);
  const [choices, setChoices] = useState<Record<string, Attendance>>({});
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  function reloadAll(message: string) {
    setNotice(message);
    state.reload();
    regs.reload();
  }

  async function act(row: CourseRegistration, action: "APPROVE" | "CANCEL") {
    setBusy(`${action}:${row.id}`);
    setFailure(null);
    setNotice(null);
    try {
      const out = action === "APPROVE" ? await approveCourseRegistration(row.id) : await cancelCourseRegistration(row.id);
      reloadAll(`${row.legalName} is now ${(registrationStatusLabel[out.status]?.label ?? out.status).toLowerCase()}.`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  async function record(rows: CourseRegistration[]) {
    const lines = completionLines(choices, rows);
    setFailure(null);
    setNotice(null);
    if (lines.length === 0) {
      setFailure({ code: "COM-0001", message: "Choose an outcome for at least one worker.", fields: {} });
      return;
    }
    setBusy("RECORD");
    try {
      const out = await completeCourseSession(id, lines);
      setChoices({});
      reloadAll(`Recorded ${out.changed} of ${lines.length} line${lines.length === 1 ? "" : "s"}; ${out.passed} passed and had skills and certificates written.`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Session" />
        {state.error.code === "HCM-CRS-SESSION-NOT-FOUND" ? (
          <Card>
            <EmptyState
              headingLevel={3}
              title="We couldn't find this session"
              description="It may belong to another tenant, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/hr/courses">
                  Back to courses
                </Link>
              }
            />
          </Card>
        ) : (
          <Alert tone="danger" title="Could not load the session" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        )}
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Session" />
        <Card>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }
  const session = state.data;
  const rows = regs.data ?? [];
  const started = now().getTime() >= Date.parse(session.startsAt);
  const ended = now().getTime() >= Date.parse(session.endsAt);
  const recordable = rows.filter(canRecord);

  const columns: DataTableColumn<CourseRegistration>[] = [
    {
      key: "legalName",
      header: "Worker",
      render: (row) => (
        <Link to={`/hr/workers/${row.workerId}`}>
          {row.legalName} ({row.personnelNumber})
        </Link>
      ),
    },
    { key: "status", header: "Status", render: (row) => <RegistrationStatus status={row.status} /> },
    { key: "queuedAt", header: "Registered", mono: true, render: (row) => formatUtc(row.queuedAt) },
    { key: "result", header: "Result", render: (row) => (row.result ? `${row.result === "PASS" ? "Pass" : "Fail"}${row.score ? ` (${row.score})` : ""}` : dash) },
    {
      key: "attendance",
      header: "Attendance",
      render: (row) =>
        started && canRecord(row) ? (
          <Select
            id={`attendance-${row.id}`}
            aria-label={`Attendance for ${row.legalName}`}
            value={choices[row.id] ?? ""}
            onChange={(event) => setChoices({ ...choices, [row.id]: event.target.value as Attendance })}
          >
            <option value="">Not recorded</option>
            <option value="PASS">Attended, passed</option>
            <option value="FAIL">Attended, failed</option>
            {row.status === "CONFIRMED" ? <option value="ATTENDED">Attended, result later</option> : null}
            {row.status === "CONFIRMED" ? <option value="NO_SHOW">No-show</option> : null}
          </Select>
        ) : (
          dash
        ),
    },
    {
      key: "actions",
      header: "Actions",
      render: (row) => (
        <div className={styles.rowActions}>
          {row.status === "REQUESTED" ? (
            <Button
              size="sm"
              variant="primary"
              loading={busy === `APPROVE:${row.id}`}
              disabled={busy !== null}
              aria-label={`Approve ${row.legalName}'s registration`}
              onClick={() => void act(row, "APPROVE")}
            >
              Approve
            </Button>
          ) : null}
          {row.status === "REQUESTED" || row.status === "CONFIRMED" || row.status === "WAITLISTED" ? (
            <Button
              size="sm"
              variant="ghost"
              loading={busy === `CANCEL:${row.id}`}
              disabled={busy !== null}
              aria-label={`${row.status === "REQUESTED" ? "Decline" : "Cancel"} ${row.legalName}'s registration`}
              onClick={() => void act(row, "CANCEL")}
            >
              {row.status === "REQUESTED" ? "Decline" : "Cancel"}
            </Button>
          ) : null}
        </div>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${session.courseCode} on ${formatUtc(session.startsAt)}`}
        description={session.courseName}
        actions={
          <Link className={buttonClassName("ghost")} to={`/hr/courses/${session.courseId}`}>
            Back to the course
          </Link>
        }
      />
      <StatGroup label="Seats">
        <StatTile label="Confirmed" value={`${session.confirmed} of ${session.capacity}`} />
        <StatTile label="Seats left" value={session.seatsLeft} />
        <StatTile label="Waitlisted" value={`${session.waitlisted} of ${session.waitlistCapacity}`} />
        <StatTile label="Awaiting approval" value={session.requested} tone="warning" />
      </StatGroup>
      <Card title="Session">
        <dl className={styles.details}>
          <Entry term="Starts" mono>
            {formatUtc(session.startsAt)}
          </Entry>
          <Entry term="Ends" mono>
            {formatUtc(session.endsAt)}
          </Entry>
          <Entry term="Location">{session.location ?? dash}</Entry>
          <Entry term="Online link">{session.virtualUrl ? <a href={session.virtualUrl}>{session.virtualUrl}</a> : dash}</Entry>
          <Entry term="Instructor">{session.instructorWorkerId ? <Link to={`/hr/workers/${session.instructorWorkerId}`}>Open instructor</Link> : dash}</Entry>
          <Entry term="Cost per attendee" mono>
            {session.costPerAttendee && session.currency ? formatRate(session.costPerAttendee, session.currency) : dash}
          </Entry>
        </dl>
      </Card>
      <Card title="Registrations">
        <div className={styles.stack}>
          {ended ? (
            <p className={styles.note}>The session has ended, so registration is closed.</p>
          ) : (
            <RegisterForm session={session} onRegistered={reloadAll} />
          )}
          <Alert tone="success" live="status">
            {notice}
          </Alert>
          {failure ? (
            <Alert tone="danger" title="That didn't work" live="alert">
              {failure.message} ({failure.code})
            </Alert>
          ) : null}
          {regs.error ? (
            <Alert tone="danger" title="Could not load the registrations" live="alert" action={<Button size="sm" onClick={regs.reload}>Try again</Button>}>
              {regs.error.message} ({regs.error.code})
            </Alert>
          ) : (
            <DataTable
              label="Registrations"
              columns={columns}
              rows={rows}
              getRowKey={(row) => row.id}
              loading={regs.loading}
              empty={{ title: "No registrations yet", description: "Register workers above; seats go in registration order, then the waitlist." }}
            />
          )}
          {!started ? (
            <p className={styles.note}>Attendance can be recorded once the session starts.</p>
          ) : recordable.length > 0 ? (
            <div className={styles.rowActions}>
              <Button variant="primary" loading={busy === "RECORD"} disabled={busy !== null} onClick={() => void record(rows)}>
                Record attendance
              </Button>
            </div>
          ) : null}
        </div>
      </Card>
    </div>
  );
}
