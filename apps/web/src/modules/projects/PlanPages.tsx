import { useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  Input,
  PageHeader,
  Select,
  Skeleton,
  type DataTableColumn,
} from "../../shell/ui";
import {
  createPlan,
  DEPENDENCY_TYPES,
  describeProblem,
  getWbs,
  listPlans,
  newKey,
  replaceWbs,
  schedulePlan,
  type DependencyType,
  type PlanKind,
  type PlanVersion,
  type Problem,
  type ScheduleView,
  type WbsNode,
  type WbsNodeInput,
  type WbsView,
} from "./api/client";
import { dash, planKindLabel, planKindTone } from "./labels";
import plan from "./plans.module.css";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export function PlanBadge({ version }: { version: PlanVersion }) {
  return <Badge tone={planKindTone[version.kind]}>{planKindLabel[version.kind]}</Badge>;
}

const planTitle = (v: PlanVersion) => `${planKindLabel[v.kind]} plan ${v.sequence}`;

function NotFound({ title, text, back, backLabel }: { title: string; text: string; back: string; backLabel: string }) {
  return (
    <div className={styles.stack}>
      <PageHeader title={title} />
      <Card>
        <EmptyState
          icon="grid"
          title={`${title} not found`}
          description={text}
          action={
            <Link className={buttonClassName("secondary")} to={back}>
              {backLabel}
            </Link>
          }
        />
      </Card>
    </div>
  );
}

interface PlanAction {
  /** Distinguishes the two simulation actions (from WORKING, from BASELINE) for busy state and keys. */
  id: string;
  kind: PlanKind;
  label: string;
  from?: PlanVersion;
}

/**
 * Plan versions of a project (FR-41.2): one working plan, one baseline copied
 * from it and locked, and simulations copied from either. Opens the WBS of a version.
 */
export function ProjectPlansPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => listPlans(id, signal), id);
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  // One key per action until it succeeds, so a retry after a lost response replays.
  const keys = useRef<Record<string, string>>({});

  if (state.error?.status === 404) {
    return <NotFound title="Project" text={`There is no project ${id} in your legal entities.`} back="/projects/list" backLabel="Back to projects" />;
  }
  const plans = state.data?.items ?? [];
  const working = plans.find((p) => p.kind === "WORKING");
  // The current baseline: an applied change order supersedes the earlier ones (BR-41.13).
  const baseline = plans.find((p) => p.kind === "BASELINE" && !p.supersededByVersionId);
  const actions: PlanAction[] = [];
  if (!working) actions.push({ id: "working", kind: "WORKING", label: "Create working plan" });
  if (working && !baseline) actions.push({ id: "baseline", kind: "BASELINE", label: "Save as baseline", from: working });
  if (working) actions.push({ id: "sim-working", kind: "SIMULATION", label: "New simulation", from: working });
  if (baseline) actions.push({ id: "sim-baseline", kind: "SIMULATION", label: "New simulation from baseline", from: baseline });

  async function run(action: PlanAction) {
    setBusy(action.id);
    setFailure(null);
    const key = (keys.current[action.id] ??= newKey());
    try {
      const created = await createPlan(id, { kind: action.kind, ...(action.from ? { copyFromVersionId: action.from.id } : {}) }, key);
      delete keys.current[action.id];
      navigate(`/projects/${encodeURIComponent(id)}/plans/${encodeURIComponent(created.id)}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "idempotency_key_reuse" || problem.status === 422) delete keys.current[action.id];
      setFailure(problem);
      state.reload();
    } finally {
      setBusy(null);
    }
  }

  const bySequence = new Map(plans.map((p) => [p.id, p.sequence]));
  const columns: DataTableColumn<PlanVersion>[] = [
    { key: "sequence", header: "Plan", render: (p) => planTitle(p) },
    { key: "kind", header: "Kind", render: (p) => <PlanBadge version={p} /> },
    { key: "locked", header: "Changes", render: (p) => (p.transferredAt ? `Transferred ${p.transferredAt.slice(0, 10)}` : p.locked ? "Locked" : "Editable") },
    { key: "copiedFromVersionId", header: "Copied from", render: (p) => (p.copiedFromVersionId ? `Plan ${bySequence.get(p.copiedFromVersionId) ?? dash}` : dash) },
    { key: "version", header: "Version", align: "end" },
    { key: "createdAt", header: "Created", mono: true, render: (p) => p.createdAt.slice(0, 10) },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Plans for ${id}`}
        description="The working plan holds the WBS you edit and schedule. Saving a baseline copies it and locks the copy; simulations are copies you can change freely."
        actions={
          <Link className={buttonClassName("secondary")} to={`/projects/${encodeURIComponent(id)}`}>
            Back to project
          </Link>
        }
      />
      {failure ? (
        <Alert tone="danger" title="Could not create the plan version" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load the plan versions" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : !state.loading && plans.length === 0 ? (
        <Card>
          <EmptyState
            icon="grid"
            title="No plan versions yet"
            description="Create the working plan to build the work breakdown structure and schedule it."
            action={
              <Button variant="primary" loading={busy === "working"} onClick={() => void run({ id: "working", kind: "WORKING", label: "" })}>
                Create working plan
              </Button>
            }
          />
        </Card>
      ) : (
        <>
          {actions.length > 0 && !state.loading ? (
            <div className={plan.toolbar} role="group" aria-label="Plan actions">
              {actions.map((a) => (
                <Button
                  key={a.id}
                  variant={a.kind === "WORKING" ? "primary" : "secondary"}
                  loading={busy === a.id}
                  disabled={busy !== null && busy !== a.id}
                  onClick={() => void run(a)}
                >
                  {a.label}
                </Button>
              ))}
            </div>
          ) : null}
          <DataTable
            label="Plan versions"
            columns={columns}
            rows={plans}
            getRowKey={(p) => p.id}
            loading={state.loading}
            onRowOpen={(p) => navigate(`/projects/${encodeURIComponent(id)}/plans/${encodeURIComponent(p.id)}`)}
          />
        </>
      )}
    </div>
  );
}

interface NodeRow {
  key: string;
  wbsCode: string;
  name: string;
  durationDays: string;
  effortHours: string;
  startNoEarlierThan: string;
  isBillableMilestone: boolean;
}

interface DepRow {
  key: string;
  predecessor: string;
  successor: string;
  type: DependencyType;
  lagDays: string;
}

let rowSeq = 0;
const rowKey = () => `row-${++rowSeq}`;

function rowsFrom(view: WbsView): { nodes: NodeRow[]; deps: DepRow[] } {
  const codeOf = new Map(view.nodes.map((n) => [n.id, n.wbsCode]));
  return {
    nodes: view.nodes.map((n) => ({
      key: rowKey(),
      wbsCode: n.wbsCode,
      name: n.name,
      durationDays: String(n.durationDays),
      effortHours: n.effortHours ?? "",
      startNoEarlierThan: n.constraintType === "SNET" ? (n.constraintDate ?? "") : "",
      isBillableMilestone: n.isBillableMilestone,
    })),
    deps: view.dependencies.map((d) => ({
      key: rowKey(),
      predecessor: codeOf.get(d.predecessorId) ?? "",
      successor: codeOf.get(d.successorId) ?? "",
      type: d.type,
      lagDays: String(d.lagDays),
    })),
  };
}

/** The parent of a dotted code is its prefix (1.2 → 1) when that node exists. */
export function parentCode(code: string, codes: Set<string>): string | undefined {
  const cut = code.lastIndexOf(".");
  if (cut < 0) return undefined;
  const parent = code.slice(0, cut);
  return codes.has(parent) ? parent : undefined;
}

/** Turns the editor rows into the PUT body (parents follow the code), or row errors keyed like the service's. */
export function wbsBody(nodes: NodeRow[], deps: DepRow[]): { errors: Record<string, string>; body?: { nodes: WbsNodeInput[]; dependencies: { predecessorWbsCode: string; successorWbsCode: string; type: DependencyType; lagDays: number }[] } } {
  const errors: Record<string, string> = {};
  const codes = new Set(nodes.map((n) => n.wbsCode.trim()));
  const out: WbsNodeInput[] = nodes.map((n, i) => {
    const code = n.wbsCode.trim();
    if (!/^\d{1,4}(\.\d{1,4}){0,7}$/.test(code)) errors[`nodes[${i}].wbsCode`] = "Use dotted numbers such as 1.2.";
    if (n.name.trim() === "") errors[`nodes[${i}].name`] = "Enter a name.";
    if (!/^\d+$/.test(n.durationDays.trim())) errors[`nodes[${i}].durationDays`] = "Enter whole working days, 0 for a milestone.";
    if (n.effortHours.trim() !== "" && !/^\d{1,10}(\.\d{1,2})?$/.test(n.effortHours.trim())) errors[`nodes[${i}].effortHours`] = "Enter hours with at most 2 decimals.";
    const parent = parentCode(code, codes);
    return {
      wbsCode: code,
      name: n.name.trim(),
      durationDays: Number.parseInt(n.durationDays.trim() || "0", 10),
      isBillableMilestone: n.isBillableMilestone,
      ...(parent ? { parentWbsCode: parent } : {}),
      ...(n.effortHours.trim() ? { effortHours: n.effortHours.trim() } : {}),
      ...(n.startNoEarlierThan ? { constraintType: "SNET" as const, constraintDate: n.startNoEarlierThan } : { constraintType: "ASAP" as const }),
    };
  });
  const dependencies = deps.map((d, i) => {
    if (!/^-?\d+$/.test(d.lagDays.trim())) errors[`dependencies[${i}].lagDays`] = "Enter whole calendar days.";
    return { predecessorWbsCode: d.predecessor.trim(), successorWbsCode: d.successor.trim(), type: d.type, lagDays: Number.parseInt(d.lagDays.trim() || "0", 10) };
  });
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, body: { nodes: out, dependencies } };
}

/** "nodes[1].name" → "Task 2 (name)"; "dependencies[0]" → "Dependency 1". */
export function fieldLabel(key: string): string {
  const m = /^(nodes|dependencies)\[(\d+)\](?:\.(\w+))?$/.exec(key);
  if (!m) return key;
  const what = `${m[1] === "nodes" ? "Task" : "Dependency"} ${Number(m[2]) + 1}`;
  return m[3] ? `${what} (${m[3]})` : what;
}

const depth = (code: string) => code.split(".").length - 1;
const indentClass = (code: string) => [undefined, plan.indent1, plan.indent2, plan.indent3][Math.min(depth(code), 3)];

/**
 * WBS of one plan version (FR-41.2): the scheduled tasks with early and late
 * dates, slack and the critical path, an editor that replaces the whole WBS
 * under the plan's version, and the scheduler run. A baseline is read-only.
 */
export function WbsPage() {
  const { id = "", planId = "" } = useParams();
  const state = useLoad((signal) => getWbs(id, planId, signal), `${id}|${planId}`);
  const [latest, setLatest] = useState<WbsView | ScheduleView | null>(null);
  const view = latest && latest.planVersion.id === planId ? latest : state.data;
  const [nodes, setNodes] = useState<NodeRow[]>([]);
  const [deps, setDeps] = useState<DepRow[]>([]);
  const [dirty, setDirty] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<"save" | "schedule" | null>(null);

  useEffect(() => {
    if (!view || dirty) return;
    const rows = rowsFrom(view);
    setNodes(rows.nodes);
    setDeps(rows.deps);
    // Rebuild the editor only when a new version of the plan arrives (not on unsaved edits).
  }, [view?.planVersion.id, view?.planVersion.version, dirty]);

  if (state.error && !view) {
    if (state.error.status === 404) {
      return <NotFound title="Plan version" text="This plan version does not exist for the project." back={`/projects/${encodeURIComponent(id)}/plans`} backLabel="Back to plans" />;
    }
    return (
      <div className={styles.stack}>
        <PageHeader title="Plan version" />
        <Alert tone="danger" title="Could not load the plan version" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!view) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Plan version" />
        <span className="visually-hidden">Loading the plan version…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const current = view;
  const locked = current.planVersion.locked;

  function edit<T>(setter: (fn: (rows: T[]) => T[]) => void, key: string, patch: Partial<T>) {
    setter((rows) => rows.map((r) => ((r as { key: string }).key === key ? { ...r, ...patch } : r)));
    setDirty(true);
  }

  function addNode() {
    const top = nodes.filter((n) => !n.wbsCode.includes(".")).map((n) => Number.parseInt(n.wbsCode, 10) || 0);
    const next = String((top.length ? Math.max(...top) : 0) + 1);
    setNodes((rows) => [...rows, { key: rowKey(), wbsCode: next, name: "", durationDays: "1", effortHours: "", startNoEarlierThan: "", isBillableMilestone: false }]);
    setDirty(true);
  }

  function discard() {
    const rows = rowsFrom(current);
    setNodes(rows.nodes);
    setDeps(rows.deps);
    setErrors({});
    setFailure(null);
    setDirty(false);
  }

  function reload() {
    setLatest(null);
    setDirty(false);
    setFailure(null);
    setErrors({});
    state.reload();
  }

  async function save() {
    const { errors: found, body } = wbsBody(nodes, deps);
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (!body) return;
    setBusy("save");
    try {
      const out = await replaceWbs(id, planId, body, current.planVersion.version);
      setDirty(false);
      setLatest(out);
      setNotice(`The WBS is saved (version ${out.planVersion.version}). Run the schedule to compute dates.`);
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(null);
    }
  }

  async function schedule() {
    setBusy("schedule");
    setFailure(null);
    setNotice(null);
    try {
      const out = await schedulePlan(id, planId);
      setLatest(out);
      setNotice(
        out.finishDate
          ? `Scheduled. The plan finishes on ${out.finishDate}; ${out.criticalPath.length} ${out.criticalPath.length === 1 ? "task is" : "tasks are"} critical.`
          : "There are no tasks to schedule yet.",
      );
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const stale = failure?.code === "conflict_version";
  const summaries = new Set(current.nodes.filter((n) => n.parentId).map((n) => n.parentId!));
  const columns: DataTableColumn<WbsNode>[] = [
    { key: "wbsCode", header: "Code", mono: true, render: (n) => <span className={indentClass(n.wbsCode)}>{n.wbsCode}</span> },
    { key: "name", header: "Name", render: (n) => <span className={summaries.has(n.id) ? plan.summary : undefined}>{n.name}</span> },
    { key: "durationDays", header: "Days", align: "end", render: (n) => (summaries.has(n.id) ? dash : n.durationDays) },
    { key: "effortHours", header: "Effort (h)", align: "end", render: (n) => n.effortHours ?? dash },
    { key: "earlyStart", header: "Early start", mono: true, render: (n) => n.earlyStart ?? dash },
    { key: "earlyFinish", header: "Early finish", mono: true, render: (n) => n.earlyFinish ?? dash },
    { key: "lateFinish", header: "Late finish", mono: true, render: (n) => n.lateFinish ?? dash },
    { key: "slackDays", header: "Slack (days)", align: "end", render: (n) => n.slackDays ?? dash },
    { key: "critical", header: "Critical", render: (n) => (n.critical ? <Badge tone="danger">Critical</Badge> : n.critical === false ? "No" : dash) },
  ];
  const errorList = Object.entries(errors);
  const rowError = (key: string) => (errors[key] ? <p className={plan.rowError}>{errors[key]}</p> : null);

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${planTitle(current.planVersion)} for ${id}`}
        meta={<PlanBadge version={current.planVersion} />}
        description={
          current.planVersion.transferredAt
            ? "This simulation was transferred into the working plan and is locked. Change the working plan instead."
            : locked
              ? "This baseline is locked. It keeps the dates it had when it was saved; change the working plan instead."
              : "Edit the tasks and their links, save, then run the schedule. Dates use the Monday-to-Friday calendar from the project start."
        }
        actions={
          <>
            <Link className={buttonClassName("secondary")} to={`/projects/${encodeURIComponent(id)}/plans`}>
              All plans
            </Link>
            {current.planVersion.kind === "SIMULATION" && !locked ? (
              <Link className={buttonClassName("secondary")} to={`/projects/${encodeURIComponent(id)}/plans/${encodeURIComponent(planId)}/transfer`}>
                Transfer to working plan
              </Link>
            ) : null}
            {!locked ? (
              <Button variant="primary" loading={busy === "schedule"} disabled={dirty || busy === "save"} onClick={() => void schedule()}>
                Run schedule
              </Button>
            ) : null}
          </>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert
          tone="danger"
          title={stale ? "Someone else changed this plan" : "Could not update the plan"}
          live="alert"
          action={stale ? <Button size="sm" onClick={reload}>Reload plan</Button> : undefined}
        >
          {stale ? "Reload to see the latest WBS; your unsaved changes are discarded." : <ProblemMessage problem={failure} />}
          {!stale && errorList.length > 0 ? (
            <ul className={plan.errorList}>
              {errorList.map(([k, v]) => (
                <li key={k}>
                  {fieldLabel(k)}: {v}
                </li>
              ))}
            </ul>
          ) : null}
        </Alert>
      ) : null}
      <DataTable
        label="Schedule"
        columns={columns}
        rows={current.nodes}
        getRowKey={(n) => n.id}
        empty={{ title: "No tasks yet", description: locked ? "This baseline has no tasks." : "Add tasks in the editor below and save." }}
      />
      {!locked ? (
        <Card
          title="Edit WBS"
          actions={
            <div className={plan.toolbar}>
              <Button size="sm" onClick={addNode}>
                Add task
              </Button>
              <Button
                size="sm"
                disabled={nodes.length < 2}
                onClick={() => {
                  setDeps((rows) => [...rows, { key: rowKey(), predecessor: "", successor: "", type: "FS", lagDays: "0" }]);
                  setDirty(true);
                }}
              >
                Add dependency
              </Button>
            </div>
          }
        >
          <div className={styles.stack}>
            <p className={styles.note}>
              A task's code places it in the tree: 1.2 sits under 1. A task with children is a summary; its dates come from its children, so link tasks only.
            </p>
            <div className={plan.scroll}>
              <table className={plan.grid} aria-label="WBS tasks">
                <thead>
                  <tr>
                    <th scope="col" className={plan.head}>Code</th>
                    <th scope="col" className={plan.head}>Name</th>
                    <th scope="col" className={plan.head}>Days</th>
                    <th scope="col" className={plan.head}>Effort (h)</th>
                    <th scope="col" className={plan.head}>Start no earlier than</th>
                    <th scope="col" className={plan.head}>Billable milestone</th>
                    <th scope="col" className={plan.head}>
                      <span className="visually-hidden">Remove</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {nodes.map((n, i) => (
                    <tr key={n.key}>
                      <td className={plan.cell}>
                        <Input aria-label={`Code of task ${i + 1}`} className={`${plan.code} ${styles.mono}`} value={n.wbsCode} invalid={Boolean(errors[`nodes[${i}].wbsCode`])} onChange={(e) => edit(setNodes, n.key, { wbsCode: e.target.value })} />
                        {rowError(`nodes[${i}].wbsCode`)}
                      </td>
                      <td className={plan.cell}>
                        <Input aria-label={`Name of task ${i + 1}`} value={n.name} invalid={Boolean(errors[`nodes[${i}].name`])} onChange={(e) => edit(setNodes, n.key, { name: e.target.value })} />
                        {rowError(`nodes[${i}].name`)}
                      </td>
                      <td className={plan.cell}>
                        <Input aria-label={`Days of task ${i + 1}`} className={plan.number} inputMode="numeric" value={n.durationDays} invalid={Boolean(errors[`nodes[${i}].durationDays`])} onChange={(e) => edit(setNodes, n.key, { durationDays: e.target.value })} />
                        {rowError(`nodes[${i}].durationDays`)}
                      </td>
                      <td className={plan.cell}>
                        <Input aria-label={`Effort of task ${i + 1}`} className={plan.number} inputMode="decimal" value={n.effortHours} invalid={Boolean(errors[`nodes[${i}].effortHours`])} onChange={(e) => edit(setNodes, n.key, { effortHours: e.target.value })} />
                        {rowError(`nodes[${i}].effortHours`)}
                      </td>
                      <td className={plan.cell}>
                        <Input aria-label={`Start no earlier than for task ${i + 1}`} className={plan.date} type="date" value={n.startNoEarlierThan} invalid={Boolean(errors[`nodes[${i}].constraintDate`])} onChange={(e) => edit(setNodes, n.key, { startNoEarlierThan: e.target.value })} />
                        {rowError(`nodes[${i}].constraintDate`)}
                      </td>
                      <td className={plan.cell}>
                        <Input aria-label={`Task ${i + 1} is a billable milestone`} type="checkbox" checked={n.isBillableMilestone} onChange={(e) => edit(setNodes, n.key, { isBillableMilestone: e.target.checked })} />
                      </td>
                      <td className={plan.cell}>
                        <Button
                          size="sm"
                          variant="ghost"
                          aria-label={`Remove task ${i + 1}`}
                          onClick={() => {
                            setNodes((rows) => rows.filter((r) => r.key !== n.key));
                            setDirty(true);
                          }}
                        >
                          Remove
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {deps.length > 0 ? (
              <div className={plan.scroll}>
                <table className={plan.grid} aria-label="Dependencies">
                  <thead>
                    <tr>
                      <th scope="col" className={plan.head}>Predecessor</th>
                      <th scope="col" className={plan.head}>Successor</th>
                      <th scope="col" className={plan.head}>Type</th>
                      <th scope="col" className={plan.head}>Lag (calendar days)</th>
                      <th scope="col" className={plan.head}>
                        <span className="visually-hidden">Remove</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {deps.map((d, i) => (
                      <tr key={d.key}>
                        <td className={plan.cell}>
                          <Input aria-label={`Predecessor of dependency ${i + 1}`} className={`${plan.code} ${styles.mono}`} value={d.predecessor} invalid={Boolean(errors[`dependencies[${i}]`])} onChange={(e) => edit(setDeps, d.key, { predecessor: e.target.value })} />
                          {rowError(`dependencies[${i}]`)}
                        </td>
                        <td className={plan.cell}>
                          <Input aria-label={`Successor of dependency ${i + 1}`} className={`${plan.code} ${styles.mono}`} value={d.successor} onChange={(e) => edit(setDeps, d.key, { successor: e.target.value })} />
                        </td>
                        <td className={plan.cell}>
                          <Select aria-label={`Type of dependency ${i + 1}`} value={d.type} onChange={(e) => edit(setDeps, d.key, { type: e.target.value as DependencyType })}>
                            {DEPENDENCY_TYPES.map((t) => (
                              <option key={t} value={t}>
                                {t}
                              </option>
                            ))}
                          </Select>
                        </td>
                        <td className={plan.cell}>
                          <Input aria-label={`Lag of dependency ${i + 1}`} className={plan.number} inputMode="numeric" value={d.lagDays} invalid={Boolean(errors[`dependencies[${i}].lagDays`])} onChange={(e) => edit(setDeps, d.key, { lagDays: e.target.value })} />
                          {rowError(`dependencies[${i}].lagDays`)}
                        </td>
                        <td className={plan.cell}>
                          <Button
                            size="sm"
                            variant="ghost"
                            aria-label={`Remove dependency ${i + 1}`}
                            onClick={() => {
                              setDeps((rows) => rows.filter((r) => r.key !== d.key));
                              setDirty(true);
                            }}
                          >
                            Remove
                          </Button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : null}
            <div className={styles.actions}>
              <Button variant="primary" loading={busy === "save"} disabled={!dirty || busy === "schedule"} onClick={() => void save()}>
                Save WBS
              </Button>
              <Button variant="ghost" disabled={!dirty} onClick={discard}>
                Discard changes
              </Button>
            </div>
          </div>
        </Card>
      ) : null}
    </div>
  );
}
