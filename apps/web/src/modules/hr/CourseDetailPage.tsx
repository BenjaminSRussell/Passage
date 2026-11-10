import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  createCourseSession,
  DEFAULT_WAITLIST,
  describeProblem,
  getCourse,
  listCourseSessions,
  MAX_SEATS,
  MAX_WAITLIST,
  newKey,
  type Course,
  type CourseSession,
  type NewCourseSession,
  type Problem,
} from "./api/client";
import { dash, formatRate, formatUtc, UUID, utcFromLocalInput, validityLabel, validRate } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

function Entry({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

const sessionColumns: DataTableColumn<CourseSession>[] = [
  { key: "startsAt", header: "Starts", mono: true, render: (row) => <Link to={`/hr/courses/sessions/${row.id}`}>{formatUtc(row.startsAt)}</Link> },
  { key: "endsAt", header: "Ends", mono: true, render: (row) => formatUtc(row.endsAt) },
  { key: "location", header: "Where", render: (row) => row.location ?? (row.virtualUrl ? "Online" : dash) },
  { key: "confirmed", header: "Confirmed", mono: true, align: "end", render: (row) => `${row.confirmed} of ${row.capacity}` },
  { key: "waitlisted", header: "Waitlist", mono: true, align: "end", render: (row) => `${row.waitlisted} of ${row.waitlistCapacity}` },
  { key: "requested", header: "Awaiting approval", mono: true, align: "end" },
  { key: "seatsLeft", header: "Seats left", mono: true, align: "end" },
  { key: "costPerAttendee", header: "Cost each", mono: true, align: "end", render: (row) => (row.costPerAttendee && row.currency ? formatRate(row.costPerAttendee, row.currency) : dash) },
];

export interface SessionValues {
  startsAt: string;
  endsAt: string;
  location: string;
  virtualUrl: string;
  instructorWorkerId: string;
  capacity: string;
  waitlistCapacity: string;
  costPerAttendee: string;
  currency: string;
}
type SessionField = keyof SessionValues;
type SessionErrors = Partial<Record<SessionField, string>>;
const SESSION_FIELDS: SessionField[] = ["startsAt", "endsAt", "location", "virtualUrl", "instructorWorkerId", "capacity", "waitlistCapacity", "costPerAttendee", "currency"];

const blankSession = (): SessionValues => ({
  startsAt: "",
  endsAt: "",
  location: "",
  virtualUrl: "",
  instructorWorkerId: "",
  capacity: "",
  waitlistCapacity: String(DEFAULT_WAITLIST),
  costPerAttendee: "",
  currency: "",
});

function wholeNumber(raw: string, min: number, max: number): number | null {
  if (!/^\d+$/.test(raw.trim())) return null;
  const n = Number(raw);
  return n >= min && n <= max ? n : null;
}

/** The service's session rules, and the request the form sends. Times are entered in UTC. */
export function validateSession(values: SessionValues): { errors: SessionErrors; input?: NewCourseSession } {
  const errors: SessionErrors = {};
  const starts = utcFromLocalInput(values.startsAt);
  const ends = utcFromLocalInput(values.endsAt);
  if (!starts) errors.startsAt = "Enter the start date and time.";
  if (!ends) errors.endsAt = "Enter the end date and time.";
  else if (starts && ends <= starts) errors.endsAt = "The end must be after the start.";
  const location = values.location.trim();
  const virtualUrl = values.virtualUrl.trim();
  if (location === "" && virtualUrl === "") errors.location = "Give a location, an online link, or both.";
  if (location.length > 200) errors.location = "Use up to 200 characters.";
  if (virtualUrl !== "" && (!/^https?:\/\/[^\s/]+/i.test(virtualUrl) || virtualUrl.length > 500)) errors.virtualUrl = "Enter an http or https link.";
  const instructor = values.instructorWorkerId.trim();
  if (instructor !== "" && !UUID.test(instructor)) errors.instructorWorkerId = "Enter the instructor's worker id, or leave it empty.";
  const capacity = wholeNumber(values.capacity, 1, MAX_SEATS);
  if (capacity === null) errors.capacity = `Enter 1 to ${MAX_SEATS} seats.`;
  const waitlist = values.waitlistCapacity.trim() === "" ? DEFAULT_WAITLIST : wholeNumber(values.waitlistCapacity, 0, MAX_WAITLIST);
  if (waitlist === null) errors.waitlistCapacity = `Enter 0 to ${MAX_WAITLIST} places.`;
  const cost = values.costPerAttendee.trim();
  const currency = values.currency.trim().toUpperCase();
  if (cost !== "" || currency !== "") {
    if (!/^[A-Z]{3}$/.test(currency)) errors.currency = "Enter a currency code such as USD.";
    else if (!validRate(cost, currency)) errors.costPerAttendee = "Enter a non-negative amount with the currency's decimals.";
  }
  if (Object.keys(errors).length > 0 || !starts || !ends || capacity === null || waitlist === null) return { errors };
  const input: NewCourseSession = { startsAt: starts, endsAt: ends, capacity, waitlistCapacity: waitlist };
  if (location) input.location = location;
  if (virtualUrl) input.virtualUrl = virtualUrl;
  if (instructor) input.instructorWorkerId = instructor;
  if (cost) {
    input.costPerAttendee = cost;
    input.currency = currency;
  }
  return { errors, input };
}

function SessionForm({ course, onCreated, onCancel }: { course: Course; onCreated(s: CourseSession): void; onCancel(): void }) {
  const [values, setValues] = useState<SessionValues>(blankSession());
  const [errors, setErrors] = useState<SessionErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<SessionValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateSession(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      onCreated(await createCourseSession(course.id, input, key.current));
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: SessionErrors = {};
      for (const name of SESSION_FIELDS) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.code === "HCM-WRK-NOT-FOUND") fieldErrors.instructorWorkerId = "No worker has this id.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const field = (name: SessionField, label: string, hint?: string, extra?: Record<string, unknown>) => (
    <FormField id={`session-${name}`} label={label} hint={hint} error={errors[name]}>
      {(control) => (
        <Input {...control} {...extra} value={values[name]} invalid={Boolean(errors[name])} onChange={(event) => update({ [name]: event.target.value })} />
      )}
    </FormField>
  );

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Schedule a session">
      {field("startsAt", "Starts (UTC)", undefined, { type: "datetime-local" })}
      {field("endsAt", "Ends (UTC)", undefined, { type: "datetime-local" })}
      {field("location", "Location", "A room or site; leave empty for online only.", { maxLength: 200 })}
      {field("virtualUrl", "Online link", "Optional, https://…", { type: "url", maxLength: 500 })}
      {field("instructorWorkerId", "Instructor (worker id)", "Optional.")}
      {field("capacity", "Seats", undefined, { inputMode: "numeric" })}
      {field("waitlistCapacity", "Waitlist places", `Default ${DEFAULT_WAITLIST}.`, { inputMode: "numeric" })}
      {field("costPerAttendee", "Cost per attendee", "Optional.", { inputMode: "decimal" })}
      {field("currency", "Currency", "With a cost, for example USD.", { maxLength: 3 })}
      {failure ? (
        <div className={styles.formActions}>
          <Alert tone="danger" title="Could not schedule the session" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          Schedule session
        </Button>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

/** One course: what a pass awards, its sessions with seats, and a form to schedule another. */
export function CourseDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getCourse(id, signal), id);
  const sessions = useLoad((signal) => listCourseSessions(id, signal), `sessions|${id}`);
  const [adding, setAdding] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Course" />
        {state.error.code === "HCM-CRS-NOT-FOUND" ? (
          <Card>
            <EmptyState
              headingLevel={3}
              title="We couldn't find this course"
              description="It may belong to another tenant, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/hr/courses">
                  Back to courses
                </Link>
              }
            />
          </Card>
        ) : (
          <Alert tone="danger" title="Could not load the course" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            <ProblemMessage problem={state.error} />
          </Alert>
        )}
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Course" />
        <Card>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }
  const course = state.data;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${course.code} ${course.name}`}
        description={course.courseGroup ? `Group ${course.courseGroup}` : undefined}
        meta={course.requiresManagerApproval ? <Badge tone="warning">Manager approval</Badge> : undefined}
        actions={
          <Link className={buttonClassName("ghost")} to="/hr/courses">
            Back to courses
          </Link>
        }
      />
      <Card title="On a pass">
        <dl className={styles.details}>
          <Entry term="Skills">
            {course.skillOutcomes.length === 0 ? "None" : course.skillOutcomes.map((o) => `${o.skillCode} (${o.skillName}) to ${o.rating}`).join(", ")}
          </Entry>
          <Entry term="Certificate">{course.certificationCode ? `${course.certificationCode}: ${course.certificationName ?? ""}` : "None"}</Entry>
          <Entry term="Certificate validity">{course.certificationCode ? validityLabel(course.validityMonths) : dash}</Entry>
          <Entry term="Registration">{course.requiresManagerApproval ? "Needs a manager's approval to take a seat" : "Seats go in registration order"}</Entry>
        </dl>
      </Card>
      <Card
        title="Sessions"
        actions={
          adding ? null : (
            <Button size="sm" onClick={() => setAdding(true)}>
              Schedule session
            </Button>
          )
        }
      >
        <div className={styles.stack}>
          <Alert tone="success" live="status">
            {notice}
          </Alert>
          {adding ? (
            <SessionForm
              course={course}
              onCancel={() => setAdding(false)}
              onCreated={(s) => {
                setAdding(false);
                setNotice(`Session on ${formatUtc(s.startsAt)} scheduled with ${s.capacity} seats.`);
                sessions.reload();
              }}
            />
          ) : null}
          {sessions.error ? (
            <Alert tone="danger" title="Could not load the sessions" live="alert" action={<Button size="sm" onClick={sessions.reload}>Try again</Button>}>
              <ProblemMessage problem={sessions.error} />
            </Alert>
          ) : (
            <DataTable
              label="Sessions"
              columns={sessionColumns}
              rows={sessions.data ?? []}
              getRowKey={(row) => row.id}
              loading={sessions.loading}
              empty={{ title: "No sessions yet", description: "Schedule a session to open registration." }}
            />
          )}
        </div>
      </Card>
    </div>
  );
}
