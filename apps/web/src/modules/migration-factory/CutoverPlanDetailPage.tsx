import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  ConfirmDialog,
  DataTable,
  FormField,
  Input,
  PageHeader,
  StatGroup,
  StatTile,
  StatusPill,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import { completeStep, describeProblem, getPlan, newKey, rekeyFileUrl, rollbackPlan, type Problem, type RekeyPosting, type RunbookStep } from "./api/client";
import { formatDateTime, formatOffset, overrunTone, parsePostings, toneLabel } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const WHOLE = /^\d{1,5}$/;

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
 * One cutover runbook: steps with planned against actual, the go/no-go checklist, step completion,
 * and the rollback with its re-keying export of the postings since T-0 (FR-49.9).
 */
export function CutoverPlanDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getPlan(id, signal), id);
  const plan = state.data;
  const [selected, setSelected] = useState<RunbookStep | null>(null);
  const [minutes, setMinutes] = useState("");
  const [minutesError, setMinutesError] = useState<string | undefined>();
  const [approvers, setApprovers] = useState({ first: "", second: "" });
  const [approverError, setApproverError] = useState<string | undefined>();
  const [postingsText, setPostingsText] = useState("");
  const [postingsError, setPostingsError] = useState<string | undefined>();
  const [postings, setPostings] = useState<RekeyPosting[]>([]);
  const [confirming, setConfirming] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const stepKey = useRef(newKey());
  const rollbackKey = useRef(newKey());

  const columns: DataTableColumn<RunbookStep>[] = [
    { key: "seq", header: "Seq", mono: true },
    { key: "offsetMin", header: "T offset", mono: true, render: (row) => formatOffset(row.offsetMin) },
    { key: "name", header: "Step" },
    { key: "owner", header: "Owner" },
    { key: "plannedDurationMin", header: "Planned (min)", align: "end" },
    { key: "actualDurationMin", header: "Actual (min)", align: "end", render: (row) => (row.status === "DONE" ? row.actualDurationMin : "") },
    { key: "goNoGo", header: "Go/no-go", render: (row) => (row.goNoGo ? "Yes" : "No") },
    { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
    {
      key: "tone",
      header: "Overrun",
      render: (row) => (row.status === "DONE" ? toneLabel[overrunTone(row.plannedDurationMin, row.actualDurationMin)] : ""),
    },
    {
      key: "action",
      header: "Action",
      render: (row) =>
        row.status === "PENDING" && plan && !plan.rolledBack ? (
          <Button size="sm" onClick={() => choose(row)} aria-label={`Complete step ${row.seq}`}>
            Complete
          </Button>
        ) : null,
    },
  ];

  function choose(step: RunbookStep) {
    setSelected(step);
    setMinutes(String(step.plannedDurationMin));
    setMinutesError(undefined);
    setFailure(null);
    stepKey.current = newKey();
  }

  async function submitStep(event: FormEvent) {
    event.preventDefault();
    if (!plan || !selected) return;
    if (!WHOLE.test(minutes.trim())) {
      setMinutesError("Enter the minutes the step took, as a whole number.");
      return;
    }
    setMinutesError(undefined);
    setFailure(null);
    setBusy(true);
    try {
      state.set(await completeStep(plan.planId, selected.seq, Number(minutes.trim()), plan.version, stepKey.current));
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
    const first = approvers.first.trim();
    const second = approvers.second.trim();
    const approversOk = Boolean(first && second && first !== second);
    setApproverError(approversOk ? undefined : "Rollback needs two different approvers (CUT-1603).");
    const parsed = parsePostings(postingsText, plan.entities);
    setPostingsError(parsed.errors.length > 0 ? parsed.errors.slice(0, 3).join(" ") : undefined);
    if (!approversOk || parsed.errors.length > 0) return;
    setPostings(parsed.postings);
    setConfirming(true);
  }

  async function doRollback() {
    if (!plan) return;
    setConfirming(false);
    setFailure(null);
    setBusy(true);
    try {
      state.set(await rollbackPlan(plan.planId, [approvers.first.trim(), approvers.second.trim()], postings, plan.version, rollbackKey.current));
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
        description={plan ? `T-0 ${formatDateTime(plan.t0)} for ${plan.entities.join(", ")}.` : undefined}
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
          <StatGroup label="Progress">
            <StatTile label="Steps done" value={`${plan.stepsDone} / ${plan.stepsTotal}`} />
            <StatTile label="Open go/no-go" value={plan.openGoNoGo.length} tone={plan.openGoNoGo.length > 0 ? "warning" : "neutral"} />
            <StatTile label="Abort deadline" value={formatOffset(plan.abortOffsetMin)} />
          </StatGroup>
          <Card title="Go/no-go checklist">
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
                <FormField id="step-minutes" label="Actual duration (min)" hint={`Planned ${selected.plannedDurationMin} min.`} error={minutesError}>
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
      {!state.error ? (
        <DataTable
          label="Runbook steps"
          columns={columns}
          rows={plan?.steps ?? []}
          getRowKey={(row) => String(row.seq)}
          loading={state.loading}
          empty={{ title: "No steps", description: "The plan has no steps." }}
        />
      ) : null}
      {plan ? (
        <Card title="Rollback">
          {plan.rolledBack ? (
            <p className={styles.note}>Rolled back with the approval of {plan.approvers.join(" and ")}.</p>
          ) : (
            <form className={styles.form} onSubmit={askRollback} noValidate>
              <p className={`${styles.note} ${styles.wide}`}>
                Stops Passage access, restores legacy write access and exports the postings made since T-0 as journal and document files for
                re-keying in the legacy system. Two different people must approve.
              </p>
              <FormField id="rollback-first" label="First approver" error={approverError}>
                {(control) => <Input {...control} value={approvers.first} invalid={Boolean(approverError)} onChange={(event) => setApprovers({ ...approvers, first: event.target.value })} />}
              </FormField>
              <FormField id="rollback-second" label="Second approver">
                {(control) => <Input {...control} value={approvers.second} onChange={(event) => setApprovers({ ...approvers, second: event.target.value })} />}
              </FormField>
              <div className={styles.wide}>
                <FormField id="rollback-postings" label="Postings since T-0" hint={POSTINGS_HINT} error={postingsError} optional>
                  {(control) => (
                    <Textarea {...control} mono rows={6} value={postingsText} invalid={Boolean(postingsError)} onChange={(event) => setPostingsText(event.target.value)} />
                  )}
                </FormField>
              </div>
              <div className={styles.formActions}>
                <Button type="submit" variant="danger" loading={busy}>
                  Roll back cutover
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
          title="Roll back this cutover?"
          description={`Passage access stops and the legacy system takes postings again. ${postingSummary(postings.length)} This can't be undone from this screen.`}
          confirmLabel="Roll back"
          cancelLabel="Keep cutover"
          onConfirm={() => void doRollback()}
          onCancel={() => setConfirming(false)}
        />
      ) : null}
    </div>
  );
}
