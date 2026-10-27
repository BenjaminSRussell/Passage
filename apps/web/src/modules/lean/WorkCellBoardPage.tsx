import { useEffect, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, EmptyState, FormField, Input, PageHeader, Select, Skeleton, StatGroup, StatTile } from "../../shell/ui";
import {
  completeJob,
  describeProblem,
  fetchJobBoard,
  getFlow,
  listFlows,
  newKey,
  startJob,
  type Flow,
  type JobState,
  type KanbanJob,
  type Problem,
} from "./api/client";
import { REFRESH_MS } from "./BoardPage";
import { formatDateTime, overdue, reportQuantity, shortId, STATE_LABELS, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** "MIX-A-0001 · 1 Mix": how a job is named on screen. */
export function jobLabel(job: Pick<KanbanJob, "cardNumber" | "sequence" | "activityName">): string {
  return `${job.cardNumber} · ${job.sequence} ${job.activityName}`;
}

/** The work cells of a flow's activities, each with the activities it runs. */
export function workCells(flow: Flow | null | undefined): { id: string; label: string }[] {
  const byCell = new Map<string, string[]>();
  for (const a of flow?.activities ?? []) {
    if (!a.workCellResourceId) continue;
    byCell.set(a.workCellResourceId, [...(byCell.get(a.workCellResourceId) ?? []), a.name]);
  }
  return [...byCell].map(([id, names]) => ({ id, label: `${names.join(", ")} (${shortId(id)})` }));
}

export interface CompletionDraft {
  reportedQty: string;
  scrapQty: string;
  handlingUnits: string;
}

/** Checks a completion report as the service does (FR-24.10); returns field errors. */
export function validateCompletion(d: CompletionDraft): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!reportQuantity(d.reportedQty)) errors.reportedQty = "Enter the good quantity (0 or more, up to 6 decimals).";
  if (!reportQuantity(d.scrapQty)) errors.scrapQty = "Enter the scrap (0 or more, up to 6 decimals).";
  if (!/^[1-9]\d{0,4}$/.test(d.handlingUnits) && d.handlingUnits !== "100000") errors.handlingUnits = "Enter 1 to 100000 handling units.";
  if (!errors.reportedQty && !errors.scrapQty && Number(d.reportedQty) + Number(d.scrapQty) <= 0) {
    errors.reportedQty = "Report a good or scrap quantity above zero.";
  }
  return errors;
}

interface JobCardProps {
  job: KanbanJob;
  busy: boolean;
  onStart(job: KanbanJob): void;
  onComplete(job: KanbanJob, draft: CompletionDraft): Promise<boolean>;
}

function JobCard({ job, busy, onStart, onComplete }: JobCardProps) {
  const [draft, setDraft] = useState<CompletionDraft | null>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const label = jobLabel(job);
  const late = overdue(job.due, job.state);
  const id = `job-${job.id}`;

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!draft) return;
    const found = validateCompletion(draft);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    if (await onComplete(job, draft)) setDraft(null);
  }

  return (
    <li className={styles.boardCard} aria-busy={busy || undefined}>
      <div className={styles.boardCardHead}>
        <Link className={styles.mono} to={`/lean/kanbans/${job.kanbanId}`}>
          {job.cardNumber}
        </Link>
        {late ? <Badge tone="warning">Overdue</Badge> : null}
      </div>
      <dl className={styles.boardFacts}>
        <div>
          <dt className={styles.term}>Activity</dt>
          <dd className={styles.value}>{`${job.sequence} ${job.activityName}`}</dd>
        </div>
        <div>
          <dt className={styles.term}>Quantity</dt>
          <dd className={styles.value}>{job.cardQty}</dd>
        </div>
        <div>
          <dt className={styles.term}>Due</dt>
          <dd className={styles.value}>{formatDateTime(job.due)}</dd>
        </div>
        <div>
          <dt className={styles.term}>Item</dt>
          <dd className={`${styles.value} ${styles.mono}`}>{shortId(job.itemId)}</dd>
        </div>
        {job.state === "completed" ? (
          <>
            <div>
              <dt className={styles.term}>Reported</dt>
              <dd className={styles.value}>{job.reportedQty}</dd>
            </div>
            <div>
              <dt className={styles.term}>Scrap</dt>
              <dd className={styles.value}>{job.scrapQty}</dd>
            </div>
          </>
        ) : null}
      </dl>
      {job.state === "not-planned" ? <p className={styles.muted}>Plan the card on the kanban board first.</p> : null}
      {(job.state === "planned" || job.state === "prepared") && job.pickingRequired && job.cardState === "planned" ? (
        <p className={styles.muted}>Needs picking: move the card to Prepared before starting.</p>
      ) : null}
      {job.state === "planned" || job.state === "prepared" ? (
        <div className={styles.actions}>
          <Button size="sm" variant="primary" loading={busy} onClick={() => onStart(job)} aria-label={`Start ${label}`}>
            Start
          </Button>
        </div>
      ) : null}
      {job.state === "in-progress" && !draft ? (
        <div className={styles.actions}>
          <Button
            size="sm"
            variant="primary"
            onClick={() => {
              setErrors({});
              setDraft({ reportedQty: job.cardQty, scrapQty: "0", handlingUnits: "1" });
            }}
            aria-label={`Complete ${label}`}
          >
            Complete
          </Button>
        </div>
      ) : null}
      {job.state === "in-progress" && draft ? (
        <form className={styles.stack} onSubmit={submit} noValidate aria-label={`Report ${label}`}>
          <FormField id={`${id}-good`} label="Good quantity" error={errors.reportedQty}>
            {(c) => (
              <Input {...c} inputMode="decimal" value={draft.reportedQty} invalid={Boolean(errors.reportedQty)} onChange={(e) => setDraft({ ...draft, reportedQty: e.target.value })} />
            )}
          </FormField>
          <FormField id={`${id}-scrap`} label="Scrap" error={errors.scrapQty}>
            {(c) => (
              <Input {...c} inputMode="decimal" value={draft.scrapQty} invalid={Boolean(errors.scrapQty)} onChange={(e) => setDraft({ ...draft, scrapQty: e.target.value })} />
            )}
          </FormField>
          <FormField id={`${id}-units`} label="Handling units" error={errors.handlingUnits}>
            {(c) => (
              <Input {...c} inputMode="numeric" value={draft.handlingUnits} invalid={Boolean(errors.handlingUnits)} onChange={(e) => setDraft({ ...draft, handlingUnits: e.target.value })} />
            )}
          </FormField>
          <div className={styles.actions}>
            <Button size="sm" type="submit" variant="primary" loading={busy}>
              Report completion
            </Button>
            <Button size="sm" onClick={() => setDraft(null)}>
              Cancel
            </Button>
          </div>
        </form>
      ) : null}
    </li>
  );
}

/**
 * Work-cell board (FR-24.10): the jobs at one work cell's activities in one
 * column per job state. Starting the first job of a card runs the card and
 * completing its last job completes it (BR-24.6); the service refuses anything
 * else, and the job then stays where it was (AC-24.7). The board reloads after
 * each command and every 30 seconds.
 */
export function WorkCellBoardPage() {
  const [params, setParams] = useSearchParams();
  const workCellId = params.get("workCellId") ?? "";
  const flowId = params.get("flowId") ?? "";
  const chosen = UUID.test(workCellId);
  const flows = useLoad((signal) => listFlows("", signal), "flows");
  const flow = useLoad((signal) => (flowId ? getFlow(flowId, signal) : Promise.resolve(null)), `flow|${flowId}`);
  const board = useLoad((signal) => (chosen ? fetchJobBoard({ workCellId, flowId }, signal) : Promise.resolve(null)), `jobs|${workCellId}|${flowId}`);
  const [cellDraft, setCellDraft] = useState(workCellId);
  const [cellError, setCellError] = useState<string | null>(null);
  const [working, setWorking] = useState<string | null>(null);
  const [failure, setFailure] = useState<(Problem & { job: string; from: JobState }) | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const { reload } = board;

  useEffect(() => {
    if (!chosen) return;
    const timer = setInterval(reload, REFRESH_MS);
    return () => clearInterval(timer);
  }, [reload, chosen]);

  function filter(patch: { flowId?: string; workCellId?: string }) {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    setParams(next, { replace: true });
  }

  function chooseCell(event: FormEvent) {
    event.preventDefault();
    const value = cellDraft.trim();
    if (!UUID.test(value)) {
      setCellError("Enter the work cell's resource id (a UUID).");
      return;
    }
    setCellError(null);
    filter({ workCellId: value });
  }

  async function run(job: KanbanJob, command: () => Promise<KanbanJob>, done: (j: KanbanJob) => string): Promise<boolean> {
    setWorking(job.id);
    setFailure(null);
    setNotice(null);
    try {
      setNotice(done(await command()));
      return true;
    } catch (error) {
      setFailure({ ...describeProblem(error), job: jobLabel(job), from: job.state });
      return false;
    } finally {
      setWorking(null);
      reload();
    }
  }

  const start = (job: KanbanJob) =>
    void run(
      job,
      () => startJob(job.id, { version: job.version }, newKey()),
      (j) => `${jobLabel(j)} started.${j.cardState === "in-progress" && job.cardState !== "in-progress" ? ` ${j.cardNumber} is in progress.` : ""}`,
    );
  const complete = (job: KanbanJob, d: CompletionDraft) =>
    run(
      job,
      () => completeJob(job.id, { version: job.version, reportedQty: d.reportedQty, scrapQty: d.scrapQty, handlingUnits: Number(d.handlingUnits) }, newKey()),
      (j) => `${jobLabel(j)} completed.${j.cardState === "completed" ? ` ${j.cardNumber} is completed.` : ""}`,
    );

  const cells = workCells(flow.data);
  const columns = board.data?.columns ?? [];
  const jobs = columns.flatMap((c) => c.jobs);
  const late = jobs.filter((j) => overdue(j.due, j.state)).length;
  const wip = jobs.filter((j) => j.state === "in-progress").length;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Work-cell board"
        description="The jobs at one work cell. Start a job when work begins and complete it with the quantity, scrap and handling units; the card follows its jobs."
      />
      <div className={styles.toolbar}>
        <FormField id="cell-flow" label="Production flow" optional>
          {(c) => (
            <Select {...c} value={flowId} onChange={(e) => filter({ flowId: e.target.value })}>
              <option value="">All flows</option>
              {(flows.data ?? []).map((f) => (
                <option key={f.id} value={f.id}>
                  {f.name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        {cells.length > 0 ? (
          <FormField id="cell-pick" label="Work cell of the flow">
            {(c) => (
              <Select
                {...c}
                value={cells.some((x) => x.id === workCellId) ? workCellId : ""}
                onChange={(e) => {
                  setCellDraft(e.target.value);
                  filter({ workCellId: e.target.value });
                }}
              >
                <option value="">Choose a work cell…</option>
                {cells.map((x) => (
                  <option key={x.id} value={x.id}>
                    {x.label}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        ) : null}
        <form className={styles.toolbar} onSubmit={chooseCell} noValidate>
          <FormField id="cell-id" label="Work cell id" error={cellError ?? undefined}>
            {(c) => <Input {...c} value={cellDraft} invalid={Boolean(cellError)} onChange={(e) => setCellDraft(e.target.value)} />}
          </FormField>
          <Button type="submit">Show board</Button>
        </form>
        {chosen ? <Button onClick={reload}>Refresh</Button> : null}
      </div>
      {failure ? (
        <Alert tone="danger" title={`${failure.job} stays in ${STATE_LABELS[failure.from]}`} live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {notice ? <Alert tone="success" title={notice} live="status" /> : null}
      {!chosen ? (
        <EmptyState
          icon="grid"
          title="Choose a work cell"
          description="Pick a production flow to choose one of its work cells, or enter the work cell's resource id."
        />
      ) : board.error ? (
        <Alert tone="danger" title="Could not load the work-cell board" live="alert" action={<Button size="sm" onClick={reload}>Try again</Button>}>
          {board.error.message} ({board.error.code})
        </Alert>
      ) : !board.data ? (
        <div aria-busy="true" className={styles.loading}>
          <span className="visually-hidden">Loading the work-cell board…</span>
          <Skeleton height="tile" />
        </div>
      ) : jobs.length === 0 ? (
        <EmptyState
          icon="grid"
          title="No jobs at this work cell"
          description="Jobs appear when kanbans are generated for a rule whose production flow has an activity on this work cell."
        />
      ) : (
        <>
          <StatGroup label="Work-cell metrics">
            <StatTile label="Jobs" value={jobs.length} />
            <StatTile label="In progress" value={wip} />
            <StatTile label="Overdue" value={late} tone="warning" />
            {board.data.takt ? (
              <StatTile
                label="Takt vs cycle"
                value={`${board.data.takt.taktTimeS} s / ${board.data.takt.cycleTimeS} s`}
                hint={
                  board.data.takt.withinTakt
                    ? `The slowest activity, ${board.data.takt.bottleneckActivity}, keeps up with takt.`
                    : `${board.data.takt.bottleneckActivity} is slower than takt.`
                }
              />
            ) : null}
          </StatGroup>
          {board.data.truncated ? (
            <Alert tone="warning" title="Showing the first 500 jobs">
              Filter by flow to see the rest.
            </Alert>
          ) : null}
          <div className={styles.board}>
            {columns.map((column) => (
              <section key={column.state} className={styles.boardColumn} aria-labelledby={`jobcol-${column.state}`}>
                <h3 id={`jobcol-${column.state}`} className={styles.boardHeading}>
                  {`${STATE_LABELS[column.state]} (${column.jobs.length})`}
                </h3>
                <ul className={styles.boardList} aria-label={`${STATE_LABELS[column.state]} jobs`}>
                  {column.jobs.map((job) => (
                    <JobCard key={job.id} job={job} busy={working === job.id} onStart={start} onComplete={complete} />
                  ))}
                </ul>
              </section>
            ))}
          </div>
        </>
      )}
    </div>
  );
}
