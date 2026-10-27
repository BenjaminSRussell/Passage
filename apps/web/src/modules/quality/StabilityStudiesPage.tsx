import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { createStudy, describeProblem, getStudy, listStudies, listTestGroups, newKey, type NewStudy, type Problem, type Pull, type Study, type StudyStatus, type TestGroup } from "./api/client";
import { dash, formatDate, formatDateTime, PULL_STATUS_LABELS, shortId, STUDY_STATUS_LABELS, STUDY_STATUSES, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** ICH Q1A(R2) storage conditions offered on the form; any other condition can be typed. */
export const ICH_CONDITIONS = ["25C/60RH", "30C/65RH", "30C/75RH", "40C/75RH", "5C", "-20C"] as const;

/** The ICH long-term schedule (0, 3, 6, 9, 12, 18, 24, 36 months). */
export const DEFAULT_PULLS = "0, 3, 6, 9, 12, 18, 24, 36";

function StudyStatusBadge({ status }: { status: StudyStatus }) {
  const s = STUDY_STATUS_LABELS[status] ?? { label: status, tone: "neutral" as const };
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

/** The next pull still to happen (not pulled or missed), or undefined. */
export function nextPull(study: Study): Pull | undefined {
  return study.pulls.find((p) => p.status === "PLANNED" || p.status === "ORDERED");
}

const columns: DataTableColumn<Study>[] = [
  { key: "id", header: "Study", mono: true, render: (row) => <Link to={`/quality/stability/${row.id}`}>{`Open study ${shortId(row.id)}`}</Link> },
  { key: "itemId", header: "Item", mono: true, render: (row) => shortId(row.itemId) },
  { key: "batchId", header: "Batch", mono: true },
  { key: "condition", header: "Condition" },
  { key: "startDate", header: "Start", render: (row) => formatDate(row.startDate) },
  { key: "pulls", header: "Pull points", align: "end", render: (row) => String(row.pulls.length) },
  {
    key: "next",
    header: "Next pull",
    render: (row) => {
      const next = nextPull(row);
      return next ? `${next.months} months, ${formatDate(next.pullDate)}` : dash;
    },
  },
  { key: "status", header: "Status", render: (row) => <StudyStatusBadge status={row.status} /> },
];

/** Stability studies (FR-33.16), newest first, by status. */
export function StabilityStudiesPage() {
  const [params, setParams] = useSearchParams();
  const status = (params.get("status") ?? "") as StudyStatus | "";
  const state = useLoad((signal) => listStudies({ status }, signal), `studies|${status}`);

  function setStatus(value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set("status", value);
    else next.delete("status");
    setParams(next);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Stability studies"
        description="Studies of a batch under a storage condition with a schedule of pull points. Each pull's quality order is due a set number of days before the pull."
        actions={
          <Link className={buttonClassName("primary")} to="/quality/stability/new">
            Plan a study
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="studies-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value)}>
              <option value="">All statuses</option>
              {STUDY_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {STUDY_STATUS_LABELS[s].label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load stability studies" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Stability studies"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={
            status
              ? { title: "No studies with this status", description: "Choose all statuses to see every study." }
              : { title: "No stability studies yet", description: "Plan a study for a batch to schedule its pull points." }
          }
        />
      )}
    </div>
  );
}

export interface StudyValues {
  itemId: string;
  batchId: string;
  protocol: string;
  condition: string;
  startDate: string;
  testGroupId: string;
  pullMonths: string;
  leadDays: string;
  windowDays: string;
}

type Errors = Partial<Record<keyof StudyValues, string>>;

/** Checks that mirror the service's ValidateStudy, and the request the form sends. */
export function validateStudy(values: StudyValues): { errors: Errors; input?: NewStudy } {
  const errors: Errors = {};
  if (!UUID.test(values.itemId.trim())) errors.itemId = "Enter the item id (a UUID).";
  const batch = values.batchId.trim();
  if (batch === "" || batch.length > 60) errors.batchId = "Enter the batch, up to 60 characters.";
  const protocol = values.protocol.trim();
  if (protocol === "" || protocol.length > 120) errors.protocol = "Enter the protocol, up to 120 characters.";
  const condition = values.condition.trim();
  if (condition === "" || condition.length > 60) errors.condition = "Enter the storage condition, such as 25C/60RH.";
  if (!/^\d{4}-\d{2}-\d{2}$/.test(values.startDate)) errors.startDate = "Enter the start date.";
  if (!UUID.test(values.testGroupId)) errors.testGroupId = "Choose the test group the pulls are tested with.";
  const parts = values.pullMonths.split(/[\s,]+/).filter(Boolean);
  const months = parts.map(Number);
  if (parts.length === 0 || parts.length > 40 || parts.some((p) => !/^\d{1,3}$/.test(p)) || months.some((m, i) => m > 120 || (i > 0 && m <= months[i - 1]))) {
    errors.pullMonths = "Enter 1 to 40 months from 0 to 120 in ascending order, such as 0, 3, 6, 12.";
  }
  const lead = values.leadDays.trim();
  if (!/^\d{1,2}$/.test(lead) || Number(lead) > 30) errors.leadDays = "Enter 0 to 30 days.";
  const window = values.windowDays.trim();
  if (!/^\d{1,2}$/.test(window) || Number(window) > 14) errors.windowDays = "Enter 0 to 14 days.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      itemId: values.itemId.trim(),
      batchId: batch,
      protocol,
      condition,
      startDate: values.startDate,
      testGroupId: values.testGroupId,
      pullMonths: months,
      leadDays: Number(lead),
      windowDays: Number(window),
    },
  };
}

const fieldNames: (keyof StudyValues)[] = ["itemId", "batchId", "protocol", "condition", "startDate", "testGroupId", "pullMonths", "leadDays", "windowDays"];

/** Plan a stability study; the service generates the pull schedule (BR-33.14). */
export function NewStudyPage() {
  const navigate = useNavigate();
  const groups = useLoad((signal) => listTestGroups(signal), "study-test-groups");
  const [values, setValues] = useState<StudyValues>({
    itemId: "",
    batchId: "",
    protocol: "ICH Q1A(R2)",
    condition: ICH_CONDITIONS[0],
    startDate: "",
    testGroupId: "",
    pullMonths: DEFAULT_PULLS,
    leadDays: "7",
    windowDays: "3",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<StudyValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const checked = validateStudy(values);
    setErrors(checked.errors);
    setFailure(null);
    if (!checked.input) return;
    setBusy(true);
    try {
      const study = await createStudy(checked.input, key.current);
      navigate(`/quality/stability/${study.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of fieldNames) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.code === "QM-0140") fieldErrors.testGroupId = "This test group no longer exists.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const text = (id: keyof StudyValues, label: string, hint?: string, extra?: Record<string, unknown>) => (
    <FormField id={`study-${id}`} label={label} hint={hint} error={errors[id]}>
      {(control) => <Input {...control} {...extra} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value })} />}
    </FormField>
  );

  const testGroups: TestGroup[] = groups.data ?? [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Plan a stability study"
        description="Pull dates count from the start date and keep its day of the month, clamped to the month end (31 January + 3 months = 30 April)."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Plan a stability study">
          {text("itemId", "Item id", undefined, { className: styles.mono })}
          {text("batchId", "Batch", undefined, { maxLength: 60 })}
          {text("protocol", "Protocol", undefined, { maxLength: 120 })}
          <FormField id="study-condition" label="Storage condition" hint="ICH Q1A(R2): 25C/60RH long term, 40C/75RH accelerated." error={errors.condition}>
            {(control) => (
              <Select {...control} value={values.condition} onChange={(event) => update({ condition: event.target.value })}>
                {ICH_CONDITIONS.map((c) => (
                  <option key={c} value={c}>
                    {c}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("startDate", "Start date", undefined, { type: "date" })}
          <FormField
            id="study-testGroupId"
            label="Test group"
            hint={groups.error ? "Test groups could not be loaded." : undefined}
            error={errors.testGroupId}
          >
            {(control) => (
              <Select {...control} value={values.testGroupId} invalid={Boolean(errors.testGroupId)} onChange={(event) => update({ testGroupId: event.target.value })}>
                <option value="">{groups.loading ? "Loading test groups…" : "Choose a test group"}</option>
                {testGroups.map((g) => (
                  <option key={g.id} value={g.id}>{`${g.code} v${g.version}`}</option>
                ))}
              </Select>
            )}
          </FormField>
          {text("pullMonths", "Pull points (months)", "Ascending, comma-separated.")}
          {text("leadDays", "Order lead time (days)", "stability_lead_days, default 7.", { inputMode: "numeric" })}
          {text("windowDays", "Late-pull window (days)", "stability_pull_window_days, default 3.", { inputMode: "numeric" })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not plan the study" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Plan study
            </Button>
            <Link className={buttonClassName("ghost")} to="/quality/stability">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

const pullColumns: DataTableColumn<Pull>[] = [
  { key: "months", header: "Months", align: "end" },
  { key: "pullDate", header: "Pull date", render: (row) => formatDate(row.pullDate) },
  { key: "orderDate", header: "Quality order due", render: (row) => formatDate(row.orderDate) },
  { key: "testGroupId", header: "Test group", mono: true, render: (row) => shortId(row.testGroupId) },
  {
    key: "status",
    header: "Status",
    render: (row) => {
      const s = PULL_STATUS_LABELS[row.status] ?? { label: row.status, tone: "neutral" as const };
      return <Badge tone={s.tone}>{s.label}</Badge>;
    },
  },
];

/** One study with its pull schedule. */
export function StudyDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getStudy(id, signal), `study|${id}`);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Stability study" />
        <Alert tone="danger" title="Could not load the study" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Stability study" />
        <span className="visually-hidden">Loading the study…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const study = state.data;
  const next = nextPull(study);
  const detail = (term: string, value: string, mono = false) => (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
    </div>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Stability study ${shortId(study.id)}`}
        description={`${study.protocol}, ${study.condition}`}
        meta={<StudyStatusBadge status={study.status} />}
        actions={
          <Link className={buttonClassName("secondary")} to="/quality/stability">
            All studies
          </Link>
        }
      />
      <StatGroup label="Schedule summary">
        <StatTile label="Pull points" value={study.pulls.length} />
        <StatTile label="Last pull" value={study.pulls.length ? `${study.pulls[study.pulls.length - 1].months} months` : dash} />
        <StatTile label="Next pull" value={next ? formatDate(next.pullDate) : dash} />
      </StatGroup>
      <Card title="Study">
        <dl className={styles.details}>
          {detail("Item", study.itemId, true)}
          {detail("Batch", study.batchId, true)}
          {detail("Start date", formatDate(study.startDate))}
          {detail("Test group", study.testGroupId, true)}
          {detail("Order lead time", `${study.leadDays} days before each pull`)}
          {detail("Late-pull window", `${study.windowDays} days`)}
          {detail("Planned", formatDateTime(study.createdAt))}
        </dl>
      </Card>
      <Card title="Pull schedule">
        <DataTable label="Pull schedule" columns={pullColumns} rows={study.pulls} getRowKey={(row) => String(row.months)} empty={{ title: "No pull points" }} />
      </Card>
    </div>
  );
}
