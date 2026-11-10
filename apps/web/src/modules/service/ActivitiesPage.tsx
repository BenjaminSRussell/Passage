import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  ACTIVITY_KINDS,
  ACTIVITY_STATUSES,
  activityCalendarUrl,
  ASSOCIABLE_TYPES,
  cancelActivity,
  completeActivity,
  createActivity,
  describeProblem,
  getActivity,
  listActivities,
  newKey,
  rescheduleActivity,
  type Activity,
  type ActivityInput,
  type ActivityKind,
  type ActivitySchedule,
  type ActivityStatus,
  type AssociableType,
  type Problem,
  type Versioned,
} from "./api/client";
import { Details } from "./Details";
import { dash, formatDateTime, sentence, shortId, utcInstant, utcLocal, UUID } from "./format";
import { PagedTable } from "./PagedTable";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

/** Where a record of each type lives in this module, when it has a page here. */
const recordPath: Partial<Record<AssociableType, string>> = {
  case: "/service/cases",
  work_order: "/service/work-orders",
  agreement: "/service/agreements",
  service_object: "/service/objects",
  knowledge_article: "/service/knowledge",
};

/** "Case 5555…" linked to its page when this module has one. */
export function RecordLink({ type, id }: { type: AssociableType; id: string }) {
  const label = `${sentence(type)} ${shortId(id)}`;
  const path = recordPath[type];
  return path ? <Link to={`${path}/${id}`}>{label}</Link> : <span>{label}</span>;
}

/** When the activity happens: its start, else its due instant. */
const when = (a: Activity) => formatDateTime(a.startAt ?? a.dueAt);

const columns: DataTableColumn<Activity>[] = [
  { key: "subject", header: "Activity" },
  { key: "kind", header: "Kind", render: (row) => sentence(row.kind) },
  { key: "when", header: "Due or start", mono: true, render: when },
  {
    key: "associations",
    header: "Related to",
    render: (row) =>
      row.associations.length === 0 ? (
        dash
      ) : (
        <span className={styles.actions}>
          {row.associations.map((as) => (
            <RecordLink key={`${as.entityType}-${as.entityId}`} type={as.entityType} id={as.entityId} />
          ))}
        </span>
      ),
  },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

/** "My activities" (PRD 8.45.6): one owner's activities across records, earliest due first. */
export function ActivitiesPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const [owner, setOwner] = useState(params.get("owner") ?? "");
  const [status, setStatus] = useState<ActivityStatus | "">("OPEN");
  const ownerId = owner.trim();
  const valid = UUID.test(ownerId);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="My activities"
        description="Tasks, appointments and notes on cases, work orders, projects and other records, earliest due first."
        actions={
          <div className={styles.actions}>
            {valid ? (
              <a className={buttonClassName("secondary")} href={activityCalendarUrl({ owner: ownerId, status })} download="activities.ics">
                Download calendar
              </a>
            ) : null}
            <Link className={buttonClassName("primary")} to={valid ? `/service/activities/new?owner=${ownerId}` : "/service/activities/new"}>
              New activity
            </Link>
          </div>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="activities-owner" label="Owner" hint="Your worker UUID." error={owner && !valid ? "Enter a full UUID." : undefined}>
          {(control) => (
            <Input
              {...control}
              className={styles.mono}
              value={owner}
              invalid={Boolean(owner && !valid)}
              onChange={(e) => {
                setOwner(e.target.value);
                if (UUID.test(e.target.value.trim())) setParams({ owner: e.target.value.trim() }, { replace: true });
              }}
            />
          )}
        </FormField>
        <FormField id="activities-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as ActivityStatus | "")}>
              <option value="">Any status</option>
              {ACTIVITY_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {valid ? (
        <PagedTable
          label="My activities"
          columns={columns}
          load={(cursor, signal) => listActivities({ owner: ownerId, status, cursor }, signal)}
          loadKey={`${ownerId}|${status}`}
          empty={{ title: "Nothing to do", description: "No activities match this owner and status." }}
          onRowOpen={(row) => navigate(`/service/activities/${row.id}`)}
        />
      ) : (
        <p className={styles.note}>Enter the owner&apos;s worker UUID to list their activities.</p>
      )}
    </div>
  );
}

/** The activities on one record, with a link to add one (FR-45.14). */
export function ActivityPanel({ entityType, entityId }: { entityType: AssociableType; entityId: string }) {
  const navigate = useNavigate();
  return (
    <Card
      title="Activities"
      headingLevel={3}
      actions={
        <Link className={buttonClassName("secondary", "sm")} to={`/service/activities/new?entityType=${entityType}&entityId=${entityId}`}>
          Add activity
        </Link>
      }
    >
      <PagedTable
        label={`${sentence(entityType)} activities`}
        columns={columns.filter((c) => c.key !== "associations")}
        load={(cursor, signal) => listActivities({ entityType, entityId, cursor }, signal)}
        loadKey={`${entityType}|${entityId}`}
        empty={{ title: "No activities yet", description: "Add a task, appointment or note for this record." }}
        onRowOpen={(row) => navigate(`/service/activities/${row.id}`)}
      />
    </Card>
  );
}

export interface ActivityValues {
  kind: ActivityKind;
  subject: string;
  ownerWorkerId: string;
  dueAt: string;
  startAt: string;
  endAt: string;
  reminderMinutes: string;
  entityType: AssociableType | "";
  entityId: string;
}

function scheduleOf(values: Pick<ActivityValues, "dueAt" | "startAt" | "endAt">, errors: Errors): ActivitySchedule {
  const out: ActivitySchedule = {};
  for (const name of ["dueAt", "startAt", "endAt"] as const) {
    if (!values[name]) continue;
    const instant = utcInstant(values[name]);
    if (instant === null) errors[name] = "Choose a date and time, or leave it empty.";
    else out[name] = instant;
  }
  if (out.startAt && out.endAt && out.endAt < out.startAt) errors.endAt = "End on or after the start.";
  return out;
}

/** Checks that mirror the service (activities.go createActivity). Times are read as UTC. */
export function validateActivity(values: ActivityValues): { errors: Errors; input?: ActivityInput } {
  const errors: Errors = {};
  const subject = values.subject.trim();
  if (subject === "" || [...subject].length > 200) errors.subject = "Enter a subject of at most 200 characters.";
  if (!UUID.test(values.ownerWorkerId.trim())) errors.ownerWorkerId = "Enter the owner's worker UUID.";
  const schedule = scheduleOf(values, errors);
  let reminder: number | undefined;
  if (values.reminderMinutes.trim()) {
    reminder = Number(values.reminderMinutes);
    if (!Number.isInteger(reminder) || reminder < 0 || reminder > 10080) errors.reminderMinutes = "Enter whole minutes from 0 to 10,080 (one week).";
  }
  const entityId = values.entityId.trim();
  if (values.entityType && !UUID.test(entityId)) errors.entityId = "Enter the record's UUID.";
  if (!values.entityType && entityId) errors.entityType = "Choose what kind of record this is.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      kind: values.kind,
      subject,
      ownerWorkerId: values.ownerWorkerId.trim(),
      ...schedule,
      ...(reminder !== undefined ? { reminderMinutes: reminder } : {}),
      associations: values.entityType ? [{ entityType: values.entityType, entityId }] : [],
    },
  };
}

/** Create an activity, optionally on a record passed as ?entityType=&entityId= (and a ?subject=, such as a required stage activity). */
export function NewActivityPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const presetType = params.get("entityType");
  const [values, setValues] = useState<ActivityValues>({
    kind: "TASK",
    subject: params.get("subject") ?? "",
    ownerWorkerId: params.get("owner") ?? "",
    dueAt: "",
    startAt: "",
    endAt: "",
    reminderMinutes: "",
    entityType: presetType && (ASSOCIABLE_TYPES as readonly string[]).includes(presetType) ? (presetType as AssociableType) : "",
    entityId: params.get("entityId") ?? "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<ActivityValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateActivity(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createActivity(input, key.current);
      navigate(`/service/activities/${created.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  // The service names the record field associations[0].entityId.
  const fieldError = (name: string) => errors[name] ?? (name === "entityId" ? errors["associations[0].entityId"] : undefined);
  const text = (name: "subject" | "ownerWorkerId" | "reminderMinutes" | "entityId", label: string, hint?: string, optional = false, mono = false) => (
    <FormField id={`activity-${name}`} label={label} hint={hint} error={fieldError(name)} optional={optional}>
      {(control) => (
        <Input {...control} className={mono ? styles.mono : undefined} value={values[name]} onChange={(e) => update({ [name]: e.target.value })} invalid={Boolean(fieldError(name))} />
      )}
    </FormField>
  );
  const time = (name: "dueAt" | "startAt" | "endAt", label: string) => (
    <FormField id={`activity-${name}`} label={label} hint="UTC." error={errors[name]} optional>
      {(control) => <Input {...control} type="datetime-local" value={values[name]} onChange={(e) => update({ [name]: e.target.value })} invalid={Boolean(errors[name])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New activity" description="A task, appointment or note owned by one worker, attached to the record it is about." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New activity">
          <FormField id="activity-kind" label="Kind">
            {(control) => (
              <Select {...control} value={values.kind} onChange={(e) => update({ kind: e.target.value as ActivityKind })}>
                {ACTIVITY_KINDS.map((k) => (
                  <option key={k} value={k}>
                    {sentence(k)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("subject", "Subject")}
          {text("ownerWorkerId", "Owner", "Worker UUID of the person doing it.", false, true)}
          {time("dueAt", "Due")}
          {time("startAt", "Starts")}
          {time("endAt", "Ends")}
          {text("reminderMinutes", "Reminder", "Minutes before the start or due time.", true)}
          <FormField id="activity-entityType" label="Related record" error={errors.entityType ?? errors["associations[0].entityType"]} optional>
            {(control) => (
              <Select {...control} value={values.entityType} onChange={(e) => update({ entityType: e.target.value as AssociableType | "" })}>
                <option value="">None</option>
                {ASSOCIABLE_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("entityId", "Record", "UUID of the related record.", true, true)}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the activity" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create activity
            </Button>
            <Link className={buttonClassName("ghost")} to="/service/activities">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

/** One activity: complete, cancel or reschedule it (AC-45.15). */
export function ActivityDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getActivity(id, signal), id);
  const [latest, setLatest] = useState<Versioned<Activity> | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [schedule, setSchedule] = useState<{ dueAt: string; startAt: string; endAt: string } | null>(null);
  const [errors, setErrors] = useState<Errors>({});
  const current = latest && latest.value.id === id ? latest : state.data;

  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Activity" />
        <Alert tone="danger" title="Could not load the activity" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Activity" />
        <span className="visually-hidden">Loading the activity…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: act, etag } = current;
  const dates = schedule ?? { dueAt: utcLocal(act.dueAt), startAt: utcLocal(act.startAt), endAt: utcLocal(act.endAt) };

  async function run(action: string, call: () => Promise<Versioned<Activity>>, done: string) {
    setBusy(action);
    setFailure(null);
    setNotice("");
    try {
      setLatest(await call());
      setSchedule(null);
      setNotice(done);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "conflict_version") state.reload();
    } finally {
      setBusy(null);
    }
  }

  function reschedule(event: FormEvent) {
    event.preventDefault();
    const found: Errors = {};
    const when = scheduleOf(dates, found);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void run("reschedule", () => rescheduleActivity(act.id, etag, { dueAt: when.dueAt ?? null, startAt: when.startAt ?? null, endAt: when.endAt ?? null }), "Activity rescheduled.");
  }

  const open = act.status === "OPEN";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={act.subject}
        meta={<StatusPill status={act.status} />}
        actions={
          open ? (
            <div className={styles.actions}>
              <Button variant="primary" loading={busy === "complete"} onClick={() => run("complete", () => completeActivity(act.id, etag), "Activity done.")}>
                Mark done
              </Button>
              <Button variant="danger" loading={busy === "cancel"} onClick={() => run("cancel", () => cancelActivity(act.id, etag), "Activity canceled.")}>
                Cancel activity
              </Button>
            </div>
          ) : null
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failure.code === "conflict_version" ? "Someone else changed this activity; it was reloaded" : "The action failed"} live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <Card title="Activity" headingLevel={3}>
        <Details
          label="Activity"
          items={[
            ["Kind", sentence(act.kind)],
            ["Owner", act.ownerWorkerId, true],
            ["Due", formatDateTime(act.dueAt), true],
            ["Starts", formatDateTime(act.startAt), true],
            ["Ends", formatDateTime(act.endAt), true],
            ["Reminder", act.reminderMinutes !== undefined ? `${act.reminderMinutes} min before` : dash],
            ["Done", formatDateTime(act.completedAt), true],
            [
              "Related to",
              act.associations.length === 0 ? (
                dash
              ) : (
                <span key="r" className={styles.actions}>
                  {act.associations.map((as) => (
                    <RecordLink key={`${as.entityType}-${as.entityId}`} type={as.entityType} id={as.entityId} />
                  ))}
                </span>
              ),
            ],
          ]}
        />
      </Card>
      {open ? (
        <Card title="Reschedule" headingLevel={3}>
          <form className={styles.form} onSubmit={reschedule} noValidate aria-label="Reschedule activity">
            {(["dueAt", "startAt", "endAt"] as const).map((name) => (
              <FormField key={name} id={`reschedule-${name}`} label={name === "dueAt" ? "Due" : name === "startAt" ? "Starts" : "Ends"} hint="UTC; leave empty to clear." error={errors[name]} optional>
                {(control) => (
                  <Input {...control} type="datetime-local" value={dates[name]} onChange={(e) => setSchedule({ ...dates, [name]: e.target.value })} invalid={Boolean(errors[name])} />
                )}
              </FormField>
            ))}
            <div className={styles.formActions}>
              <Button type="submit" loading={busy === "reschedule"}>
                Reschedule
              </Button>
            </div>
          </form>
        </Card>
      ) : (
        <p className={styles.note}>
          <Badge tone="neutral">{sentence(act.status)}</Badge> activities are final.
        </p>
      )}
    </div>
  );
}
