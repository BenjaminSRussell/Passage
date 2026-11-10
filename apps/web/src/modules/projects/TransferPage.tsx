import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, PageHeader, Select, Skeleton, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  previewTransfer,
  RESOLUTIONS,
  transferSimulation,
  type NodeChange,
  type NodeChangeKind,
  type Problem,
  type Resolution,
  type TransferView,
  type WbsNodeInput,
} from "./api/client";
import { dash } from "./labels";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage, problemSummary } from "../../shell/errors";

const changeLabel: Record<NodeChangeKind, string> = { ADDED: "Added", CHANGED: "Changed", REMOVED: "Removed" };
const changeTone: Record<NodeChangeKind, BadgeTone> = { ADDED: "success", CHANGED: "info", REMOVED: "warning" };
const resolutionLabel: Record<Resolution, string> = { KEEP_WORKING: "Keep working plan", TAKE_SIMULATION: "Take simulation" };

/** A short description of one side of a node, for comparing the two sides at a glance. */
export function nodeSummary(n?: WbsNodeInput): string {
  if (!n) return "Not in this plan";
  const parts = [n.name, `${n.durationDays} ${n.durationDays === 1 ? "day" : "days"}`];
  if (n.effortHours) parts.push(`${n.effortHours} h`);
  if (n.constraintType === "SNET" && n.constraintDate) parts.push(`starts no earlier than ${n.constraintDate}`);
  if (n.isBillableMilestone) parts.push("billable milestone");
  return parts.join(", ");
}

const failureTitle: Record<string, string> = {
  "PROJ-SIM-001": "The transfer needs a decision",
  "PROJ-PLAN-002": "This simulation is already transferred",
  conflict_version: "A plan changed since the comparison",
};

/**
 * Simulation transfer (FR-41.17, BR-41.14): the three-way comparison of a simulation with the working plan,
 * one decision per conflicting task, then the transfer, which replaces the working WBS and locks the simulation.
 */
export function TransferPage() {
  const { id = "", planId = "" } = useParams();
  const state = useLoad((signal) => previewTransfer(id, planId, signal), `${id}|${planId}`);
  const [choices, setChoices] = useState<Record<string, Resolution>>({});
  const [done, setDone] = useState<TransferView | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const plansLink = `/projects/${encodeURIComponent(id)}/plans`;
  const simLink = `${plansLink}/${encodeURIComponent(planId)}`;

  if (!state.data && !done) {
    if (state.error) {
      const notFound = state.error.status === 404;
      return (
        <div className={styles.stack}>
          <PageHeader title="Transfer simulation" />
          {notFound || state.error.status === 422 ? (
            <Card>
              <EmptyState
                icon="grid"
                title={notFound ? "Plan version not found" : "This plan cannot be transferred"}
                description={notFound ? "This plan version does not exist for the project." : problemSummary(state.error)}
                action={
                  <Link className={buttonClassName("secondary")} to={notFound ? plansLink : simLink}>
                    {notFound ? "Back to plans" : "Back to the simulation"}
                  </Link>
                }
              />
            </Card>
          ) : (
            <Alert tone="danger" title="Could not compare the simulation" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
              <ProblemMessage problem={state.error} />
            </Alert>
          )}
        </div>
      );
    }
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Transfer simulation" />
        <span className="visually-hidden">Comparing the simulation with the working plan…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const view = (done ?? state.data) as TransferView;
  const sim = view.simulation;
  const open = done ? [] : view.conflicts.filter((c) => !choices[c.wbsCode]);

  function compareAgain() {
    setFailure(null);
    setChoices({});
    state.reload();
  }

  async function transfer() {
    setBusy(true);
    setFailure(null);
    try {
      const resolutions: Record<string, Resolution> = {};
      for (const c of view.conflicts) if (choices[c.wbsCode]) resolutions[c.wbsCode] = choices[c.wbsCode];
      const out = await transferSimulation(id, planId, { resolutions, workingVersion: view.working.version }, sim.version);
      setDone(out);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const changeColumns: DataTableColumn<NodeChange>[] = [
    { key: "wbsCode", header: "Code", mono: true },
    { key: "change", header: "Change", render: (c) => <Badge tone={changeTone[c.change]}>{changeLabel[c.change]}</Badge> },
    { key: "working", header: "Working plan", render: (c) => nodeSummary(c.working) },
    { key: "simulation", header: "Simulation", render: (c) => nodeSummary(c.simulation) },
    { key: "resolution", header: "Decision", render: (c) => (c.resolution ? resolutionLabel[c.resolution] : dash) },
  ];
  const conflictColumns: DataTableColumn<NodeChange>[] = [
    { key: "wbsCode", header: "Code", mono: true },
    { key: "working", header: "Working plan", render: (c) => nodeSummary(c.working) },
    { key: "simulation", header: "Simulation", render: (c) => nodeSummary(c.simulation) },
    {
      key: "resolution",
      header: "Decision",
      render: (c) =>
        done ? (
          c.resolution ? resolutionLabel[c.resolution] : dash
        ) : (
          <Select
            aria-label={`Decision for task ${c.wbsCode}`}
            value={choices[c.wbsCode] ?? ""}
            onChange={(e) => {
              const value = e.target.value as Resolution | "";
              setChoices((prev) => {
                const next = { ...prev };
                if (value) next[c.wbsCode] = value;
                else delete next[c.wbsCode];
                return next;
              });
            }}
          >
            <option value="">Choose…</option>
            {RESOLUTIONS.map((r) => (
              <option key={r} value={r}>
                {resolutionLabel[r]}
              </option>
            ))}
          </Select>
        ),
    },
  ];
  const stale = failure?.code === "conflict_version" || failure?.code === "PROJ-PLAN-002";

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Transfer simulation plan ${sim.sequence} for ${id}`}
        description="Tasks changed only in the simulation are applied to the working plan. Tasks changed differently in both since the simulation was copied need a decision. The transfer replaces the working WBS (run its schedule again) and locks the simulation."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to={simLink}>
              Back to the simulation
            </Link>
            {done ? (
              <Link className={buttonClassName("primary")} to={`${plansLink}/${encodeURIComponent(done.working.id)}`}>
                Open working plan
              </Link>
            ) : null}
          </>
        }
      />
      <Alert tone="success" live="status">
        {done
          ? `Transferred. The working plan is at version ${done.working.version}; ${done.applied.length} ${done.applied.length === 1 ? "task was" : "tasks were"} applied and the simulation is locked.`
          : null}
      </Alert>
      {failure ? (
        <Alert
          tone="danger"
          title={failureTitle[failure.code] ?? "Could not transfer the simulation"}
          live="alert"
          action={stale ? <Button size="sm" onClick={compareAgain}>Compare again</Button> : undefined}
        >
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <Card title={`Conflicts (${view.conflicts.length})`}>
        {view.conflicts.length === 0 ? (
          <p className={styles.note}>No task was changed differently in both plans; nothing needs a decision.</p>
        ) : (
          <DataTable label="Conflicts" columns={conflictColumns} rows={view.conflicts} getRowKey={(c) => c.wbsCode} />
        )}
      </Card>
      <DataTable
        label="Changes to apply"
        columns={changeColumns}
        rows={view.applied}
        getRowKey={(c) => c.wbsCode}
        empty={{ title: "Nothing to apply", description: "No task was changed only in the simulation." }}
      />
      {!done ? (
        <div className={styles.actions}>
          <Button variant="primary" loading={busy} disabled={open.length > 0 || state.loading} onClick={() => void transfer()}>
            Transfer to working plan
          </Button>
          {open.length > 0 ? <p className={styles.note}>Decide {open.length === 1 ? "the conflict" : `all ${open.length} conflicts`} to transfer.</p> : null}
        </div>
      ) : null}
    </div>
  );
}
