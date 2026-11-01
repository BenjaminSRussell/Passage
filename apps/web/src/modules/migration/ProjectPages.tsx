import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  createProject,
  describeProblem,
  getKeyRewriteRules,
  getProject,
  listProjects,
  listRuns,
  newKey,
  previewKeyRewrite,
  replaceKeyRewriteRules,
  updateProject,
  type KeyRewrite,
  type KeyRewritePreview,
  type MigrationProject,
  type MigrationRun,
  type Problem,
} from "./api/client";
import {
  checkRewriteRules,
  cutoverInput,
  formatDateTime,
  MODE_LABELS,
  parseProjectEdit,
  parseProjectForm,
  PROJECT_TYPE_LABELS,
  SOURCE_LABELS,
  SOURCE_SYSTEMS,
  type ProjectEditForm,
  type ProjectForm,
} from "./domain";
import { DUTY_RUN } from "./RunPages";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const DUTY_PROJECT_WRITE = "migration.project.write";

const columns: DataTableColumn<MigrationProject>[] = [
  { key: "name", header: "Name" },
  { key: "projectType", header: "Type", render: (row) => PROJECT_TYPE_LABELS[row.projectType] ?? row.projectType },
  { key: "sourceSystem", header: "Source", render: (row) => (row.sourceSystem ? (SOURCE_LABELS[row.sourceSystem] ?? row.sourceSystem) : "") },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "cutoverAt", header: "Cutover", render: (row) => (row.cutoverAt ? formatDateTime(row.cutoverAt) : "") },
  { key: "version", header: "Version", align: "end" },
];

/** Migration projects: the unit a legacy migration, a copy between legal entities or a package apply belongs to. */
export function ProjectsPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_PROJECT_WRITE);
  const [type, setType] = useState("");
  const [status, setStatus] = useState("");
  const key = `${type}|${status}`;
  const keyRef = useRef(key);
  keyRef.current = key;
  const first = useLoad((signal) => listProjects({ projectType: type || undefined, status: status || undefined }, signal), key);
  const [more, setMore] = useState<MigrationProject[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);

  // A new first page (new filter or reload) replaces everything fetched after it.
  useEffect(() => {
    setMore([]);
    setCursor(first.data?.nextCursor);
    setMoreError(null);
  }, [first.data]);

  async function loadMore() {
    if (!cursor) return;
    const requested = key;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listProjects({ projectType: type || undefined, status: status || undefined, cursor });
      if (keyRef.current !== requested) return;
      setMore((prev) => [...prev, ...page.items]);
      setCursor(page.nextCursor);
    } catch (e) {
      if (keyRef.current === requested) setMoreError(describeProblem(e));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Migration projects"
        description="A project groups the runs, rules and schedule of one migration, one copy between legal entities, or one configuration package."
        actions={
          <>
            <Link className={buttonClassName("ghost")} to="/migration">
              Runs
            </Link>
            {canWrite ? (
              <Link className={buttonClassName("primary")} to="/migration/projects/new">
                New project
              </Link>
            ) : null}
          </>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="projects-type" label="Type">
          {(control) => (
            <Select {...control} value={type} onChange={(event) => setType(event.target.value)}>
              <option value="">All</option>
              {Object.entries(PROJECT_TYPE_LABELS).map(([value, label]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="projects-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value)}>
              <option value="">All</option>
              <option value="active">Active</option>
              <option value="archived">Archived</option>
            </Select>
          )}
        </FormField>
      </div>
      {first.error ? (
        <Alert tone="danger" title="Could not load migration projects" live="alert" action={<Button size="sm" onClick={first.reload}>Try again</Button>}>
          {first.error.message} ({first.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Migration projects"
          columns={columns}
          rows={[...(first.data?.items ?? []), ...more]}
          getRowKey={(row) => row.id}
          loading={first.loading}
          empty={{ title: "No migration projects yet", description: "Create a project to set up a copy between legal entities or a legacy migration." }}
          onRowOpen={(row) => navigate(`/migration/projects/${row.id}`)}
        />
      )}
      {moreError ? (
        <Alert tone="danger" title="Could not load more projects" live="alert">
          {moreError.message} ({moreError.code})
        </Alert>
      ) : null}
      {cursor && !first.error ? (
        <div>
          <Button onClick={loadMore} disabled={loadingMore}>
            {loadingMore ? "Loading" : "Load more"}
          </Button>
        </div>
      ) : null}
    </div>
  );
}

const CRON_HINT = "Five fields, for example 0 2 * * *, at least 15 minutes apart. Stored for recurring incremental imports; nothing runs it yet.";

const EMPTY_FORM: ProjectForm = { name: "", projectType: "copy", sourceSystem: "", sourceLegalEntityId: "", targetLegalEntityId: "", cutoverAt: "", scheduleCron: "" };

/** Create a project. Its type, source system and legal entities cannot be changed afterwards. */
export function NewProjectPage() {
  const navigate = useNavigate();
  const [form, setForm] = useState<ProjectForm>(EMPTY_FORM);
  const [errors, setErrors] = useState<Partial<Record<keyof ProjectForm, string>>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function change<K extends keyof ProjectForm>(field: K, value: ProjectForm[K]) {
    setForm((prev) => ({ ...prev, [field]: value }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const parsed = parseProjectForm(form);
    setErrors(parsed.errors);
    if (!parsed.value) return;
    setBusy(true);
    try {
      const saved = await createProject(parsed.value, key.current);
      navigate(`/migration/projects/${saved.id}`);
    } catch (e) {
      const problem = describeProblem(e);
      setFailure(problem);
      // Field messages from the service land next to their fields.
      if (problem.errors) setErrors((prev) => ({ ...prev, ...problem.errors }));
      key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New migration project" description="The type, source system and legal entities are fixed once the project exists, because copy lineage is recorded per project." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="project-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} value={form.name} invalid={Boolean(errors.name)} onChange={(event) => change("name", event.target.value)} />}
          </FormField>
          <FormField id="project-type" label="Type">
            {(control) => (
              <Select {...control} value={form.projectType} onChange={(event) => change("projectType", event.target.value as ProjectForm["projectType"])}>
                {Object.entries(PROJECT_TYPE_LABELS).map(([value, label]) => (
                  <option key={value} value={value}>
                    {label}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {form.projectType === "migrate" ? (
            <FormField id="project-system" label="Source system" error={errors.sourceSystem}>
              {(control) => (
                <Select {...control} value={form.sourceSystem} onChange={(event) => change("sourceSystem", event.target.value)}>
                  <option value="">Choose a system</option>
                  {SOURCE_SYSTEMS.map((s) => (
                    <option key={s} value={s}>
                      {SOURCE_LABELS[s]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          ) : null}
          {form.projectType === "copy" ? (
            <>
              <FormField id="project-source-le" label="Source legal entity id" error={errors.sourceLegalEntityId}>
                {(control) => <Input {...control} value={form.sourceLegalEntityId} invalid={Boolean(errors.sourceLegalEntityId)} onChange={(event) => change("sourceLegalEntityId", event.target.value)} />}
              </FormField>
              <FormField id="project-target-le" label="Target legal entity id" error={errors.targetLegalEntityId}>
                {(control) => <Input {...control} value={form.targetLegalEntityId} invalid={Boolean(errors.targetLegalEntityId)} onChange={(event) => change("targetLegalEntityId", event.target.value)} />}
              </FormField>
            </>
          ) : null}
          <FormField id="project-cutover" label="Cutover (UTC, optional)" error={errors.cutoverAt}>
            {(control) => <Input {...control} type="datetime-local" value={form.cutoverAt} invalid={Boolean(errors.cutoverAt)} onChange={(event) => change("cutoverAt", event.target.value)} />}
          </FormField>
          <FormField id="project-cron" label="Schedule (cron, optional)" error={errors.scheduleCron} hint={CRON_HINT}>
            {(control) => <Input {...control} value={form.scheduleCron} invalid={Boolean(errors.scheduleCron)} onChange={(event) => change("scheduleCron", event.target.value)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the project" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create project
            </Button>
            <Link className={buttonClassName("ghost")} to="/migration/projects">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

function Entry({ term, children }: { term: string; children: React.ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}

/** One project with its details, archive and reactivate, and (for a copy project) its key rewrite rules. */
export function ProjectDetailPage() {
  const { id = "" } = useParams();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_PROJECT_WRITE);
  const state = useLoad((signal) => getProject(id, signal), id);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Migration project" />
        <Alert tone="danger" title="Could not load this project" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link className={buttonClassName("ghost")} to="/migration/projects">
          Back to projects
        </Link>
      </div>
    );
  }
  const project = state.data?.id === id ? state.data : null;
  if (!project) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Migration project" description="Loading..." />
      </div>
    );
  }

  async function toggle(current: MigrationProject) {
    setBusy(true);
    setFailure(null);
    try {
      state.set(await updateProject(current.id, current.version, { status: current.status === "active" ? "archived" : "active" }));
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={project.name}
        description={`${PROJECT_TYPE_LABELS[project.projectType] ?? project.projectType}. Created ${formatDateTime(project.createdAt)}.`}
        actions={
          <>
            <StatusPill status={project.status} />
            {canWrite ? (
              <Button loading={busy} onClick={() => toggle(project)}>
                {project.status === "active" ? "Archive project" : "Reactivate project"}
              </Button>
            ) : null}
          </>
        }
      />
      {failure ? (
        <Alert tone="danger" title="Could not change the project" live="alert" action={<Button size="sm" onClick={state.reload}>Reload</Button>}>
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <DetailsCard project={project} canEdit={canWrite && project.status === "active"} onSaved={state.set} />
      {project.projectType === "migrate" ? <ProjectRunsCard project={project} /> : null}
      {project.projectType === "copy" ? <RewriteRulesCard project={project} canWrite={canWrite} onSaved={state.reload} /> : null}
      <Link className={buttonClassName("ghost")} to="/migration/projects">
        Back to projects
      </Link>
    </div>
  );
}

const runColumns: DataTableColumn<MigrationRun>[] = [
  { key: "createdAt", header: "Started", render: (row) => formatDateTime(row.createdAt) },
  { key: "entity", header: "Entity", mono: true },
  { key: "mode", header: "Mode", render: (row) => `${MODE_LABELS[row.mode] ?? row.mode}${row.dryRun ? " (dry run)" : ""}` },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "loaded", header: "Loaded", align: "end", render: (row) => row.counts.created + row.counts.updated },
  { key: "rejected", header: "Rejected", align: "end", render: (row) => row.counts.rejected },
];

/** The runs started under a migrate project. Only one run can be active per project, so a start can answer 409. */
function ProjectRunsCard({ project }: { project: MigrationProject }) {
  const navigate = useNavigate();
  const canRun = holdsDuty(usePrincipalDuties(), DUTY_RUN);
  const state = useLoad((signal) => listRuns({ projectId: project.id }, signal), project.id);
  return (
    <Card
      title="Runs"
      actions={
        canRun && project.status === "active" ? (
          <Link className={buttonClassName("primary", "sm")} to={`/migration/runs/new?project=${project.id}`}>
            Start run
          </Link>
        ) : undefined
      }
    >
      {state.error ? (
        <Alert tone="danger" title="Could not load the project's runs" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Project runs"
          columns={runColumns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No runs under this project yet", description: "Start a run from a published mapping spec to see counts and rejects here." }}
          onRowOpen={(row) => navigate(`/migration/runs/${row.id}`)}
        />
      )}
    </Card>
  );
}

/** The project's details, with an edit form for the name, cutover and schedule while the project is active. */
function DetailsCard({ project, canEdit, onSaved }: { project: MigrationProject; canEdit: boolean; onSaved: (saved: MigrationProject) => void }) {
  const [form, setForm] = useState<ProjectEditForm | null>(null);
  const [errors, setErrors] = useState<Partial<Record<keyof ProjectEditForm, string>>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  function open() {
    setForm({ name: project.name, cutoverAt: cutoverInput(project.cutoverAt), scheduleCron: project.scheduleCron ?? "" });
    setErrors({});
    setFailure(null);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!form) return;
    setFailure(null);
    const parsed = parseProjectEdit(form, project);
    setErrors(parsed.errors);
    if (!parsed.value) return;
    if (Object.keys(parsed.value).length === 0) {
      setForm(null);
      return;
    }
    setBusy(true);
    try {
      onSaved(await updateProject(project.id, project.version, parsed.value));
      setForm(null);
    } catch (e) {
      const problem = describeProblem(e);
      setFailure(problem);
      if (problem.errors) setErrors((prev) => ({ ...prev, ...problem.errors }));
    } finally {
      setBusy(false);
    }
  }

  if (form && canEdit) {
    const set = (field: keyof ProjectEditForm, value: string) => setForm((prev) => (prev ? { ...prev, [field]: value } : prev));
    return (
      <Card title="Details">
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="edit-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} value={form.name} invalid={Boolean(errors.name)} onChange={(event) => set("name", event.target.value)} />}
          </FormField>
          <FormField id="edit-cutover" label="Cutover (UTC, optional)" error={errors.cutoverAt} hint="Leave empty to clear it.">
            {(control) => <Input {...control} type="datetime-local" value={form.cutoverAt} invalid={Boolean(errors.cutoverAt)} onChange={(event) => set("cutoverAt", event.target.value)} />}
          </FormField>
          <FormField id="edit-cron" label="Schedule (cron, optional)" error={errors.scheduleCron} hint={CRON_HINT}>
            {(control) => <Input {...control} value={form.scheduleCron} invalid={Boolean(errors.scheduleCron)} onChange={(event) => set("scheduleCron", event.target.value)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the details" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save details
            </Button>
            <Button onClick={() => setForm(null)}>Cancel</Button>
          </div>
        </form>
      </Card>
    );
  }
  return (
    <Card title="Details" actions={canEdit ? <Button size="sm" onClick={open}>Edit details</Button> : undefined}>
      <dl className={styles.details}>
        {project.sourceSystem ? <Entry term="Source system">{SOURCE_LABELS[project.sourceSystem] ?? project.sourceSystem}</Entry> : null}
        {project.sourceLegalEntityId ? <Entry term="Source legal entity"><span className={styles.mono}>{project.sourceLegalEntityId}</span></Entry> : null}
        {project.targetLegalEntityId ? <Entry term="Target legal entity"><span className={styles.mono}>{project.targetLegalEntityId}</span></Entry> : null}
        <Entry term="Cutover">{project.cutoverAt ? formatDateTime(project.cutoverAt) : "Not set"}</Entry>
        <Entry term="Schedule">{project.scheduleCron ? <span className={styles.mono}>{project.scheduleCron}</span> : "Not scheduled"}</Entry>
        <Entry term="Version">{project.version}</Entry>
      </dl>
    </Card>
  );
}

const previewColumns: DataTableColumn<KeyRewritePreview>[] = [
  { key: "key", header: "Source key", mono: true },
  { key: "rewritten", header: "Key in the target", mono: true },
  { key: "changed", header: "Changed", render: (row) => (row.changed ? "Yes" : "No") },
];

/** The ordered key rewrite rules of a copy project, with a preview on sample keys before saving. */
function RewriteRulesCard({ project, canWrite: holdsWrite, onSaved }: { project: MigrationProject; canWrite: boolean; onSaved: () => void }) {
  // An archived project's rules are frozen until it is reactivated.
  const canWrite = holdsWrite && project.status === "active";
  const rules = useLoad((signal) => getKeyRewriteRules(project.id, signal), `${project.id}@${project.version}`);
  const [rows, setRows] = useState<KeyRewrite[]>([]);
  const [problems, setProblems] = useState<(string | undefined)[]>([]);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const [sample, setSample] = useState("");
  const [preview, setPreview] = useState<KeyRewritePreview[] | null>(null);
  const [previewError, setPreviewError] = useState<Problem | null>(null);

  useEffect(() => {
    if (rules.data) setRows(rules.data.items);
  }, [rules.data]);

  function edit(index: number, patch: Partial<KeyRewrite>) {
    setRows((prev) => prev.map((r, i) => (i === index ? { ...r, ...patch } : r)));
    setSaved(false);
    setPreview(null);
  }

  function trimmed(): KeyRewrite[] {
    return rows.map((r) => ({ ...r, field: r.field.trim() }));
  }

  async function save() {
    setFailure(null);
    setSaved(false);
    const found = checkRewriteRules(rows);
    setProblems(found);
    if (found.some(Boolean) || !rules.data) return;
    setBusy(true);
    try {
      const stored = await replaceKeyRewriteRules(project.id, rules.data.version, trimmed());
      rules.set(stored);
      setRows(stored.items);
      setSaved(true);
      onSaved();
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  async function runPreview() {
    setPreviewError(null);
    const keys = sample.split("\n").map((k) => k.trim()).filter(Boolean);
    const found = checkRewriteRules(rows);
    setProblems(found);
    if (keys.length === 0 || found.some(Boolean)) {
      if (keys.length === 0) setPreviewError({ code: "COM-0001", message: "Enter at least one sample key, one per line." });
      return;
    }
    try {
      setPreview(await previewKeyRewrite(project.id, keys, trimmed()));
    } catch (e) {
      setPreview(null);
      setPreviewError(describeProblem(e));
    }
  }

  if (rules.error) {
    return (
      <Card title="Key rewrite rules">
        <Alert tone="danger" title="Could not load the key rewrite rules" live="alert" action={<Button size="sm" onClick={rules.reload}>Try again</Button>}>
          {rules.error.message} ({rules.error.code})
        </Alert>
      </Card>
    );
  }
  return (
    <Card title="Key rewrite rules">
      <div className={styles.stack}>
        <p className={styles.note}>
          When a copy runs, a field value starting with the old prefix gets the new prefix in the target legal entity. Use <code>@key</code> as the field for the record&apos;s own key. For one field the first matching rule wins.
        </p>
        {holdsWrite && !canWrite ? <p className={styles.note}>The project is archived: reactivate it to change its rules.</p> : null}
        {rules.loading && !rules.data ? <p className={styles.note}>Loading rules...</p> : null}
        {!rules.loading && rows.length === 0 ? <p className={styles.note}>No rules yet: keys are copied unchanged.</p> : null}
        {rows.map((row, i) => (
          <div className={styles.ruleRow} key={i}>
            <FormField id={`rule-field-${i}`} label={`Rule ${i + 1} field`} error={problems[i]}>
              {(control) => <Input {...control} value={row.field} disabled={!canWrite} invalid={Boolean(problems[i])} onChange={(event) => edit(i, { field: event.target.value })} />}
            </FormField>
            <FormField id={`rule-from-${i}`} label={`Rule ${i + 1} replace prefix`}>
              {(control) => <Input {...control} value={row.from} disabled={!canWrite} onChange={(event) => edit(i, { from: event.target.value })} />}
            </FormField>
            <FormField id={`rule-to-${i}`} label={`Rule ${i + 1} with prefix`}>
              {(control) => <Input {...control} value={row.to} disabled={!canWrite} onChange={(event) => edit(i, { to: event.target.value })} />}
            </FormField>
            {canWrite ? (
              <div className={styles.ruleRowEnd}>
                <Button size="sm" onClick={() => { setRows((prev) => prev.filter((_, j) => j !== i)); setProblems([]); setSaved(false); setPreview(null); }}>
                  Remove rule {i + 1}
                </Button>
              </div>
            ) : null}
          </div>
        ))}
        {canWrite ? (
          <div className={styles.rowActions}>
            <Button onClick={() => { setRows((prev) => [...prev, { field: "@key", from: "", to: "" }]); setSaved(false); }}>Add rule</Button>
            <Button variant="primary" loading={busy} onClick={save}>
              Save rules
            </Button>
          </div>
        ) : null}
        {saved ? <Alert tone="success" title="Rules saved" live="status">The copy will use these rules from its next run.</Alert> : null}
        {failure ? (
          <Alert tone="danger" title="Could not save the rules" live="alert" action={failure.code === "MIG-VERSION-CONFLICT" ? <Button size="sm" onClick={() => { setFailure(null); rules.reload(); }}>Reload rules</Button> : undefined}>
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        <FormField id="rule-sample" label="Sample keys (one per line)" hint="Shows what the rules above, saved or not, would do to these keys.">
          {(control) => <Textarea {...control} mono rows={4} value={sample} onChange={(event) => setSample(event.target.value)} />}
        </FormField>
        <div>
          <Button onClick={runPreview}>Preview</Button>
        </div>
        {previewError ? (
          <Alert tone="danger" title="Could not preview" live="alert">
            {previewError.message} ({previewError.code})
          </Alert>
        ) : null}
        {preview ? <DataTable label="Rewrite preview" columns={previewColumns} rows={preview} getRowKey={(r, i) => `${i}:${r.key}`} empty={{ title: "Nothing to preview", description: "Enter sample keys." }} /> : null}
      </div>
    </Card>
  );
}
