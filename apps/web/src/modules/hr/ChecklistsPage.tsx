import { useState, type FormEvent } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { getMyProfile } from "./api/selfService";
import {
  completeChecklist,
  finishTask,
  getChecklist,
  listChecklistTemplates,
  listOwnedTasks,
  listWorkerChecklists,
  startChecklist,
  type Checklist,
  type WorkerTask,
} from "./api/safety";
import { DATE, UUID, dayOf, todayIso } from "./format";
import styles from "./pages.module.css";
import { BackLink, Entry, Facts, FormActions, humanize, labelled, ListPage, Loaded, LookupField, opt, SelectField, Status, TextField, useAction, useSubmit } from "./parts";
import { useLoad } from "./useLoad";

type TaskAction = ReturnType<typeof useAction>;

export function TaskActions({ task, action }: { task: WorkerTask; action: TaskAction }) {
  const [evidence, setEvidence] = useState("");
  if (task.status !== "OPEN") return null;
  const finish = (status: "DONE" | "SKIPPED") =>
    void action.run(`${task.id}|${status}`, () => finishTask(task.id, { status, evidenceRef: opt(evidence) }), `${task.title} is ${status === "DONE" ? "done" : "skipped"}.`);
  return (
    <div className={styles.rowActions}>
      {task.evidenceRequired ? (
        <TextField id={`evidence-${task.id}`} label={`Evidence for ${task.title}`} value={evidence} maxLength={500} onChange={setEvidence} />
      ) : null}
      {(["DONE", "SKIPPED"] as const).map((status) => (
        <Button
          key={status}
          size="sm"
          loading={action.busy === `${task.id}|${status}`}
          disabled={action.busy !== null}
          aria-label={`${status === "DONE" ? "Mark done" : "Skip"}: ${task.title}`}
          onClick={() => finish(status)}
        >
          {status === "DONE" ? "Mark done" : "Skip"}
        </Button>
      ))}
    </div>
  );
}

function taskColumns(action: TaskAction): DataTableColumn<WorkerTask>[] {
  const today = todayIso();
  return [
    { key: "title", header: "Task", render: (row) => <Link to={`/hr/checklists/${row.checklistId}`}>{row.title}</Link> },
    { key: "ownerRole", header: "Owner", render: (row) => humanize(row.ownerRole) },
    {
      key: "dueOn",
      header: "Due",
      mono: true,
      render: (row) => (
        <>
          {dayOf(row.dueOn)} {row.status === "OPEN" && row.dueOn < today ? <Badge tone="danger">Overdue</Badge> : null}
        </>
      ),
    },
    { key: "status", header: "Status", render: (row) => <Status value={row.status} /> },
    { key: "actions", header: "Actions", render: (row) => <TaskActions task={row} action={action} /> },
  ];
}

const progress = (checklist: Checklist) => `${checklist.tasks.filter((t) => t.status !== "OPEN").length} of ${checklist.tasks.length} finished`;

const checklistColumns: DataTableColumn<Checklist>[] = [
  { key: "kind", header: "Checklist", render: (row) => <Link to={`/hr/checklists/${row.id}`}>{humanize(row.kind)}</Link> },
  { key: "effectiveDate", header: "Effective", mono: true, render: (row) => dayOf(row.effectiveDate) },
  { key: "status", header: "Status", render: (row) => <Status value={row.status} /> },
  { key: "progress", header: "Progress", render: progress },
];

export interface StartValues {
  templateId: string;
  effectiveDate: string;
  legalEntityId: string;
}

export function validateStart(v: StartValues) {
  const errors: Record<string, string> = {};
  if (!UUID.test(v.templateId.trim())) errors.templateId = "Choose a template.";
  if (!DATE.test(v.effectiveDate)) errors.effectiveDate = "Choose the effective date.";
  if (v.legalEntityId.trim() !== "" && !UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's id.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { templateId: v.templateId.trim(), effectiveDate: v.effectiveDate, legalEntityId: opt(v.legalEntityId) } };
}

function StartForm({ workerId, onStarted }: { workerId: string; onStarted(checklist: Checklist): void }) {
  const templates = useLoad((signal) => listChecklistTemplates(undefined, signal), "start-templates");
  const [values, setValues] = useState<StartValues>({ templateId: "", effectiveDate: todayIso(), legalEntityId: "" });
  const form = useSubmit({ "HCM-CHK-TEMPLATE-NOT-FOUND": "templateId", "HCM-ENTITY-REQUIRED": "legalEntityId", "HCM-ENTITY-MISMATCH": "legalEntityId" });
  const set = (patch: Partial<StartValues>) => {
    setValues({ ...values, ...patch });
    form.touch();
  };

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors, input } = validateStart(values);
    form.setErrors(errors);
    form.setFailure(null);
    if (!input) return;
    await form.run(() => startChecklist(workerId, input), onStarted);
  }

  return (
    <Card title="Start a checklist">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Start a checklist">
        {templates.data ? (
          <SelectField
            id="start-templateId"
            label="Template"
            value={values.templateId}
            error={form.errors.templateId}
            onChange={(v) => set({ templateId: v })}
            options={templates.data.map((t) => [t.id, `${t.name} (${humanize(t.kind)})`] as const)}
            placeholder={templates.data.length === 0 ? "No templates yet" : "Choose a template"}
          />
        ) : (
          <TextField
            id="start-templateId"
            label="Template id"
            hint={templates.loading ? "Loading templates." : "The template list needs the setup administrator duty; enter the template's id."}
            value={values.templateId}
            error={form.errors.templateId}
            onChange={(v) => set({ templateId: v })}
          />
        )}
        <TextField id="start-effectiveDate" label="Effective date" type="date" value={values.effectiveDate} error={form.errors.effectiveDate} onChange={(v) => set({ effectiveDate: v })} />
        <LookupField
          entity="legalEntity"
          prefill={false}
          id="start-legalEntityId"
          label="Legal entity"
          optional
          hint="Needed when the worker is employed in several legal entities."
          value={values.legalEntityId}
          error={form.errors.legalEntityId}
          onChange={(v) => set({ legalEntityId: v })}
        />
        <FormActions errors={form.errors} busy={form.busy} submit="Start checklist" failure={form.failure} failureTitle="Could not start the checklist" />
      </form>
    </Card>
  );
}

/** A worker's onboarding, offboarding and transfer checklists. Starting one again for the same date returns the stored checklist. */
export function WorkerChecklistsPage() {
  const { id = "" } = useParams();
  const [version, setVersion] = useState(0);
  const [started, setStarted] = useState<Checklist | null>(null);
  return (
    <ListPage<Checklist>
      title="Worker checklists"
      description="Onboarding, offboarding and transfer tasks for this worker."
      label="Worker checklists"
      noun="checklists"
      load={async (_cursor, signal) => ({ items: await listWorkerChecklists(id, signal) })}
      loadKey={`worker-checklists|${id}|${version}`}
      columns={checklistColumns}
      getRowKey={(row) => row.id}
      empty={{ title: "No checklists", description: "Start one from a template below." }}
      actions={<BackLink to={`/hr/workers/${id}`}>Back to the worker</BackLink>}
      banner={
        <>
          <Alert tone="success" live="status">
            {started ? `Started the ${humanize(started.kind).toLowerCase()} checklist effective ${dayOf(started.effectiveDate)}.` : null}
          </Alert>
          <StartForm
            workerId={id}
            onStarted={(checklist) => {
              setStarted(checklist);
              setVersion((v) => v + 1);
            }}
          />
        </>
      }
    />
  );
}

export function ChecklistPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getChecklist(id, signal), id);
  const action = useAction(state.reload);

  return (
    <Loaded state={state} title="Checklist" what="checklist" backTo="/hr/tasks">
      {(checklist) => (
        <div className={styles.stack}>
          <PageHeader
            title={`${humanize(checklist.kind)} checklist`}
            meta={<Status value={checklist.status} />}
            actions={<BackLink to={`/hr/workers/${checklist.workerId}/checklists`}>Back to the worker's checklists</BackLink>}
          />
          {action.feedback}
          <Card
            title="Checklist"
            actions={
              checklist.status === "OPEN" ? (
                <Button variant="primary" loading={action.busy === "complete"} onClick={() => void action.run("complete", () => completeChecklist(checklist.id), "The checklist is complete.")}>
                  Complete checklist
                </Button>
              ) : null
            }
          >
            <Facts>
              <Entry term="Worker" mono>
                <Link to={`/hr/workers/${checklist.workerId}`}>{checklist.workerId}</Link>
              </Entry>
              <Entry term="Effective" mono>
                {dayOf(checklist.effectiveDate)}
              </Entry>
              <Entry term="Progress">{progress(checklist)}</Entry>
            </Facts>
            <p className={styles.note}>Every task must be done or skipped, and an offboarding checklist can't finish while an item is still out on loan.</p>
          </Card>
          <Card title="Tasks">
            <DataTable
              label="Checklist tasks"
              columns={taskColumns(action)}
              rows={checklist.tasks}
              getRowKey={(row) => row.id}
              empty={{ title: "No tasks" }}
            />
          </Card>
        </div>
      )}
    </Loaded>
  );
}

const TASK_STATUSES = ["OPEN", "DONE", "SKIPPED"];

/** Tasks owned by one worker, soonest due first. */
export function TasksPage() {
  const [params, setParams] = useSearchParams();
  const rawOwner = params.get("owner") ?? "";
  const owner = UUID.test(rawOwner) ? rawOwner : "";
  const rawStatus = params.get("status");
  const status = rawStatus === null ? "OPEN" : TASK_STATUSES.includes(rawStatus) ? rawStatus : "";
  const me = useLoad((signal) => getMyProfile(signal), "tasks-me");
  const [version, setVersion] = useState(0);
  const action = useAction(() => setVersion((v) => v + 1));

  const setParam = (name: string, value: string) => {
    const next = new URLSearchParams(params);
    next.set(name, value);
    setParams(next, { replace: true });
  };

  return (
    <ListPage<WorkerTask>
      title="Tasks"
      description="Checklist tasks owned by a worker, such as a manager's onboarding steps."
      label="Tasks"
      noun="tasks"
      load={async (_cursor, signal) => ({ items: owner ? await listOwnedTasks({ ownerWorkerId: owner, status }, signal) : [] })}
      loadKey={`tasks|${owner}|${status}|${version}`}
      columns={taskColumns(action)}
      getRowKey={(row) => row.id}
      empty={{ title: owner ? "No tasks" : "Choose an owner", description: owner ? "Nothing matches this status." : "Enter a worker id, or use your own." }}
      banner={action.feedback}
      toolbar={
        <>
          <LookupField
            entity="worker"
            id="tasks-owner"
            label="Owner"
            value={rawOwner}
            error={rawOwner !== "" && owner === "" ? "Enter a worker id." : undefined}
            onChange={(v) => setParam("owner", v)}
          />
          <Button disabled={!me.data} onClick={() => me.data && setParam("owner", me.data.id)}>
            My tasks
          </Button>
          <SelectField id="tasks-status" label="Status" value={status} onChange={(v) => setParam("status", v)} options={labelled(TASK_STATUSES)} placeholder="All" />
        </>
      }
    />
  );
}
