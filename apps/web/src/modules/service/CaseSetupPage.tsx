import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  CATEGORY_TYPES,
  createCaseCategory,
  createCaseProcess,
  describeProblem,
  listCaseCategories,
  listCaseProcesses,
  listSlaProfiles,
  newKey,
  PRIORITIES,
  type CaseCategory,
  type CaseCategoryInput,
  type CaseProcess,
  type CaseProcessInput,
  type CaseStage,
  type CategoryType,
  type Problem,
} from "./api/client";
import { dash, sentence } from "./format";
import styles from "./pages.module.css";
import { useLoad, type LoadState } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** Category and process codes, as the service checks them (domain.ValidCode). */
export const CODE = /^[A-Z0-9_-]{2,40}$/;

type Errors = Record<string, string>;

const categoryColumns = (processes: CaseProcess[], categories: CaseCategory[]): DataTableColumn<CaseCategory>[] => [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "categoryType", header: "Type", render: (row) => sentence(row.categoryType) },
  { key: "parentId", header: "Parent", mono: true, render: (row) => categories.find((c) => c.id === row.parentId)?.code ?? dash },
  { key: "processId", header: "Process", mono: true, render: (row) => processes.find((p) => p.id === row.processId)?.code ?? dash },
  { key: "accessDuty", header: "Access duty", mono: true, render: (row) => row.accessDuty || "Any case worker" },
  { key: "resolutionCodes", header: "Resolution codes", mono: true, render: (row) => (row.resolutionCodes.length > 0 ? row.resolutionCodes.join(", ") : "Any") },
];

const processColumns: DataTableColumn<CaseProcess>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  {
    key: "stages",
    header: "Stages",
    render: (row) => row.stages.map((s) => (s.requiredActivities.length > 0 ? `${s.code} (${s.requiredActivities.join(", ")})` : s.code)).join(" → "),
  },
];

function LoadFailed({ what, state }: { what: string; state: LoadState<unknown> }) {
  return state.error ? (
    <Alert tone="danger" title={`Could not load ${what}`} live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
      <ProblemMessage problem={state.error} />
    </Alert>
  ) : null;
}

/** Case category and process setup (PRD 8.45.6): the category tree with its process, SLA, codes and security, and the processes' stages. */
export function CaseSetupPage() {
  const navigate = useNavigate();
  const categories = useLoad((signal) => listCaseCategories(signal), "categories");
  const processes = useLoad((signal) => listCaseProcesses(signal), "processes");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Case setup"
        description="Categories decide each case's process, SLA profile, resolution codes and who may see it. Processes order the stages and the activities each stage requires."
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to="/service/case-setup/processes/new">
              New process
            </Link>
            <Link className={buttonClassName("primary")} to="/service/case-setup/categories/new">
              New category
            </Link>
          </div>
        }
      />
      <Card title="Categories" headingLevel={3}>
        <LoadFailed what="case categories" state={categories} />
        {categories.error ? null : (
          <DataTable
            label="Case categories"
            columns={categoryColumns(processes.data?.items ?? [], categories.data?.items ?? [])}
            rows={categories.data?.items ?? []}
            getRowKey={(row) => row.id}
            loading={categories.loading}
            empty={{ title: "No case categories yet", description: "Create a category to open HR, collections, audit and other cases." }}
            onRowOpen={(row) => navigate(`/service/case-management?categoryId=${row.id}`)}
          />
        )}
      </Card>
      <Card title="Processes" headingLevel={3}>
        <LoadFailed what="case processes" state={processes} />
        {processes.error ? null : (
          <DataTable
            label="Case processes"
            columns={processColumns}
            rows={processes.data?.items ?? []}
            getRowKey={(row) => row.id}
            loading={processes.loading}
            empty={{ title: "No case processes yet", description: "A process orders the stages a case moves through." }}
          />
        )}
      </Card>
    </div>
  );
}

/**
 * Parses one stage per line: `CODE` or `CODE: Activity one, Activity two`, where
 * the activities must be DONE before the case leaves the stage (BR-45.9).
 */
export function parseStages(text: string): { stages: CaseStage[]; error?: string } {
  const lines = text
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);
  if (lines.length < 1 || lines.length > 20) return { stages: [], error: "Enter 1 to 20 stages, one per line." };
  const stages: CaseStage[] = [];
  for (const [i, line] of lines.entries()) {
    const [rawCode, rest = ""] = line.split(/:(.*)/s);
    const code = rawCode.trim().toUpperCase();
    if (!CODE.test(code)) return { stages: [], error: `Line ${i + 1}: the stage code must be 2 to 40 characters of A-Z, 0-9, _ or -.` };
    if (stages.some((s) => s.code === code)) return { stages: [], error: `Stage ${code} appears twice.` };
    const requiredActivities = rest
      .split(",")
      .map((a) => a.trim())
      .filter(Boolean);
    if (requiredActivities.some((a) => [...a].length > 200)) return { stages: [], error: `Line ${i + 1}: activity names are at most 200 characters.` };
    stages.push({ code, sequence: i + 1, requiredActivities });
  }
  return { stages };
}

export function validateProcess(values: { code: string; name: string; stages: string }): { errors: Errors; input?: CaseProcessInput } {
  const errors: Errors = {};
  const code = values.code.trim().toUpperCase();
  if (!CODE.test(code)) errors.code = "Use 2 to 40 characters of A-Z, 0-9, _ or -.";
  const name = values.name.trim();
  if (name.length < 1 || [...name].length > 120) errors.name = "Enter a name of 1 to 120 characters.";
  const parsed = parseStages(values.stages);
  if (parsed.error) errors.stages = parsed.error;
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { code, name, stages: parsed.stages } };
}

/** One create intent: a stable Idempotency-Key until the values change, field errors from the service, and the failure. */
function useCreate(done: () => void) {
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  async function submit(create: (key: string) => Promise<unknown>, onFields: (fields: Errors) => void) {
    setFailure(null);
    setBusy(true);
    try {
      await create(key.current);
      done();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) onFields(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }
  return { failure, busy, submit, renew: () => (key.current = newKey()) };
}

/** Define a case process: its stages in order and the activities each stage requires. */
export function NewCaseProcessPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState({ code: "", name: "", stages: "" });
  const [errors, setErrors] = useState<Errors>({});
  const action = useCreate(() => navigate("/service/case-setup"));

  function update(patch: Partial<typeof values>) {
    setValues((v) => ({ ...v, ...patch }));
    action.renew();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateProcess(values);
    setErrors(found);
    if (!input) return;
    await action.submit((key) => createCaseProcess(input, key), setErrors);
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New case process" description="A case starts at the first stage. It moves on once the stage's required activities are done on the case, and back one stage with a reason." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New case process">
          <FormField id="process-code" label="Code" error={errors.code}>
            {(control) => <Input {...control} className={styles.mono} value={values.code} onChange={(e) => update({ code: e.target.value })} invalid={Boolean(errors.code)} />}
          </FormField>
          <FormField id="process-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} value={values.name} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
          </FormField>
          <div className={styles.wide}>
            <FormField id="process-stages" label="Stages" hint="One per line, in order: CODE or CODE: Required activity, Another activity." error={errors.stages}>
              {(control) => (
                <Textarea {...control} className={styles.mono} value={values.stages} onChange={(e) => update({ stages: e.target.value })} invalid={Boolean(errors.stages)} />
              )}
            </FormField>
          </div>
          {action.failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the process" live="alert">
                <ProblemMessage problem={action.failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={action.busy}>
              Create process
            </Button>
            <Link className={buttonClassName("ghost")} to="/service/case-setup">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

export interface CategoryValues {
  code: string;
  name: string;
  categoryType: CategoryType;
  parentId: string;
  processId: string;
  slaProfileId: string;
  defaultPriority: string;
  reopenWindowDays: string;
  accessDuty: string;
  resolutionCodes: string;
}

export function validateCategory(values: CategoryValues): { errors: Errors; input?: CaseCategoryInput } {
  const errors: Errors = {};
  const code = values.code.trim().toUpperCase();
  if (!CODE.test(code)) errors.code = "Use 2 to 40 characters of A-Z, 0-9, _ or -.";
  const name = values.name.trim();
  if (name.length < 1 || [...name].length > 120) errors.name = "Enter a name of 1 to 120 characters.";
  const days = Number(values.reopenWindowDays);
  if (!/^\d+$/.test(values.reopenWindowDays.trim()) || days > 365) errors.reopenWindowDays = "Enter 0 to 365 days.";
  const duty = values.accessDuty.trim();
  if (duty.length > 100) errors.accessDuty = "Use at most 100 characters.";
  const codes = values.resolutionCodes
    .split(",")
    .map((c) => c.trim().toUpperCase())
    .filter(Boolean);
  if (codes.length > 50 || codes.some((c) => !CODE.test(c)) || new Set(codes).size !== codes.length) {
    errors.resolutionCodes = "List up to 50 distinct codes of A-Z, 0-9, _ or -, separated by commas.";
  }
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      code,
      name,
      categoryType: values.categoryType,
      ...(values.parentId ? { parentId: values.parentId } : {}),
      ...(values.processId ? { processId: values.processId } : {}),
      ...(values.slaProfileId ? { slaProfileId: values.slaProfileId } : {}),
      defaultPriority: Number(values.defaultPriority),
      reopenWindowDays: days,
      ...(duty ? { accessDuty: duty } : {}),
      resolutionCodes: codes,
    },
  };
}

/** Define a case category: its type, place in the tree, process, SLA profile, resolution codes and access duty (FR-45.12, BR-45.10). */
export function NewCaseCategoryPage() {
  const navigate = useNavigate();
  const categories = useLoad((signal) => listCaseCategories(signal), "categories");
  const processes = useLoad((signal) => listCaseProcesses(signal), "processes");
  const profiles = useLoad((signal) => listSlaProfiles(signal), "profiles");
  const [values, setValues] = useState<CategoryValues>({
    code: "",
    name: "",
    categoryType: "INTERNAL",
    parentId: "",
    processId: "",
    slaProfileId: "",
    defaultPriority: "3",
    reopenWindowDays: "14",
    accessDuty: "",
    resolutionCodes: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const action = useCreate(() => navigate("/service/case-setup"));

  function update(patch: Partial<CategoryValues>) {
    setValues((v) => ({ ...v, ...patch }));
    action.renew();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateCategory(values);
    setErrors(found);
    if (!input) return;
    await action.submit((key) => createCaseCategory(input, key), setErrors);
  }

  const text = (name: "code" | "name" | "reopenWindowDays" | "accessDuty" | "resolutionCodes", label: string, hint?: string, optional = false) => (
    <FormField id={`category-${name}`} label={label} hint={hint} error={errors[name]} optional={optional}>
      {(control) => (
        <Input
          {...control}
          className={name === "name" ? undefined : styles.mono}
          value={values[name]}
          onChange={(e) => update({ [name]: e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  const option = (load: LoadState<{ items: { id: string; code: string; name: string }[] }>) =>
    (load.data?.items ?? []).map((x) => (
      <option key={x.id} value={x.id}>
        {`${x.code} — ${x.name}`}
      </option>
    ));

  return (
    <div className={styles.stack}>
      <PageHeader title="New case category" description="Service and customer-service categories keep the entitlement and SLA engine of service cases; the others use the category's own process and SLA profile." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New case category">
          {text("code", "Code")}
          {text("name", "Name")}
          <FormField id="category-type" label="Type">
            {(control) => (
              <Select {...control} value={values.categoryType} onChange={(e) => update({ categoryType: e.target.value as CategoryType })}>
                {CATEGORY_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="category-parent" label="Parent category" optional>
            {(control) => (
              <Select {...control} value={values.parentId} onChange={(e) => update({ parentId: e.target.value })}>
                <option value="">None (top level)</option>
                {option(categories)}
              </Select>
            )}
          </FormField>
          <FormField id="category-process" label="Process" optional>
            {(control) => (
              <Select {...control} value={values.processId} onChange={(e) => update({ processId: e.target.value })}>
                <option value="">None</option>
                {option(processes)}
              </Select>
            )}
          </FormField>
          <FormField id="category-sla" label="SLA profile" hint="Sets the response and resolution dues of new cases." optional>
            {(control) => (
              <Select {...control} value={values.slaProfileId} onChange={(e) => update({ slaProfileId: e.target.value })}>
                <option value="">None</option>
                {option(profiles)}
              </Select>
            )}
          </FormField>
          <FormField id="category-priority" label="Default priority">
            {(control) => (
              <Select {...control} value={values.defaultPriority} onChange={(e) => update({ defaultPriority: e.target.value })}>
                {PRIORITIES.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("reopenWindowDays", "Reopen window (days)", "Within this many days of resolution a case reopens itself; 0 always opens a new case.")}
          {text("accessDuty", "Access duty", "Only holders of this duty and the case owner see the category's cases.", true)}
          {text("resolutionCodes", "Resolution codes", "Comma separated; empty accepts any code.", true)}
          {[categories, processes, profiles].some((l) => l.error) ? (
            <div className={styles.formActions}>
              <Alert tone="warning" title="Some lists could not be loaded" live="alert">
                Parent categories, processes or SLA profiles are missing from the choices.
              </Alert>
            </div>
          ) : null}
          {action.failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the category" live="alert">
                <ProblemMessage problem={action.failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={action.busy}>
              Create category
            </Button>
            <Link className={buttonClassName("ghost")} to="/service/case-setup">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
