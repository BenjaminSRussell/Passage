import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, PageHeader, Select, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getSpec, listProjects, listSpecsPage, newKey, PICKER_LIMIT, publishSpec, type MappingSpec, type MigrationProject, type Problem } from "./api/client";
import { formatDateTime, parseMappingText } from "./domain";
import styles from "./pages.module.css";
import { DUTY_RUN } from "./RunPages";
import { LoadMore } from "./LoadMore";
import { useLoad } from "./useLoad";
import { usePaged } from "./usePaged";
import { ProblemMessage } from "../../shell/errors";

export const DUTY_SPEC_WRITE = "migration.spec.write";

/** Project names by id, for the list and filter; empty until the projects load (the list still works without them). */
function useProjectNames(): { names: Map<string, string>; projects: MigrationProject[] } {
  const state = useLoad((signal) => listProjects({ projectType: "migrate", limit: PICKER_LIMIT }, signal), "spec-projects");
  const projects = state.data?.items ?? [];
  return { names: new Map(projects.map((p) => [p.id, p.name])), projects };
}

const columns = (names: Map<string, string>): DataTableColumn<MappingSpec>[] => [
  { key: "entity", header: "Entity", mono: true },
  { key: "projectId", header: "Project", render: (row) => (row.projectId ? (names.get(row.projectId) ?? row.projectId.slice(0, 8)) : "All projects") },
  { key: "naturalKey", header: "Natural key", mono: true, render: (row) => (row.naturalKey?.length ? row.naturalKey.join(", ") : "Not set") },
  { key: "version", header: "Version", align: "end" },
  { key: "fieldCount", header: "Fields", align: "end" },
  { key: "testCount", header: "Tests", align: "end" },
  { key: "checksum", header: "Checksum", mono: true, render: (row) => row.checksum.slice(0, 12) },
  { key: "createdAt", header: "Published", render: (row) => formatDateTime(row.createdAt) },
];

interface RejectCondition {
  field: string;
  op: string;
  value?: string;
  values?: string[];
  code: string;
}

const opText: Record<string, string> = { empty: "is empty", eq: "equals", ne: "does not equal", in: "is one of", notIn: "is not one of", match: "matches", expr: "is true:" };

/** The mapping's rejectWhen conditions; the service validated them when the spec was published. */
function rejectConditions(mapping: unknown): RejectCondition[] {
  const list = (mapping as { rejectWhen?: unknown } | null | undefined)?.rejectWhen;
  return Array.isArray(list) ? (list as RejectCondition[]) : [];
}

interface FieldRuleRow {
  target: string;
  type: string;
  rule?: string;
  source?: string;
  sources?: string[];
  value?: string;
  expr?: string;
  fn?: string;
}

/** The mapping's field rules in order, for the Field rules card. */
function fieldRules(mapping: unknown): FieldRuleRow[] {
  const list = (mapping as { fields?: unknown } | null | undefined)?.fields;
  return Array.isArray(list) ? (list as FieldRuleRow[]) : [];
}

/** What a field reads: a column, a joined list of columns, a constant or an expression. */
function ruleInput(f: FieldRuleRow): string {
  if (f.rule === "expr") return f.expr ?? "";
  if (f.rule === "concat") return (f.sources ?? []).join(" + ");
  if (f.rule === "const") return JSON.stringify(f.value ?? "");
  return f.source ?? "";
}

const fieldRuleColumns: DataTableColumn<FieldRuleRow>[] = [
  { key: "target", header: "Target", mono: true },
  { key: "type", header: "Type" },
  { key: "rule", header: "Rule", render: (f) => (f.fn ? `${f.rule || "copy"} (${f.fn})` : f.rule || "copy") },
  { key: "input", header: "Reads", mono: true, render: ruleInput },
];

const rejectColumns: DataTableColumn<RejectCondition>[] = [
  { key: "code", header: "Reason code", mono: true },
  { key: "field", header: "Target", mono: true },
  {
    key: "op",
    header: "Rejects when",
    render: (c) => [opText[c.op] ?? c.op, c.values ? c.values.join(", ") : c.value].filter((part) => part !== undefined && part !== "").join(" "),
  },
];

/** Published mapping versions; a version never changes once published. */
export function SpecsPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_SPEC_WRITE);
  const [project, setProject] = useState("");
  const { names, projects } = useProjectNames();
  const filter = { projectId: project || undefined };
  const key = `specs|${project}`;
  const state = useLoad((signal) => listSpecsPage(filter, signal), key);
  const paged = usePaged(state, key, (cursor) => listSpecsPage({ ...filter, cursor }));
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Mapping specs"
        description="Versioned field mappings with their tests. Runs reference a spec so every load can be traced to the mapping that produced it."
        actions={
          <>
            <Link className={buttonClassName("ghost")} to="/migration">
              Runs
            </Link>
            {canWrite ? (
              <Link className={buttonClassName("primary")} to="/migration/specs/new">
                Publish spec
              </Link>
            ) : null}
          </>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="specs-project" label="Project">
          {(control) => (
            <Select {...control} value={project} onChange={(event) => setProject(event.target.value)}>
              <option value="">All specs</option>
              <option value="none">Tenant-wide only</option>
              {projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load mapping specs" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Mapping specs"
          columns={columns(names)}
          rows={paged.rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No mapping specs yet", description: "Publish a mapping to start a migration run." }}
          onRowOpen={(row) => navigate(`/migration/specs/${row.id}`)}
        />
      )}
      <LoadMore paged={paged} noun="mapping specs" />
    </div>
  );
}

/** Paste a mapping document; the service parses it, runs its tests and stores an immutable version. */
export function NewSpecPage() {
  const navigate = useNavigate();
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [project, setProject] = useState("");
  const { projects } = useProjectNames();
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const parsed = parseMappingText(text);
    setError(parsed.error ?? "");
    if (parsed.mapping === undefined) return;
    setBusy(true);
    try {
      const spec = await publishSpec(parsed.mapping, key.current, project || undefined);
      navigate(`/migration/specs/${spec.id}`);
    } catch (e) {
      setFailure(describeProblem(e));
      key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Publish mapping spec" description="Needs entity, version, fields and at least one test case. Failing mapping tests stop the publish. Publishing the same content again returns the stored version." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="spec-project" label="Project (optional)" hint="Leave empty for a spec any project may use. A project's spec is versioned on its own.">
            {(control) => (
              <Select
                {...control}
                value={project}
                onChange={(event) => {
                  setProject(event.target.value);
                  key.current = newKey();
                }}
              >
                <option value="">No project (tenant-wide)</option>
                {projects
                  .filter((p) => p.status === "active")
                  .map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
              </Select>
            )}
          </FormField>
          <div className={styles.wide}>
            <FormField id="spec-mapping" label="Mapping JSON" error={error}>
              {(control) => (
                <Textarea
                  {...control}
                  mono
                  rows={16}
                  value={text}
                  invalid={error !== ""}
                  onChange={(event) => {
                    setText(event.target.value);
                    key.current = newKey();
                  }}
                />
              )}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not publish the spec" live="alert">
                <ProblemMessage problem={failure} />
                {failure.errors ? (
                  <ul>
                    {Object.entries(failure.errors).map(([name, reason]) => (
                      <li key={name}>
                        {name}: {reason}
                      </li>
                    ))}
                  </ul>
                ) : null}
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Publish spec
            </Button>
            <Link className={buttonClassName("ghost")} to="/migration/specs">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

/** One published version with its mapping document. */
export function SpecDetailPage() {
  const { id = "" } = useParams();
  const canRun = holdsDuty(usePrincipalDuties(), DUTY_RUN);
  const state = useLoad((signal) => getSpec(id, signal), id);
  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Mapping spec" />
        <Alert tone="danger" title="Could not load this spec" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
        <Link className={buttonClassName("ghost")} to="/migration/specs">
          Back to specs
        </Link>
      </div>
    );
  }
  const spec = state.data?.id === id ? state.data : null;
  if (!spec) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Mapping spec" description="Loading..." />
      </div>
    );
  }
  const conditions = rejectConditions(spec.mapping);
  const rules = fieldRules(spec.mapping);
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${spec.entity} v${spec.version}`}
        description={`Published ${formatDateTime(spec.createdAt)}. ${spec.fieldCount} fields, ${spec.testCount} tests.`}
        actions={
          canRun ? (
            <Link className={buttonClassName("primary")} to={`/migration/runs/new?spec=${encodeURIComponent(spec.id)}`}>
              Start run with this spec
            </Link>
          ) : null
        }
      />
      <Card title="Scope">
        <dl className={styles.details}>
          <div className={styles.entry}>
            <dt className={styles.term}>Project</dt>
            <dd className={styles.value}>{spec.projectId ? <Link to={`/migration/projects/${spec.projectId}`}>View the project</Link> : "All projects (tenant-wide)"}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Natural key</dt>
            <dd className={styles.value}>{spec.naturalKey?.length ? <span className={styles.mono}>{spec.naturalKey.join(", ")}</span> : "Not set"}</dd>
          </div>
        </dl>
      </Card>
      <Card title="Checksum">
        <code>{spec.checksum}</code>
      </Card>
      {rules.length > 0 ? (
        <Card title="Field rules">
          <DataTable label="Field rules" columns={fieldRuleColumns} rows={rules} getRowKey={(f) => f.target} />
        </Card>
      ) : null}
      {conditions.length > 0 ? (
        <Card title="Reject conditions">
          <DataTable label="Reject conditions" columns={rejectColumns} rows={conditions} getRowKey={(c) => `${c.code}-${c.field}-${c.op}-${c.value ?? (c.values ?? []).join("|")}`} />
        </Card>
      ) : null}
      <Card title="Mapping">
        <pre className={styles.code}>{JSON.stringify(spec.mapping, null, 2)}</pre>
      </Card>
      <Link className={buttonClassName("ghost")} to="/migration/specs">
        Back to specs
      </Link>
    </div>
  );
}
