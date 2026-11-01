import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Button, buttonClassName, Card, PageHeader, type DataTableColumn } from "../../shell/ui";
import { createChecklistTemplate, listChecklistTemplates, OWNER_ROLES, RETURN_LOANED_ITEMS, type ChecklistKind, type ChecklistTemplate, type NewChecklistTemplate, type OwnerRole } from "./api/safety";
import styles from "./pages.module.css";
import { CheckField, FormActions, humanize, ListPage, labelled, opt, SelectField, TextField, useSubmit } from "./parts";

const KINDS: ChecklistKind[] = ["ONBOARDING", "OFFBOARDING", "TRANSFER"];
const MAX_TASKS = 50;

const columns: DataTableColumn<ChecklistTemplate>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "kind", header: "Kind", render: (row) => humanize(row.kind) },
  { key: "tasks", header: "Tasks", align: "end", render: (row) => String(row.tasks.length) },
];

/** Onboarding, offboarding and transfer checklist templates. Offboarding adds a loaned-items return task by itself. */
export function ChecklistTemplatesPage() {
  const [params, setParams] = useSearchParams();
  const raw = params.get("kind") ?? "";
  const kind = KINDS.includes(raw as ChecklistKind) ? raw : "";
  return (
    <ListPage<ChecklistTemplate>
      title="Checklist templates"
      description="The tasks started for a worker on a given date, each owned by a manager, the worker, HR or IT."
      label="Checklist templates"
      noun="templates"
      load={async (_cursor, signal) => ({ items: await listChecklistTemplates(kind, signal) })}
      loadKey={`templates|${kind}`}
      columns={columns}
      getRowKey={(row) => row.id}
      empty={{ title: "No checklist templates", description: kind ? "No template of this kind." : "Define a template to start onboarding and offboarding checklists." }}
      actions={
        <Link className={buttonClassName("primary")} to="/hr/checklist-templates/new">
          New template
        </Link>
      }
      toolbar={
        <SelectField
          id="template-kind"
          label="Kind"
          value={kind}
          onChange={(value) => setParams(value ? { kind: value } : {}, { replace: true })}
          options={labelled(KINDS)}
          placeholder="All kinds"
        />
      }
    />
  );
}

export interface TaskValues {
  code: string;
  title: string;
  ownerRole: string;
  dueOffsetDays: string;
  instructions: string;
  evidenceRequired: boolean;
}

export interface TemplateValues {
  code: string;
  kind: string;
  name: string;
  tasks: TaskValues[];
}

const blankTask = (): TaskValues => ({ code: "", title: "", ownerRole: "MANAGER", dueOffsetDays: "0", instructions: "", evidenceRequired: false });

export function validateTemplate(v: TemplateValues): { errors: Record<string, string>; input?: NewChecklistTemplate } {
  const errors: Record<string, string> = {};
  const code = v.code.trim().toUpperCase();
  if (!/^[A-Z0-9][A-Z0-9_-]{0,19}$/.test(code)) errors.code = "Use 1 to 20 characters: A to Z, 0 to 9, a hyphen or an underscore.";
  const name = v.name.trim();
  if (name === "" || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  const seen = new Set<string>();
  v.tasks.forEach((t, i) => {
    const taskCode = t.code.trim().toUpperCase();
    if (!/^[A-Z0-9][A-Z0-9_-]{0,29}$/.test(taskCode) || seen.has(taskCode) || taskCode === RETURN_LOANED_ITEMS) {
      errors[`tasks[${i}].code`] = `Use a unique code of 1 to 30 characters (A to Z, 0 to 9, hyphen, underscore), not ${RETURN_LOANED_ITEMS}.`;
    }
    seen.add(taskCode);
    const title = t.title.trim();
    if (title === "" || title.length > 200) errors[`tasks[${i}].title`] = "Enter a title of up to 200 characters.";
    const offset = Number(t.dueOffsetDays);
    if (!/^-?\d{1,3}$/.test(t.dueOffsetDays.trim()) || offset < -365 || offset > 365) errors[`tasks[${i}].dueOffsetDays`] = "Enter -365 to 365 days from the start date.";
    if (t.instructions.trim().length > 2000) errors[`tasks[${i}].instructions`] = "Use up to 2000 characters.";
  });
  if (v.tasks.length === 0) errors.tasks = "Add at least one task.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      code,
      kind: v.kind as ChecklistKind,
      name,
      tasks: v.tasks.map((t) => ({
        code: t.code.trim().toUpperCase(),
        title: t.title.trim(),
        ownerRole: t.ownerRole as OwnerRole,
        dueOffsetDays: Number(t.dueOffsetDays),
        instructions: opt(t.instructions),
        evidenceRequired: t.evidenceRequired,
      })),
    },
  };
}

export function NewChecklistTemplatePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<TemplateValues>({ code: "", kind: "ONBOARDING", name: "", tasks: [blankTask()] });
  const form = useSubmit({ "HCM-CHK-TEMPLATE-DUPLICATE": "code" });
  const set = (patch: Partial<TemplateValues>) => {
    setValues({ ...values, ...patch });
    form.touch();
  };
  const setTask = (i: number, patch: Partial<TaskValues>) => set({ tasks: values.tasks.map((t, j) => (j === i ? { ...t, ...patch } : t)) });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors, input } = validateTemplate(values);
    form.setErrors(errors);
    form.setFailure(null);
    if (!input) return;
    await form.run((key) => createChecklistTemplate(input, key), () => navigate("/hr/checklist-templates"));
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New checklist template" />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <TextField id="template-code" label="Code" value={values.code} error={form.errors.code} onChange={(v) => set({ code: v })} />
          <TextField id="template-name" label="Name" value={values.name} error={form.errors.name} onChange={(v) => set({ name: v })} />
          <SelectField id="template-new-kind" label="Kind" value={values.kind} error={form.errors.kind} onChange={(v) => set({ kind: v })} options={labelled(KINDS)} />
          <fieldset className={styles.fieldset}>
            <legend className={styles.legend}>Tasks</legend>
            {values.tasks.map((task, i) => (
              <div key={i} className={styles.optionRow} role="group" aria-label={`Task ${i + 1}`}>
                <TextField id={`task-${i}-code`} label="Code" value={task.code} error={form.errors[`tasks[${i}].code`]} onChange={(v) => setTask(i, { code: v })} />
                <TextField id={`task-${i}-title`} label="Title" value={task.title} error={form.errors[`tasks[${i}].title`]} onChange={(v) => setTask(i, { title: v })} />
                <SelectField id={`task-${i}-role`} label="Owner" value={task.ownerRole} error={form.errors[`tasks[${i}].ownerRole`]} onChange={(v) => setTask(i, { ownerRole: v })} options={labelled(OWNER_ROLES)} />
                <TextField
                  id={`task-${i}-offset`}
                  label="Due (days from start)"
                  inputMode="numeric"
                  value={task.dueOffsetDays}
                  error={form.errors[`tasks[${i}].dueOffsetDays`]}
                  onChange={(v) => setTask(i, { dueOffsetDays: v })}
                />
                <TextField id={`task-${i}-instructions`} label="Instructions" optional value={task.instructions} error={form.errors[`tasks[${i}].instructions`]} onChange={(v) => setTask(i, { instructions: v })} />
                <CheckField id={`task-${i}-evidence`} label="Evidence required" checked={task.evidenceRequired} onChange={(evidenceRequired) => setTask(i, { evidenceRequired })} />
                {values.tasks.length > 1 ? (
                  <div className={styles.optionRemove}>
                    <Button variant="ghost" size="sm" aria-label={`Remove task ${i + 1}`} onClick={() => set({ tasks: values.tasks.filter((_, j) => j !== i) })}>
                      Remove
                    </Button>
                  </div>
                ) : null}
              </div>
            ))}
            {form.errors.tasks ? <p role="alert">{form.errors.tasks}</p> : null}
            {values.tasks.length < MAX_TASKS ? (
              <div>
                <Button size="sm" onClick={() => set({ tasks: [...values.tasks, blankTask()] })}>
                  Add task
                </Button>
              </div>
            ) : null}
          </fieldset>
          <FormActions busy={form.busy} submit="Add template" cancelTo="/hr/checklist-templates" failure={form.failure} failureTitle="Could not add the template" />
        </form>
      </Card>
    </div>
  );
}
