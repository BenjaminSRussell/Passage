import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import {
  createSchedule,
  DELIVERY_STATUSES,
  describeProblem,
  DESTINATION_TYPES,
  getSchedule,
  listDeliveries,
  listSchedules,
  SCHEDULE_FORMATS,
  TARGET_TYPES,
  UUID,
  type Delivery,
  type Problem,
  type Schedule,
  type ScheduleInput,
} from "./api/client";
import { count, dateTime, enumLabel, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const destinationLabel: Record<Schedule["destination"]["type"], string> = { EMAIL: "E-mail", SFTP: "SFTP", INBOX: "In-app inbox" };

function destinationText(d: Schedule["destination"]): string {
  return `${destinationLabel[d.type]}: ${d.targets.join(", ")}`;
}

const columns: DataTableColumn<Schedule>[] = [
  { key: "name", header: "Name" },
  { key: "target", header: "Report", render: (s) => `${enumLabel(s.targetType)} ${s.targetId}` },
  { key: "cron", header: "Cron", mono: true },
  { key: "timezone", header: "Time zone", mono: true },
  { key: "format", header: "Format" },
  { key: "destination", header: "Destination", render: (s) => destinationText(s.destination) },
  { key: "nextRunAt", header: "Next run", render: (s) => dateTime(s.nextRunAt) },
  { key: "failureCount", header: "Failures", align: "end" },
];

/** Scheduled reports (FR-46.10, PRD §9.46.6 "Schedules"). */
export function SchedulesPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listSchedules(undefined, signal), "schedules");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Scheduled reports"
        description="Dashboards and saved explorer queries delivered on a cron schedule in the time zone you choose. A failed delivery is retried three times, after 1, 4 and 16 minutes."
        actions={
          <Link className={buttonClassName("primary")} to="/analytics/schedules/new">
            New schedule
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load schedules" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Report schedules"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(s) => s.scheduleId}
          loading={state.loading}
          empty={{ title: "No schedules yet", description: "Create a schedule to deliver a dashboard or query by e-mail, SFTP or the in-app inbox." }}
          onRowOpen={(s) => navigate(`/analytics/schedules/${encodeURIComponent(s.scheduleId)}`)}
        />
      )}
    </div>
  );
}

interface Draft {
  name: string;
  targetType: ScheduleInput["targetType"];
  targetId: string;
  cron: string;
  timezone: string;
  format: ScheduleInput["format"];
  destinationType: ScheduleInput["destination"]["type"];
  targets: string;
}
type DraftErrors = Partial<Record<keyof Draft | "destination", string>>;

const browserZone = (() => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
})();

const blank: Draft = {
  name: "",
  targetType: "DASHBOARD",
  targetId: "",
  cron: "0 6 * * 1",
  timezone: browserZone,
  format: "PDF",
  destinationType: "EMAIL",
  targets: "",
};

const TARGET_ID = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/;
const EMAIL = /^[^@\s]+@[^@\s]+\.[^@\s]+$/;
const SFTP = /^sftp:\/\/[A-Za-z0-9.-]+(:[0-9]{1,5})?\/\S*$/;

function splitTargets(raw: string): string[] {
  return raw
    .split(/[\s,;]+/)
    .map((t) => t.trim())
    .filter((t) => t !== "");
}

/** Checks that mirror domain.PrepareSchedule; the service also checks the cron and time zone. */
export function validateSchedule(d: Draft): DraftErrors {
  const errors: DraftErrors = {};
  if (d.name.trim() === "" || d.name.trim().length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!TARGET_ID.test(d.targetId)) errors.targetId = "Enter the dashboard or saved query id.";
  if (d.cron.trim().split(/\s+/).length !== 5) errors.cron = "Use five fields: minute, hour, day of month, month and day of week.";
  if (d.timezone.trim() === "") errors.timezone = "Enter a time zone such as Europe/Berlin.";
  const targets = splitTargets(d.targets);
  if (targets.length === 0 || targets.length > 50) errors.targets = "Enter between 1 and 50 recipients.";
  else if (d.destinationType === "EMAIL" && !targets.every((t) => EMAIL.test(t))) errors.targets = "Enter e-mail addresses.";
  else if (d.destinationType === "SFTP" && (targets.length !== 1 || !SFTP.test(targets[0]))) errors.targets = "Enter one sftp://host/path address.";
  else if (d.destinationType === "INBOX" && !targets.every((t) => UUID.test(t))) errors.targets = "Enter user ids.";
  return errors;
}

const targetHint: Record<Draft["destinationType"], string> = {
  EMAIL: "E-mail addresses, separated by commas or new lines.",
  SFTP: "One address such as sftp://files.example.com/reports.",
  INBOX: "User ids, separated by commas or new lines.",
};

function newKey(): string {
  return crypto.randomUUID();
}

/** Creates a schedule. One idempotency key per filled-in form makes a retried submit safe. */
export function ScheduleCreatePage() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<Draft>(blank);
  const [errors, setErrors] = useState<DraftErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef<{ body: string; key: string } | null>(null);
  const set = <K extends keyof Draft>(k: K, value: Draft[K]) => setDraft((d) => ({ ...d, [k]: value }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateSchedule(draft);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const input: ScheduleInput = {
      name: draft.name.trim(),
      targetType: draft.targetType,
      targetId: draft.targetId,
      cron: draft.cron.trim().replace(/\s+/g, " "),
      timezone: draft.timezone.trim(),
      format: draft.format,
      destination: { type: draft.destinationType, targets: splitTargets(draft.targets) },
    };
    const body = JSON.stringify(input);
    // The same request keeps its key, so a retry after a lost response replays;
    // an edited form is a new request and gets a new key.
    if (key.current?.body !== body) key.current = { body, key: newKey() };
    setBusy(true);
    try {
      const created = await createSchedule(input, key.current.key);
      navigate(`/analytics/schedules/${encodeURIComponent(created.scheduleId)}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      const fields = problem.fields as DraftErrors;
      setErrors(fields.destination ? { ...fields, targets: fields.destination } : fields);
    } finally {
      setBusy(false);
    }
  }

  const invalid = (k: keyof Draft) => Boolean(errors[k]);

  return (
    <div className={styles.stack}>
      <PageHeader title="New schedule" description="The first delivery is the next time the cron matches in the chosen time zone." />
      <Card title="Schedule">
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label="Report schedule">
          <FormField id="schedule-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} invalid={invalid("name")} value={draft.name} onChange={(e) => set("name", e.target.value)} />}
          </FormField>
          <FormField id="schedule-target-type" label="Report type">
            {(control) => (
              <Select {...control} value={draft.targetType} onChange={(e) => set("targetType", e.target.value as Draft["targetType"])}>
                {TARGET_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {t === "QUERY" ? "Saved explorer query" : "Dashboard"}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="schedule-target-id" label="Report id" error={errors.targetId}>
            {(control) => <Input {...control} className={styles.mono} invalid={invalid("targetId")} value={draft.targetId} onChange={(e) => set("targetId", e.target.value)} />}
          </FormField>
          <FormField id="schedule-format" label="Format">
            {(control) => (
              <Select {...control} value={draft.format} onChange={(e) => set("format", e.target.value as Draft["format"])}>
                {SCHEDULE_FORMATS.map((f) => (
                  <option key={f} value={f}>
                    {f}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="schedule-cron" label="Cron" hint="For example 0 6 * * 1 is Mondays at 06:00. L in the day field is the last day of the month." error={errors.cron}>
            {(control) => <Input {...control} className={styles.mono} invalid={invalid("cron")} value={draft.cron} onChange={(e) => set("cron", e.target.value)} />}
          </FormField>
          <FormField id="schedule-timezone" label="Time zone" hint="An IANA time zone such as Europe/Berlin." error={errors.timezone}>
            {(control) => <Input {...control} className={styles.mono} invalid={invalid("timezone")} value={draft.timezone} onChange={(e) => set("timezone", e.target.value)} />}
          </FormField>
          <FormField id="schedule-destination" label="Deliver to">
            {(control) => (
              <Select {...control} value={draft.destinationType} onChange={(e) => set("destinationType", e.target.value as Draft["destinationType"])}>
                {DESTINATION_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {destinationLabel[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.wide}>
            <FormField id="schedule-targets" label="Recipients" hint={targetHint[draft.destinationType]} error={errors.targets}>
              {(control) => <Textarea {...control} invalid={invalid("targets")} value={draft.targets} onChange={(e) => set("targets", e.target.value)} />}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The schedule was not created" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create schedule
            </Button>
            <Link className={buttonClassName("secondary")} to="/analytics/schedules">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

function Entry({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}

/** One schedule: what it delivers, where, and when it runs next. */
export function ScheduleDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getSchedule(id, undefined, signal), `schedule/${id}`);

  if (state.loading && !state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Schedule" />
        <Card>
          <div className={styles.loading}>
            <Skeleton width="1/2" />
            <Skeleton width="3/4" />
          </div>
        </Card>
        <p role="status" className={styles.muted}>
          Loading schedule…
        </p>
      </div>
    );
  }
  if (state.error?.code === "ANA-0112") {
    return (
      <Card>
        <EmptyState
          headingLevel={2}
          icon="database"
          title="We couldn't find that schedule"
          description="No report schedule has this id in your tenant."
          action={
            <Link className={buttonClassName("secondary")} to="/analytics/schedules">
              Back to schedules
            </Link>
          }
        />
      </Card>
    );
  }
  if (state.error || !state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Schedule" />
        <Alert tone="danger" title="Could not load the schedule" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  const s = state.data;
  return (
    <div className={styles.stack}>
      <PageHeader title={s.name} description={`${enumLabel(s.targetType)} ${s.targetId} as ${s.format}.`} />
      <Card title="Delivery">
        <dl className={styles.details}>
          <Entry term="Cron">
            <span className={styles.mono}>{s.cron}</span>
          </Entry>
          <Entry term="Time zone">
            <span className={styles.mono}>{s.timezone}</span>
          </Entry>
          <Entry term="Next run">{dateTime(s.nextRunAt) || "Not scheduled"}</Entry>
          <Entry term="Last run">{dateTime(s.lastRunAt) || "Not run yet"}</Entry>
          <Entry term="Destination">{destinationText(s.destination)}</Entry>
          <Entry term="Failed attempts in this run">{s.failureCount}</Entry>
        </dl>
      </Card>
      <Deliveries schedule={s} />
    </div>
  );
}

function DeliveryStatus({ delivery }: { delivery: Delivery }) {
  switch (delivery.status) {
    case "DELIVERED":
      return <Badge tone="success">Delivered</Badge>;
    case "RETRY":
      return <Badge tone="warning">Retrying</Badge>;
    default:
      return <Badge tone="danger">Failed</Badge>;
  }
}

const deliveryColumns: DataTableColumn<Delivery>[] = [
  { key: "runAt", header: "Attempted", render: (d) => dateTime(d.runAt) },
  { key: "runId", header: "Run", mono: true, render: (d) => <span title={d.runId}>{shortId(d.runId)}</span> },
  { key: "attempt", header: "Attempt", align: "end" },
  { key: "status", header: "Status", render: (d) => <DeliveryStatus delivery={d} /> },
  { key: "format", header: "Format" },
  { key: "destinationType", header: "Destination", render: (d) => destinationLabel[d.destinationType] },
  { key: "rowCount", header: "Rows", align: "end", render: (d) => count(d.rowCount) },
  { key: "bytes", header: "Bytes", align: "end", render: (d) => count(d.bytes) },
  { key: "retryAt", header: "Next attempt", render: (d) => dateTime(d.retryAt) },
  { key: "error", header: "Error", render: (d) => d.error ?? "" },
];

/** Delivery attempts of a schedule, newest first (PRD §9.46.14 "Schedule deliveries"). */
function Deliveries({ schedule }: { schedule: Schedule }) {
  const [status, setStatus] = useState("");
  const state = useLoad((signal) => listDeliveries(schedule.scheduleId, status, undefined, signal), `deliveries/${schedule.scheduleId}/${status}`);
  // The newest attempt decides the banner: a run that spent its retries is ANA-0108.
  const latest = status === "" ? state.data?.[0] : undefined;
  return (
    <div className={styles.stack}>
      {latest?.status === "FAILED" ? (
        <Alert tone="danger" title={`Schedule ${schedule.name} failed after 3 attempts`}>
          {latest.error || "The destination did not accept the report."} Fix the destination; the next run is {dateTime(schedule.nextRunAt) || "not scheduled"}. (ANA-0108)
        </Alert>
      ) : null}
      <Card title="Deliveries">
        <div className={styles.toolbar}>
          <FormField id="deliveries-status" label="Status">
            {(control) => (
              <Select {...control} value={status} onChange={(e) => setStatus(e.target.value)}>
                <option value="">Every attempt</option>
                {DELIVERY_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {enumLabel(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
        {state.error ? (
          <Alert tone="danger" title="Could not load deliveries" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            <ProblemMessage problem={state.error} />
          </Alert>
        ) : (
          <DataTable
            label="Deliveries"
            columns={deliveryColumns}
            rows={state.data ?? []}
            getRowKey={(d) => d.deliveryId}
            loading={state.loading}
            empty={{ title: "No deliveries yet", description: "Each attempt appears here once the schedule runs. A failed attempt is retried after 1, 4 and 16 minutes." }}
          />
        )}
      </Card>
    </div>
  );
}
