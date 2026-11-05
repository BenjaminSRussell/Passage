import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  ConfirmDialog,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  StatGroup,
  StatTile,
  StatusPill,
  Textarea,
  type BadgeTone,
  type DataTableColumn,
} from "../../shell/ui";
import {
  completeStep,
  describeProblem,
  exportFileUrl,
  getPlan,
  newKey,
  rekeyFileUrl,
  rollbackPlan,
  startStep,
  type ExportFile,
  type Problem,
  type RekeyPosting,
  type RollbackStep,
  type RunbookStep,
} from "./api/client";
import { elapsedSince, formatDateTime, formatMinutes, formatOffset, overrunTone, parsePostings, toneLabel, type Tone } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const EXPORT_FILES: { file: ExportFile; label: string }[] = [
  { file: "runbook.csv", label: "Download runbook (CSV)" },
  { file: "rollback-plan.csv", label: "Download rollback plan (CSV)" },
  { file: "go-no-go.csv", label: "Download go/no-go checklist (CSV)" },
  { file: "decisions.csv", label: "Download decisions (CSV)" },
  { file: "reconciliation.csv", label: "Download reconciliation (CSV)" },
];

const WHOLE = /^\d{1,5}$/;

/** The entity filter value for the plan-wide steps of a per-entity runbook. */
const ALL_ENTITY = "__plan__";

/** How often the runbook timer refreshes while a step runs. */
const TICK_MS = 30_000;

const toneBadge: Record<Tone, BadgeTone> = { ok: "success", amber: "warning", red: "danger" };

const running = (step: RunbookStep) => step.status === "PENDING" && Boolean(step.startedAt);

/** Elapsed minutes of a step at nowMs: live from the start while running, the recorded duration when done. */
function elapsedOf(step: RunbookStep, nowMs: number): number | null {
  if (step.status === "DONE") return step.actualDurationMin;
  if (step.startedAt) return elapsedSince(step.startedAt, nowMs);
  return null;
}

const rollbackColumns: DataTableColumn<RollbackStep>[] = [
  { key: "seq", header: "Seq", mono: true },
  { key: "offsetMin", header: "T offset", mono: true, render: (row) => formatOffset(row.offsetMin) },
  { key: "name", header: "Step" },
  { key: "owner", header: "Owner" },
  { key: "plannedDurationMin", header: "Planned (min)", align: "end" },
  { key: "decisionPoint", header: "Decision point", render: (row) => (row.decisionPoint ? <Badge tone="warning">Decision</Badge> : "") },
];

const POSTINGS_HINT =
  "One line per posting since T-0: kind (JOURNAL or DOCUMENT), entity, document, posted at (2027-01-02T10:00:00Z), account, dimensions, currency, amount (credit negative), text. Leave blank if nothing was posted.";

function postingSummary(n: number): string {
  if (n === 0) return "No postings are sent for re-keying.";
  return `${n} posting ${n === 1 ? "line is" : "lines are"} sent for re-keying; lines dated before T-0 are left out.`;
}

function conflictMessage(problem: Problem): string {
  return problem.code === "MIG-0006" ? "Someone changed this plan since you opened it. The plan has been reloaded; check it and try again." : problem.message;
}

/**
 * One cutover runbook: steps with planned against actual, started steps timed live (FR-49.6), the
 * go/no-go checklist, step completion, the generated rollback plan, and the rollback with its
 * re-keying export of the postings since T-0 (FR-49.9).
 */
export function CutoverPlanDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getPlan(id, signal), id);
  const plan = state.data;
  const [selected, setSelected] = useState<RunbookStep | null>(null);
  const [minutes, setMinutes] = useState("");
  const [minutesError, setMinutesError] = useState<string | undefined>();
  const [postingsText, setPostingsText] = useState("");
  const [postingsError, setPostingsError] = useState<string | undefined>();
  const [postings, setPostings] = useState<RekeyPosting[]>([]);
  const [confirming, setConfirming] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [entityFilter, setEntityFilter] = useState("");
  const stepKey = useRef(newKey());
  const startKey = useRef(newKey());
  const rollbackKey = useRef(newKey());
  const [now, setNow] = useState(() => Date.now());
  const anyRunning = plan?.steps?.some(running) ?? false;

  // The runbook timer (FR-49.6): refresh elapsed against planned while a step runs.
  useEffect(() => {
    if (!anyRunning) return undefined;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), TICK_MS);
    return () => clearInterval(timer);
  }, [anyRunning]);

  const overruns = (plan?.steps ?? [])
    .filter(running)
    .map((step) => ({ step, elapsed: elapsedOf(step, now) ?? 0 }))
    .filter(({ step, elapsed }) => overrunTone(step.plannedDurationMin, elapsed) !== "ok");

  const planSteps = plan?.steps ?? [];
  const stepEntities = [...new Set(planSteps.map((s) => s.entity).filter((e): e is string => Boolean(e)))];
  const shownSteps = entityFilter === "" ? planSteps : planSteps.filter((s) => (entityFilter === ALL_ENTITY ? !s.entity : s.entity === entityFilter));

  const columns: DataTableColumn<RunbookStep>[] = [
    { key: "seq", header: "Seq", mono: true },
    { key: "offsetMin", header: "T offset", mono: true, render: (row) => formatOffset(row.offsetMin) },
    { key: "name", header: "Step" },
    ...(stepEntities.length > 0 ? [{ key: "entity", header: "Entity", render: (row: RunbookStep) => row.entity ?? "All" }] : []),
    { key: "owner", header: "Owner" },
    { key: "plannedDurationMin", header: "Planned (min)", align: "end" },
    {
      key: "actualDurationMin",
      header: "Elapsed / actual (min)",
      align: "end",
      render: (row) => {
        const elapsed = elapsedOf(row, now);
        return elapsed === null ? "" : elapsed;
      },
    },
    { key: "goNoGo", header: "Go/no-go", render: (row) => (row.goNoGo ? "Yes" : "No") },
    { key: "status", header: "Status", render: (row) => <StatusPill status={running(row) ? "running" : row.status} /> },
    {
      key: "tone",
      header: "Overrun",
      render: (row) => {
        const elapsed = elapsedOf(row, now);
        if (elapsed === null) return "";
        const tone = overrunTone(row.plannedDurationMin, elapsed);
        return <Badge tone={toneBadge[tone]}>{toneLabel[tone]}</Badge>;
      },
    },
    {
      key: "action",
      header: "Action",
      render: (row) =>
        row.status === "PENDING" && plan && !plan.rolledBack ? (
          <span className={styles.rowActions}>
            {row.startedAt ? null : (
              <Button size="sm" onClick={() => void start(row)} disabled={busy} aria-label={`Start step ${row.seq}`}>
                Start
              </Button>
            )}
            <Button size="sm" onClick={() => choose(row)} aria-label={`Complete step ${row.seq}`}>
              Complete
            </Button>
          </span>
        ) : null,
    },
  ];

  async function start(step: RunbookStep) {
    if (!plan) return;
    setFailure(null);
    setBusy(true);
    try {
      const updated = await startStep(plan.planId, step.seq, plan.version, startKey.current);
      state.set(updated);
      setNow(Date.now());
      // A completion form open for this step switches to the measured time.
      if (selected?.seq === step.seq) choose(updated.steps?.find((s) => s.seq === step.seq) ?? step);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure({ ...problem, message: conflictMessage(problem) });
      if (problem.code === "MIG-0006") state.reload();
    } finally {
      startKey.current = newKey();
      setBusy(false);
    }
  }

  function choose(step: RunbookStep) {
    setSelected(step);
    // A started step is measured by the service when the minutes are left blank.
    setMinutes(step.startedAt ? "" : String(step.plannedDurationMin));
    setMinutesError(undefined);
    setFailure(null);
    stepKey.current = newKey();
  }

  async function submitStep(event: FormEvent) {
    event.preventDefault();
    if (!plan || !selected) return;
    const typed = minutes.trim();
    const measured = typed === "" && Boolean(selected.startedAt);
    if (!measured && !WHOLE.test(typed)) {
      setMinutesError(selected.startedAt ? "Enter the minutes as a whole number, or leave blank to use the measured time." : "Enter the minutes the step took, as a whole number.");
      return;
    }
    setMinutesError(undefined);
    setFailure(null);
    setBusy(true);
    try {
      state.set(await completeStep(plan.planId, selected.seq, measured ? undefined : Number(typed), plan.version, stepKey.current));
      setSelected(null);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure({ ...problem, message: conflictMessage(problem) });
      if (problem.code === "MIG-0006") state.reload();
      stepKey.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  function askRollback(event: FormEvent) {
    event.preventDefault();
    if (!plan) return;
    const parsed = parsePostings(postingsText, plan.entities);
    setPostingsError(parsed.errors.length > 0 ? parsed.errors.slice(0, 3).join(" ") : undefined);
    if (parsed.errors.length > 0) return;
    setPostings(parsed.postings);
    setConfirming(true);
  }

  async function doRollback() {
    if (!plan) return;
    setConfirming(false);
    setFailure(null);
    setBusy(true);
    try {
      state.set(await rollbackPlan(plan.planId, postings, plan.version, rollbackKey.current));
      rollbackKey.current = newKey();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure({ ...problem, message: conflictMessage(problem) });
      if (problem.code === "MIG-0006") state.reload();
      rollbackKey.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cutover runbook"
        description={
          plan
            ? `T-0 ${formatDateTime(plan.t0)} for ${plan.entities.join(", ")}.${
                plan.rehearsalPlanId ? ` ${plan.rehearsalCarried ?? 0} planned durations were measured in rehearsal plan ${plan.rehearsalPlanId}.` : ""
              }`
            : undefined
        }
        meta={plan?.rolledBack ? <StatusPill status="Rolled back" /> : undefined}
        actions={
          <Link className={buttonClassName("ghost")} to="/migration-factory/cutover">
            All plans
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the plan" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="The change was not saved" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {plan ? (
        <>
          {plan.abortDue ? (
            <Alert tone="danger" title="Abort deadline passed">
              Go/no-go items are still open after T+{plan.abortOffsetMin / 60}h. Confirm the rollback below (BR-49.9).
            </Alert>
          ) : plan.abortWarning ? (
            <Alert tone="warning" title="Abort deadline approaching">
              {plan.abortWarning}
            </Alert>
          ) : null}
          {overruns.length > 0 ? (
            <Alert tone="warning" title="Steps over plan">
              <ul className={styles.plainList} aria-label="Steps over plan">
                {overruns.map(({ step, elapsed }) => (
                  <li key={step.seq}>
                    CUT-1602 Step {step.seq} {step.name} at {Math.floor((elapsed * 100) / step.plannedDurationMin)}% of planned duration ({formatMinutes(elapsed)}{" "}
                    of {formatMinutes(step.plannedDurationMin)}).
                  </li>
                ))}
              </ul>
            </Alert>
          ) : null}
          <StatGroup label="Progress">
            <StatTile label="Steps done" value={`${plan.stepsDone} / ${plan.stepsTotal}`} />
            <StatTile label="Open go/no-go" value={plan.openGoNoGo.length} tone={plan.openGoNoGo.length > 0 ? "warning" : "neutral"} />
            <StatTile label="Abort deadline" value={formatOffset(plan.abortOffsetMin)} />
          </StatGroup>
          <Card title="Go/no-go checklist">
            <p className={styles.note}>
              {plan.decision
                ? `Latest decision: ${plan.decision.decision === "GO" ? "Go" : "No-go"} by ${plan.decision.approvers.join(" and ")}, ${formatDateTime(plan.decision.decidedAt)}.`
                : "No go/no-go decision is recorded yet."}{" "}
              <Link to={`/migration-factory/cutover/${plan.planId}/go-no-go`}>Open the go/no-go decision</Link>
            </p>
            {plan.openGoNoGo.length === 0 ? (
              <p className={styles.note}>All go/no-go items are complete.</p>
            ) : (
              <ul className={styles.plainList} aria-label="Open go/no-go items">
                {plan.openGoNoGo.map((name) => (
                  <li key={name}>{name}</li>
                ))}
              </ul>
            )}
          </Card>
          {selected ? (
            <Card title={`Complete step ${selected.seq}: ${selected.name}`}>
              <form className={styles.form} onSubmit={submitStep} noValidate>
                <FormField
                  id="step-minutes"
                  label="Actual duration (min)"
                  hint={
                    selected.startedAt
                      ? `Planned ${selected.plannedDurationMin} min. Started ${formatDateTime(selected.startedAt)}, running ${formatMinutes(elapsedSince(selected.startedAt, now))}; leave blank to use the measured time.`
                      : `Planned ${selected.plannedDurationMin} min.`
                  }
                  error={minutesError}
                  optional={Boolean(selected.startedAt)}
                >
                  {(control) => <Input {...control} inputMode="numeric" value={minutes} invalid={Boolean(minutesError)} onChange={(event) => setMinutes(event.target.value)} />}
                </FormField>
                <div className={styles.formActions}>
                  <Button type="submit" variant="primary" loading={busy}>
                    Mark step done
                  </Button>
                  <Button variant="ghost" onClick={() => setSelected(null)}>
                    Cancel
                  </Button>
                </div>
              </form>
            </Card>
          ) : null}
        </>
      ) : null}
      {!state.error && stepEntities.length > 0 ? (
        <div className={styles.form}>
          <FormField id="plan-entity-filter" label="Show steps for" hint={`${shownSteps.length} of ${planSteps.length} steps.`} optional>
            {(control) => (
              <Select {...control} value={entityFilter} onChange={(event) => setEntityFilter(event.target.value)}>
                <option value="">All steps</option>
                <option value={ALL_ENTITY}>Plan-wide steps</option>
                {stepEntities.map((e) => (
                  <option key={e} value={e}>
                    {e}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
      ) : null}
      {!state.error ? (
        <DataTable
          label="Runbook steps"
          columns={columns}
          rows={shownSteps}
          getRowKey={(row) => String(row.seq)}
          loading={state.loading}
          empty={{ title: "No steps", description: "The plan has no steps." }}
        />
      ) : null}
      {plan?.rollbackSteps && plan.rollbackSteps.length > 0 ? (
        <Card title="Rollback plan">
          <div className={styles.stack}>
            <p className={styles.note}>
              Generated for this cutover (FR-49.9). If go/no-go items are still open at the abort deadline ({formatOffset(plan.abortOffsetMin)}), the
              cutover manager decides on the rollback and these steps follow.
            </p>
            <DataTable label="Rollback plan" columns={rollbackColumns} rows={plan.rollbackSteps} getRowKey={(row) => String(row.seq)} />
          </div>
        </Card>
      ) : null}
      {plan ? (
        <Card title="Cutover record">
          <div className={styles.stack}>
            <p className={styles.note}>
              CSV files to keep with the cutover record (9.49.15), built from the factory's data when you download them. The reconciliation carries
              the runs' control totals and also needs Migration view. The signed PDF records need e-signature and come later.
            </p>
            <div className={styles.toolbar}>
              {EXPORT_FILES.map((file) => (
                <a key={file.file} className={buttonClassName("secondary")} href={exportFileUrl(plan.planId, file.file)} download>
                  {file.label}
                </a>
              ))}
            </div>
          </div>
        </Card>
      ) : null}
      {plan ? (
        <Card title="Rollback">
          {plan.rolledBack ? (
            <p className={styles.note}>Rolled back with the approval of {plan.approvers.join(" and ")}.</p>
          ) : (
            <form className={styles.form} onSubmit={askRollback} noValidate>
              <p className={`${styles.note} ${styles.wide}`}>
                Stops Passage access, restores legacy write access and exports the postings made since T-0 as journal and document files for
                re-keying in the legacy system. Two different migration.cutover.manage holders must approve (CUT-1603): your approval is recorded under your own
                sign-in, and the rollback runs when a second person approves.
              </p>
              {plan.pendingRollback ? (
                <div className={styles.wide}>
                  <Alert tone="warning" title="Waiting for a second approver">
                    {plan.pendingRollback.approver} approved this rollback on {formatDateTime(plan.pendingRollback.approvedAt)}. A different migration.cutover.manage
                    approves it here to run it, with the postings since T-0 entered below. A go/no-go decision recorded before then voids this approval.
                  </Alert>
                </div>
              ) : null}
              <div className={styles.wide}>
                <FormField id="rollback-postings" label="Postings since T-0" hint={POSTINGS_HINT} error={postingsError} optional>
                  {(control) => (
                    <Textarea {...control} mono rows={6} value={postingsText} invalid={Boolean(postingsError)} onChange={(event) => setPostingsText(event.target.value)} />
                  )}
                </FormField>
              </div>
              <div className={styles.formActions}>
                <Button type="submit" variant="danger" loading={busy}>
                  {plan.pendingRollback ? "Approve and roll back" : "Approve rollback"}
                </Button>
              </div>
            </form>
          )}
        </Card>
      ) : null}
      {plan?.rolledBack && plan.rekeying ? (
        <Card title="Re-keying files">
          <div className={styles.stack}>
            <StatGroup label="Re-keying export">
              <StatTile label="Journal lines" value={plan.rekeying.journalLines} />
              <StatTile label="Document lines" value={plan.rekeying.documentLines} />
              <StatTile label="Left out (before T-0)" value={plan.rekeying.excluded} />
            </StatGroup>
            <p className={styles.note}>
              Re-key these postings in the legacy system, then reconcile. The files are kept with this plan until they are re-keyed.
            </p>
            <div className={styles.toolbar}>
              <a className={buttonClassName("secondary")} href={rekeyFileUrl(plan.planId, "journals.csv")} download>
                Download journals (CSV)
              </a>
              <a className={buttonClassName("secondary")} href={rekeyFileUrl(plan.planId, "documents.csv")} download>
                Download documents (CSV)
              </a>
            </div>
          </div>
        </Card>
      ) : null}
      {confirming ? (
        <ConfirmDialog
          title={plan?.pendingRollback ? "Roll back this cutover?" : "Approve the rollback?"}
          description={
            plan?.pendingRollback
              ? `Passage access stops and the legacy system takes postings again. ${postingSummary(postings.length)} This can't be undone from this screen.`
              : `Your approval is recorded; the rollback runs when a different migration.cutover.manage holder approves it. ${postingSummary(postings.length)}`
          }
          confirmLabel={plan?.pendingRollback ? "Roll back" : "Approve"}
          cancelLabel="Keep cutover"
          onConfirm={() => void doRollback()}
          onCancel={() => setConfirming(false)}
        />
      ) : null}
    </div>
  );
}
