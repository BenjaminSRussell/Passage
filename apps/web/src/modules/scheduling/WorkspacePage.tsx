import { useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  fetchGantt,
  firmJob,
  listResources,
  moveJob,
  newKey,
  type GanttBar,
  type MoveResponse,
  type Problem,
  type Resource,
} from "./api/client";
import { dayStart, formatDateTime, fromUtcInput, shortId, today, toUtcInput } from "./format";
import { jobsOf } from "./gantt";
import { GANTT_DAYS, GanttView, type GanttDays } from "./GanttView";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

interface Board {
  resources: Resource[];
  bars: GanttBar[];
}

interface Proposal {
  job: GanttBar;
  resourceId: string;
  start: string;
}

interface Preview {
  proposal: Proposal;
  response: MoveResponse;
  /** One key per previewed move: a double click on Confirm replays instead of moving twice. */
  key: string;
}

/**
 * Scheduling workspace (PRD 4.23.6): resources × time Gantt, drag or form
 * moves with a server dry run before commit (FR-23.7, FR-23.8), and firming.
 */
export function WorkspacePage() {
  const [date, setDate] = useState(today());
  const [days, setDays] = useState<GanttDays>(2);
  const from = dayStart(date);
  const to = dayStart(date, days);
  const board = useLoad<Board>(async (signal) => {
    const [resources, bars] = await Promise.all([listResources(signal), fetchGantt(from, to, [], signal)]);
    return { resources, bars };
  }, `${from}|${days}`);

  const [proposal, setProposal] = useState<Proposal | null>(null);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [violations, setViolations] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState<"preview" | "commit" | null>(null);
  const [firming, setFirming] = useState<string | null>(null);
  const formRef = useRef<HTMLDivElement>(null);

  const resources = board.data?.resources ?? [];
  const bars = board.data?.bars ?? [];
  const jobs = jobsOf(bars);
  const codeOf = (id: string) => resources.find((r) => r.id === id)?.code ?? shortId(id);

  function select(job: GanttBar, start = job.start) {
    const whole = jobs.find((j) => j.jobId === job.jobId) ?? job;
    setProposal({ job: whole, resourceId: whole.resourceId, start });
    setPreview(null);
    setViolations({});
    setFailure(null);
    setNotice("");
    formRef.current?.scrollIntoView?.({ block: "nearest" });
  }

  async function runPreview(next: Proposal) {
    setBusy("preview");
    setFailure(null);
    setNotice("");
    try {
      const response = await moveJob(next.job.jobId, { start: next.start, resourceId: next.resourceId, dryRun: true });
      setPreview({ proposal: next, response, key: newKey() });
      setViolations(response.feasible ? {} : Object.fromEntries(response.violations.map((v) => [v.jobId ?? next.job.jobId, `${v.detail} (${v.code})`])));
    } catch (error) {
      setPreview(null);
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  function onDrop(bar: GanttBar, start: string) {
    const whole = jobs.find((j) => j.jobId === bar.jobId) ?? bar;
    // A drag moves the job's first segment by the dragged distance.
    const offset = Date.parse(start) - Date.parse(bar.start);
    const next = { job: whole, resourceId: whole.resourceId, start: new Date(Date.parse(whole.start) + offset).toISOString().replace(".000Z", "Z") };
    setProposal(next);
    void runPreview(next);
  }

  async function confirm() {
    if (!preview || !preview.response.feasible) return;
    setBusy("commit");
    setFailure(null);
    try {
      const { proposal: p } = preview;
      const done = await moveJob(p.job.jobId, { start: p.start, resourceId: p.resourceId, dryRun: false }, preview.key);
      setPreview(null);
      setProposal(null);
      setNotice(`Move saved. ${done.changes.length} operation(s) rescheduled${done.orderEnd ? `; the order now ends ${formatDateTime(done.orderEnd)}` : ""}.`);
      board.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  async function toggleFirm(job: GanttBar) {
    setFirming(job.jobId);
    setFailure(null);
    setNotice("");
    try {
      const out = await firmJob(job.jobId, !job.firmed);
      setNotice(out.firmed ? `Operation ${job.operNumber} is firmed; runs and moves leave it in place.` : `Operation ${job.operNumber} is no longer firmed.`);
      setProposal(null);
      setPreview(null);
      board.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setFirming(null);
    }
  }

  const columns: DataTableColumn<GanttBar>[] = [
    { key: "orderId", header: "Order", mono: true, render: (row) => shortId(row.orderId) },
    { key: "operNumber", header: "Operation", align: "end" },
    { key: "resourceId", header: "Resource", mono: true, render: (row) => codeOf(row.resourceId) },
    { key: "start", header: "Start", render: (row) => formatDateTime(row.start) },
    { key: "end", header: "End", render: (row) => formatDateTime(row.end) },
    { key: "lateMinutes", header: "Late", render: (row) => (row.lateMinutes > 0 ? <Badge tone="warning">{`${row.lateMinutes} min`}</Badge> : "On time") },
    { key: "firmed", header: "Firmed", render: (row) => (row.firmed ? <Badge tone="info">Firmed</Badge> : "No") },
    {
      key: "actions",
      header: "Actions",
      render: (row) => (
        <div className={styles.actions}>
          <Button size="sm" onClick={() => select(row)} aria-label={`Move order ${shortId(row.orderId)} operation ${row.operNumber}`}>
            Move
          </Button>
          <Button
            size="sm"
            variant="ghost"
            loading={firming === row.jobId}
            onClick={() => toggleFirm(row)}
            aria-label={`${row.firmed ? "Unfirm" : "Firm"} order ${shortId(row.orderId)} operation ${row.operNumber}`}
          >
            {row.firmed ? "Unfirm" : "Firm"}
          </Button>
        </div>
      ),
    },
  ];

  function submitPreview(event: FormEvent) {
    event.preventDefault();
    if (proposal) void runPreview(proposal);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Scheduling workspace"
        description="Drag a job along its resource, or choose Move, to preview the change before you save it. Times are UTC."
        actions={
          <Link className={buttonClassName("primary")} to="/scheduling/runs/new">
            Run schedule
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="gantt-from" label="From">
          {(control) => <Input {...control} type="date" value={date} onChange={(event) => event.target.value && setDate(event.target.value)} />}
        </FormField>
        <FormField id="gantt-days" label="Days shown">
          {(control) => (
            <Select {...control} value={String(days)} onChange={(event) => setDays(Number(event.target.value) as GanttDays)}>
              {GANTT_DAYS.map((d) => (
                <option key={d} value={d}>
                  {d === 1 ? "1 day" : `${d} days`}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button onClick={board.reload}>Refresh</Button>
      </div>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert tone="danger" title="The schedule was not changed" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {board.error ? (
        <Alert tone="danger" title="Could not load the schedule" live="alert" action={<Button size="sm" onClick={board.reload}>Try again</Button>}>
          {board.error.message} ({board.error.code})
        </Alert>
      ) : board.loading && !board.data ? (
        <div className={styles.loading} aria-busy="true">
          <span className="visually-hidden">Loading the schedule…</span>
          <Skeleton height="tile" />
          <Skeleton height="tile" />
        </div>
      ) : resources.length === 0 ? (
        <Card>
          <EmptyState
            icon="calendarCheck"
            title="No resources yet"
            description="Add a resource with its working calendar, then add operations and run the schedule."
            action={
              <Link className={buttonClassName("secondary")} to="/scheduling/resources/new">
                Add a resource
              </Link>
            }
          />
        </Card>
      ) : (
        <>
          <GanttView
            bars={bars}
            resources={resources}
            from={from}
            days={days}
            selectedJobId={proposal?.job.jobId}
            violations={violations}
            onSelect={(bar) => select(bar)}
            onDrop={onDrop}
          />
          <p className={styles.note}>
            Outlines: amber is late, dashed is firmed, red breaks a constraint. The table lists the same jobs for keyboard use.
          </p>
          {proposal ? (
            <div ref={formRef}>
              <Card title={`Move order ${shortId(proposal.job.orderId)} operation ${proposal.job.operNumber}`}>
                <form className={styles.form} onSubmit={submitPreview} noValidate>
                  <FormField id="move-resource" label="Resource" hint="The service refuses resources that are not candidates of the operation.">
                    {(control) => (
                      <Select
                        {...control}
                        value={proposal.resourceId}
                        onChange={(event) => {
                          setProposal({ ...proposal, resourceId: event.target.value });
                          setPreview(null);
                        }}
                      >
                        {resources.map((r) => (
                          <option key={r.id} value={r.id}>
                            {r.code}
                          </option>
                        ))}
                      </Select>
                    )}
                  </FormField>
                  <FormField id="move-start" label="New start (UTC)">
                    {(control) => (
                      <Input
                        {...control}
                        type="datetime-local"
                        step={300}
                        value={toUtcInput(proposal.start)}
                        onChange={(event) => {
                          const start = fromUtcInput(event.target.value);
                          if (start) {
                            setProposal({ ...proposal, start });
                            setPreview(null);
                          }
                        }}
                      />
                    )}
                  </FormField>
                  <div className={styles.formActions}>
                    <Button type="submit" loading={busy === "preview"}>
                      Preview move
                    </Button>
                    {preview?.response.feasible ? (
                      <Button variant="primary" loading={busy === "commit"} onClick={confirm}>
                        Confirm move
                      </Button>
                    ) : null}
                    <Button
                      variant="ghost"
                      onClick={() => {
                        setProposal(null);
                        setPreview(null);
                        setViolations({});
                      }}
                    >
                      Cancel
                    </Button>
                  </div>
                </form>
                {preview && !preview.response.feasible ? (
                  <Alert tone="warning" title="This move breaks a constraint and cannot be saved">
                    <ul className={styles.messages}>
                      {preview.response.violations.map((v) => (
                        <li key={`${v.code}-${v.jobId ?? ""}`}>
                          {v.detail} ({v.code})
                        </li>
                      ))}
                    </ul>
                  </Alert>
                ) : null}
                {preview?.response.feasible ? (
                  <div className={styles.stack}>
                    <p className={styles.note}>
                      {`${preview.response.changes.length} operation(s) move. The order runs ${formatDateTime(preview.response.orderStart)} to ${formatDateTime(preview.response.orderEnd)}.`}
                    </p>
                    <DataTable
                      label="Affected operations"
                      columns={[
                        { key: "operationId", header: "Operation", mono: true, render: (c) => shortId(c.operationId) },
                        { key: "resourceId", header: "Resource", mono: true, render: (c) => codeOf(c.resourceId) },
                        { key: "oldStart", header: "Was", render: (c) => formatDateTime(c.oldStart) },
                        { key: "newStart", header: "Becomes", render: (c) => formatDateTime(c.newStart) },
                        { key: "newEnd", header: "Ends", render: (c) => formatDateTime(c.newEnd) },
                        { key: "lateMinutes", header: "Late (min)", align: "end" },
                      ]}
                      rows={preview.response.changes}
                      getRowKey={(c) => c.operationId}
                    />
                  </div>
                ) : null}
              </Card>
            </div>
          ) : null}
          <DataTable
            label="Jobs in window"
            columns={columns}
            rows={jobs}
            getRowKey={(row) => row.jobId}
            loading={board.loading}
            empty={{ title: "No jobs in this window", description: "Run the schedule, or pick another date." }}
          />
        </>
      )}
    </div>
  );
}
