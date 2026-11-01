import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, PageHeader, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getSpec, listSpecsPage, newKey, publishSpec, type MappingSpec, type Problem } from "./api/client";
import { formatDateTime, parseMappingText } from "./domain";
import styles from "./pages.module.css";
import { DUTY_RUN } from "./RunPages";
import { LoadMore } from "./LoadMore";
import { useLoad } from "./useLoad";
import { usePaged } from "./usePaged";

export const DUTY_SPEC_WRITE = "migration.spec.write";

const columns: DataTableColumn<MappingSpec>[] = [
  { key: "entity", header: "Entity", mono: true },
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

const opText: Record<string, string> = { empty: "is empty", eq: "equals", ne: "does not equal", in: "is one of", notIn: "is not one of", match: "matches" };

/** The mapping's rejectWhen conditions; the service validated them when the spec was published. */
function rejectConditions(mapping: unknown): RejectCondition[] {
  const list = (mapping as { rejectWhen?: unknown } | null | undefined)?.rejectWhen;
  return Array.isArray(list) ? (list as RejectCondition[]) : [];
}

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
  const state = useLoad((signal) => listSpecsPage({}, signal), "specs");
  const paged = usePaged(state, "specs", (cursor) => listSpecsPage({ cursor }));
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
      {state.error ? (
        <Alert tone="danger" title="Could not load mapping specs" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Mapping specs"
          columns={columns}
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
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const parsed = parseMappingText(text);
    setError(parsed.error ?? "");
    if (parsed.mapping === undefined) return;
    setBusy(true);
    try {
      const spec = await publishSpec(parsed.mapping, key.current);
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
                {failure.message} ({failure.code})
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
          {state.error.message} ({state.error.code})
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
      <Card title="Checksum">
        <code>{spec.checksum}</code>
      </Card>
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
