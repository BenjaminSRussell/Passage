import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  StatusPill,
  type DataTableColumn,
} from "../../shell/ui";
import {
  ACTIVITY_KINDS,
  activateFlow,
  createFlow,
  describeProblem,
  getFlow,
  listFlows,
  newFlowVersion,
  newKey,
  type Activity,
  type ActivityKind,
  type Flow,
  type FlowStatus,
  type NewFlow,
  type Problem,
} from "./api/client";
import { dash, formatDateTime, shortId, today, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const listColumns: DataTableColumn<Flow>[] = [
  { key: "name", header: "Flow" },
  { key: "flowVersion", header: "Version", align: "end" },
  { key: "status", header: "Status", render: (f) => <StatusPill status={f.status} /> },
  { key: "activityCount", header: "Activities", align: "end" },
  { key: "taktTimeS", header: "Takt (s)", align: "end", render: (f) => f.taktTimeS ?? dash },
  { key: "activeFrom", header: "Active from", render: (f) => f.activeFrom ?? dash },
  { key: "valueStreamId", header: "Value stream", mono: true, render: (f) => shortId(f.valueStreamId) },
];

/** Production flows of the value streams (FR-24.8). */
export function FlowsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<FlowStatus | "">("");
  const state = useLoad((signal) => listFlows(status, signal), `flows|${status}`);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Production flows"
        description="The activities a value stream runs, with cycle times and locations. A flow goes live once every process activity has an output."
        actions={
          <Link className={buttonClassName("primary")} to="/lean/flows/new">
            New flow
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="flow-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as FlowStatus | "")}>
              <option value="">All statuses</option>
              <option value="draft">Draft</option>
              <option value="active">Active</option>
              <option value="expired">Expired</option>
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load production flows" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Production flows"
          columns={listColumns}
          rows={state.data ?? []}
          getRowKey={(f) => f.id}
          loading={state.loading}
          empty={
            status
              ? { title: "No flows with this status", description: "Choose another status, or all statuses." }
              : { title: "No production flows yet", description: "Create a flow for a value stream, then activate it." }
          }
          onRowOpen={(f) => navigate(`/lean/flows/${f.id}`)}
        />
      )}
    </div>
  );
}

interface ActivityDraft {
  name: string;
  kind: ActivityKind;
  cycleTimeS: string;
  outputLocationId: string;
  predecessors: string;
  pickingRequired: boolean;
}

interface FlowValues {
  valueStreamId: string;
  siteId: string;
  name: string;
  taktTimeS: string;
  activities: ActivityDraft[];
}

type FlowErrors = Partial<Record<"valueStreamId" | "siteId" | "name" | "taktTimeS" | "activities", string>> & { activity?: Record<string, string> };

function blankActivity(): ActivityDraft {
  return { name: "", kind: "process", cycleTimeS: "0", outputLocationId: "", predecessors: "", pickingRequired: false };
}

/** Checks that mirror the service, and the request the form produces. Activities are numbered in order. */
export function validateFlow(values: FlowValues): { errors: FlowErrors; input?: NewFlow } {
  const errors: FlowErrors = {};
  const activity: Record<string, string> = {};
  if (!UUID.test(values.valueStreamId.trim())) errors.valueStreamId = "Enter the value stream's operating-unit id (a UUID).";
  if (!UUID.test(values.siteId.trim())) errors.siteId = "Enter the site id (a UUID).";
  if (values.name.trim().length < 1 || values.name.trim().length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (values.taktTimeS !== "" && !/^[1-9]\d{0,6}$/.test(values.taktTimeS)) errors.taktTimeS = "Enter whole seconds, 1 or more.";
  if (values.activities.length === 0) errors.activities = "Add at least one activity.";
  const count = values.activities.length;
  const parsed = values.activities.map((a, i) => {
    if (a.name.trim().length < 1 || a.name.trim().length > 80) activity[`${i}.name`] = "Enter a name of up to 80 characters.";
    if (!/^\d{1,7}$/.test(a.cycleTimeS)) activity[`${i}.cycleTimeS`] = "Enter whole seconds.";
    if (a.outputLocationId.trim() !== "" && !UUID.test(a.outputLocationId.trim())) activity[`${i}.outputLocationId`] = "Enter a location id (a UUID).";
    const preds = a.predecessors
      .split(",")
      .map((p) => p.trim())
      .filter((p) => p !== "");
    const numbers = preds.map(Number);
    if (preds.some((p) => !/^\d+$/.test(p)) || numbers.some((n) => n < 1 || n > count || n === i + 1)) {
      activity[`${i}.predecessors`] = `List other activity numbers (1 to ${count}), separated by commas.`;
    }
    return { ...a, numbers };
  });
  if (Object.keys(activity).length > 0) errors.activity = activity;
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      valueStreamId: values.valueStreamId.trim(),
      siteId: values.siteId.trim(),
      name: values.name.trim(),
      taktTimeS: values.taktTimeS === "" ? null : Number(values.taktTimeS),
      activities: parsed.map((a, i) => ({
        sequence: i + 1,
        name: a.name.trim(),
        kind: a.kind,
        cycleTimeS: Number(a.cycleTimeS),
        outputLocationId: a.outputLocationId.trim() === "" ? null : a.outputLocationId.trim(),
        predecessors: a.numbers,
        pickingRequired: a.pickingRequired,
      })),
    },
  };
}

/** Production flow designer: activities in order, each with kind, cycle time, output and predecessors. */
export function NewFlowPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<FlowValues>({ valueStreamId: "", siteId: "", name: "", taktTimeS: "", activities: [blankActivity()] });
  const [errors, setErrors] = useState<FlowErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<FlowValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  function updateActivity(index: number, patch: Partial<ActivityDraft>) {
    update({ activities: values.activities.map((a, i) => (i === index ? { ...a, ...patch } : a)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateFlow(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const flow = await createFlow(input, key.current);
      navigate(`/lean/flows/${flow.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "LEAN-0105") setErrors({ name: problem.message });
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const activityError = (i: number, field: string) => errors.activity?.[`${i}.${field}`];

  return (
    <div className={styles.stack}>
      <PageHeader title="New production flow" description="The flow starts as a draft. Activate it once every process activity has an output location." />
      <form className={styles.stack} onSubmit={submit} noValidate>
        <Card title="Flow">
          <div className={styles.form}>
            <FormField id="flow-name" label="Name" error={errors.name}>
              {(c) => <Input {...c} value={values.name} maxLength={120} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
            </FormField>
            <FormField id="flow-stream" label="Value stream id" hint="The wave-6 operating unit of type value stream." error={errors.valueStreamId}>
              {(c) => <Input {...c} className={styles.mono} value={values.valueStreamId} onChange={(e) => update({ valueStreamId: e.target.value })} invalid={Boolean(errors.valueStreamId)} />}
            </FormField>
            <FormField id="flow-site" label="Site" error={errors.siteId}>
              {(c) => <EntityLookup entity="site" {...c} value={values.siteId} onChange={(e) => update({ siteId: e.target.value })} invalid={Boolean(errors.siteId)} />}
            </FormField>
            <FormField id="flow-takt" label="Takt time (seconds)" optional error={errors.taktTimeS}>
              {(c) => <Input {...c} inputMode="numeric" value={values.taktTimeS} onChange={(e) => update({ taktTimeS: e.target.value })} invalid={Boolean(errors.taktTimeS)} />}
            </FormField>
          </div>
        </Card>
        <Card
          title="Activities"
          actions={
            <Button size="sm" onClick={() => update({ activities: [...values.activities, blankActivity()] })}>
              Add activity
            </Button>
          }
        >
          <div className={styles.stack}>
            {values.activities.map((a, i) => (
              <fieldset key={i} className={styles.lineRow}>
                <legend className={styles.groupsLegend}>{`Activity ${i + 1}`}</legend>
                <FormField id={`act-${i}-name`} label="Activity name" error={activityError(i, "name")}>
                  {(c) => <Input {...c} value={a.name} maxLength={80} onChange={(e) => updateActivity(i, { name: e.target.value })} invalid={Boolean(activityError(i, "name"))} />}
                </FormField>
                <FormField id={`act-${i}-kind`} label="Kind">
                  {(c) => (
                    <Select {...c} value={a.kind} onChange={(e) => updateActivity(i, { kind: e.target.value as ActivityKind })}>
                      {ACTIVITY_KINDS.map((k) => (
                        <option key={k} value={k}>
                          {k}
                        </option>
                      ))}
                    </Select>
                  )}
                </FormField>
                <FormField id={`act-${i}-cycle`} label="Cycle time (seconds)" error={activityError(i, "cycleTimeS")}>
                  {(c) => <Input {...c} inputMode="numeric" value={a.cycleTimeS} onChange={(e) => updateActivity(i, { cycleTimeS: e.target.value })} invalid={Boolean(activityError(i, "cycleTimeS"))} />}
                </FormField>
                <FormField id={`act-${i}-output`} label="Output location id" optional error={activityError(i, "outputLocationId")}>
                  {(c) => (
                    <Input
                      {...c}
                      className={styles.mono}
                      value={a.outputLocationId}
                      onChange={(e) => updateActivity(i, { outputLocationId: e.target.value })}
                      invalid={Boolean(activityError(i, "outputLocationId"))}
                    />
                  )}
                </FormField>
                <FormField id={`act-${i}-preds`} label="Predecessors" optional hint="Activity numbers, such as 1, 2." error={activityError(i, "predecessors")}>
                  {(c) => <Input {...c} value={a.predecessors} onChange={(e) => updateActivity(i, { predecessors: e.target.value })} invalid={Boolean(activityError(i, "predecessors"))} />}
                </FormField>
                <div className={styles.lineChecks}>
                  <span className={styles.check}>
                    <Input type="checkbox" id={`act-${i}-picking`} checked={a.pickingRequired} onChange={(e) => updateActivity(i, { pickingRequired: e.target.checked })} />
                    <label htmlFor={`act-${i}-picking`}>Picking required</label>
                  </span>
                  {values.activities.length > 1 ? (
                    <Button size="sm" variant="ghost" onClick={() => update({ activities: values.activities.filter((_, j) => j !== i) })}>
                      {`Remove activity ${i + 1}`}
                    </Button>
                  ) : null}
                </div>
              </fieldset>
            ))}
            {errors.activities ? (
              <p role="alert" className={styles.fieldError}>
                {errors.activities}
              </p>
            ) : null}
          </div>
        </Card>
        {failure ? (
          <Alert tone="danger" title="Could not create the flow" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Create flow
          </Button>
          <Link className={buttonClassName("ghost")} to="/lean/flows">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}

/** The activity numbers an activity waits for. */
function predecessorText(a: Activity): string {
  return a.predecessors.length === 0 ? dash : a.predecessors.join(", ");
}

const activityColumns: DataTableColumn<Activity>[] = [
  { key: "sequence", header: "No.", align: "end" },
  { key: "name", header: "Activity" },
  { key: "kind", header: "Kind", render: (a) => <Badge tone="neutral">{a.kind}</Badge> },
  { key: "cycleTimeS", header: "Cycle (s)", align: "end" },
  { key: "outputLocationId", header: "Output", mono: true, render: (a) => (a.outputLocationId ? shortId(a.outputLocationId) : a.kind === "process" ? "Missing" : dash) },
  { key: "predecessors", header: "After", render: predecessorText },
];

/** One flow: its activities and activation (FR-24.8). */
export function FlowDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getFlow(id, signal), id);
  const [current, setCurrent] = useState<Flow | null>(null);
  const [from, setFrom] = useState(today());
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const [takt, setTakt] = useState("");
  const [taktError, setTaktError] = useState("");
  const [versionFailure, setVersionFailure] = useState<Problem | null>(null);
  const [versionBusy, setVersionBusy] = useState(false);
  const versionKey = useRef(newKey());
  const navigate = useNavigate();

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Production flow" />
        <Alert tone="danger" title="Could not load the flow" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Production flow" />
        <span className="visually-hidden">Loading the flow…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const flow = current && current.version >= state.data.version ? current : state.data;

  async function activate(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setBusy(true);
    try {
      setCurrent(await activateFlow(flow.id, { version: flow.version, activeFrom: from }, key.current));
    } catch (error) {
      setFailure(describeProblem(error));
      key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  async function createVersion(event: FormEvent) {
    event.preventDefault();
    setVersionFailure(null);
    if (takt !== "" && !/^[1-9]\d{0,6}$/.test(takt)) {
      setTaktError("Enter whole seconds, 1 or more.");
      return;
    }
    setTaktError("");
    setVersionBusy(true);
    try {
      const next = await newFlowVersion(flow.id, { version: flow.version, ...(takt === "" ? {} : { taktTimeS: Number(takt) }) }, versionKey.current);
      setTakt("");
      versionKey.current = newKey();
      navigate(`/lean/flows/${next.id}`);
    } catch (error) {
      setVersionFailure(describeProblem(error));
      versionKey.current = newKey();
    } finally {
      setVersionBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={flow.name}
        meta={
          <>
            <StatusPill status={flow.status} />
            <Badge tone="neutral">{`Version ${flow.flowVersion}`}</Badge>
          </>
        }
        actions={
          <>
            <Link className={buttonClassName("secondary")} to={`/lean/rules?flowId=${flow.id}`}>
              Kanban rules
            </Link>
            <Link className={buttonClassName("secondary")} to={`/lean/board?flowId=${flow.id}`}>
              Board
            </Link>
            <Link className={buttonClassName("secondary")} to={`/lean/costing?flowId=${flow.id}`}>
              Costing periods
            </Link>
          </>
        }
      />
      <StatGroup label="Flow facts">
        <StatTile label="Activities" value={flow.activityCount} />
        <StatTile label="Takt (s)" value={flow.taktTimeS ?? dash} />
        <StatTile label="Active from" value={flow.activeFrom ?? dash} />
        <StatTile label="Active to" value={flow.activeTo ?? dash} />
      </StatGroup>
      <DataTable
        label="Flow activities"
        columns={activityColumns}
        rows={flow.activities ?? []}
        getRowKey={(a) => a.id}
        empty={{ title: "No activities", description: "This flow has no activities." }}
      />
      {flow.status === "draft" ? (
        <Card title="Activate">
          <div className={styles.stack}>
            {flow.previousFlowId ? (
              <p className={styles.muted}>
                Activating this version expires the version it replaces the day before the date below, which must be after that version started.
              </p>
            ) : null}
            <form className={styles.toolbar} onSubmit={activate} noValidate>
              <FormField id="activate-from" label="Active from">
                {(c) => <Input {...c} type="date" value={from} onChange={(e) => setFrom(e.target.value)} />}
              </FormField>
              <Button type="submit" variant="primary" loading={busy}>
                Activate flow
              </Button>
            </form>
            {failure ? (
              <Alert tone="danger" title="The flow cannot go live yet" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            ) : null}
          </div>
        </Card>
      ) : null}
      {flow.status === "active" ? (
        <Card title="New version">
          <div className={styles.stack}>
            <p className={styles.muted}>
              A new version starts as a draft copy of these activities. Activating it expires this version the day before it starts.
            </p>
            <form className={styles.toolbar} onSubmit={createVersion} noValidate>
              <FormField id="version-takt" label="Takt time (seconds)" optional hint="Leave empty to keep the current takt time." error={taktError}>
                {(c) => <Input {...c} inputMode="numeric" value={takt} onChange={(e) => { setTakt(e.target.value); versionKey.current = newKey(); }} invalid={Boolean(taktError)} />}
              </FormField>
              <Button type="submit" variant="primary" loading={versionBusy}>
                Create new version
              </Button>
            </form>
            {versionFailure ? (
              <Alert tone="danger" title="Could not create a new version" live="alert">
                {versionFailure.message} ({versionFailure.code})
              </Alert>
            ) : null}
          </div>
        </Card>
      ) : null}
      {flow.previousFlowId ? (
        <p className={styles.muted}>
          Replaces <Link to={`/lean/flows/${flow.previousFlowId}`}>the previous version</Link>.
        </p>
      ) : null}
      <p className={styles.muted}>{`Value stream ${flow.valueStreamId} · site ${flow.siteId} · created ${formatDateTime(flow.createdAt)} · version ${flow.version}`}</p>
    </div>
  );
}
