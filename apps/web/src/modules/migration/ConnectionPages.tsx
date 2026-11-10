import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  createConnection,
  describeProblem,
  getConnection,
  listConnections,
  newKey,
  setConnectionSecret,
  testConnection,
  updateConnection,
  type ConnectionKind,
  type ConnectionProfile,
  type ConnectionTestResult,
  type Problem,
} from "./api/client";
import {
  checkSecret,
  CONNECTION_FIELDS,
  CONNECTION_KIND_LABELS,
  CONNECTION_KINDS,
  configToText,
  EMPTY_CONNECTION_FORM,
  formatDateTime,
  parseConnectionConfig,
  parseConnectionForm,
  secretNoun,
  sourceLabel,
  type ConnectionForm,
} from "./domain";
import { LoadMore } from "./LoadMore";
import styles from "./pages.module.css";
import { SourceSystemField } from "./SourceSystemField";
import { useLoad } from "./useLoad";
import { usePaged } from "./usePaged";
import { ProblemMessage } from "../../shell/errors";

export const DUTY_CONNECTION_WRITE = "migration.connection.write";

const columns: DataTableColumn<ConnectionProfile>[] = [
  { key: "name", header: "Name" },
  { key: "kind", header: "Kind", render: (row) => CONNECTION_KIND_LABELS[row.kind] ?? row.kind },
  { key: "sourceSystem", header: "Source system", render: (row) => sourceLabel(row.sourceSystem) },
  { key: "secretSet", header: "Secret", render: (row) => (row.secretSet ? "Set" : "Not set") },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "updatedAt", header: "Updated", render: (row) => formatDateTime(row.updatedAt) },
];

/** Connection profiles: how this tenant reaches a legacy or custom ERP. The secret of a connection is never shown. */
export function ConnectionsPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_CONNECTION_WRITE);
  const [kind, setKind] = useState("");
  const [status, setStatus] = useState("");
  const key = `${kind}|${status}`;
  const state = useLoad((signal) => listConnections({ kind: kind || undefined, status: status || undefined }, signal), key);
  const paged = usePaged(state, key, (cursor) => listConnections({ kind: kind || undefined, status: status || undefined, cursor }));
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Connections"
        description="How Passage reaches your existing ERP: a SQL database, a REST API or a drop directory of files. Settings are stored here; the password, token or key is sealed and can be set but never read back."
        actions={
          canWrite ? (
            <Link className={buttonClassName("primary")} to="/migration/connections/new">
              New connection
            </Link>
          ) : null
        }
      />
      <div className={styles.toolbar}>
        <FormField id="connections-kind" label="Kind">
          {(control) => (
            <Select {...control} value={kind} onChange={(event) => setKind(event.target.value)}>
              <option value="">All kinds</option>
              {CONNECTION_KINDS.map((k) => (
                <option key={k} value={k}>
                  {CONNECTION_KIND_LABELS[k]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="connections-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value)}>
              <option value="">All statuses</option>
              <option value="active">Active</option>
              <option value="disabled">Disabled</option>
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load connections" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Connections"
          columns={columns}
          rows={paged.rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No connections yet", description: "Create a connection to your ERP, set its password, then test it before a migration depends on it." }}
          onRowOpen={(row) => navigate(`/migration/connections/${row.id}`)}
        />
      )}
      <LoadMore paged={paged} noun="connections" />
    </div>
  );
}

/** The settings a stored secret is bound to (the server clears the secret when one changes). */
const SECRET_BOUND_LABELS: Partial<Record<ConnectionProfile["kind"], string>> = {
  sql: "driver, host, port, database, user or SSL mode",
  rest: "base URL, authentication, user name or header name",
};

/** The setting fields of one connection kind; the secret is never among them. */
function SettingsFields({ kind, values, errors, onChange }: { kind: ConnectionKind; values: Record<string, string>; errors: Record<string, string>; onChange(key: string, value: string): void }) {
  return (
    <>
      {CONNECTION_FIELDS[kind].map((field) => (
        <FormField key={field.key} id={`conn-${field.key}`} label={field.label} optional={!field.required} hint={field.hint} error={errors[field.key]}>
          {(control) =>
            field.options ? (
              <Select {...control} value={values[field.key] ?? ""} onChange={(event) => onChange(field.key, event.target.value)}>
                <option value="">{field.required ? "Choose" : "Default"}</option>
                {field.options.map((o) => (
                  <option key={o} value={o}>
                    {o}
                  </option>
                ))}
              </Select>
            ) : (
              <Input {...control} value={values[field.key] ?? ""} invalid={Boolean(errors[field.key])} inputMode={field.number ? "numeric" : undefined} onChange={(event) => onChange(field.key, event.target.value)} />
            )
          }
        </FormField>
      ))}
    </>
  );
}

/** Service field errors arrive as "config.host"; the form keys them by setting name. */
function configErrors(problem: Problem): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(problem.errors ?? {})) out[k.startsWith("config.") ? k.slice("config.".length) : k] = v;
  return out;
}

/** Create a connection. Kind and source system are fixed afterwards; the secret is set on the next page. */
export function NewConnectionPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_CONNECTION_WRITE);
  const [form, setForm] = useState<ConnectionForm>(EMPTY_CONNECTION_FORM);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [key, setKey] = useState(newKey());

  function edit(next: ConnectionForm) {
    setForm(next);
    setKey(newKey());
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const parsed = parseConnectionForm(form);
    const found = Object.fromEntries(Object.entries(parsed.errors).map(([k, v]) => [k.startsWith("config.") ? k.slice("config.".length) : k, v]));
    setErrors(found);
    if (!parsed.value) return;
    setBusy(true);
    try {
      const saved = await createConnection(parsed.value, key);
      navigate(`/migration/connections/${saved.id}`);
    } catch (e) {
      const problem = describeProblem(e);
      setFailure(problem);
      setErrors((prev) => ({ ...prev, ...configErrors(problem) }));
      setKey(newKey());
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New connection" description="Settings only. After you create it you set the password, token or key, which is sealed and never shown again." />
      {canWrite ? null : (
        <Alert tone="warning" title="You can view connections but not create them">
          Creating a connection needs the migration.connection.write duty.
        </Alert>
      )}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="conn-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} value={form.name} invalid={Boolean(errors.name)} onChange={(event) => edit({ ...form, name: event.target.value })} />}
          </FormField>
          <FormField id="conn-kind" label="Kind">
            {(control) => (
              <Select {...control} value={form.kind} onChange={(event) => edit({ ...form, kind: event.target.value as ConnectionKind, config: {} })}>
                {CONNECTION_KINDS.map((k) => (
                  <option key={k} value={k}>
                    {CONNECTION_KIND_LABELS[k]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <SourceSystemField id="conn-source" value={form.sourceSystem} error={errors.sourceSystem} onChange={(value) => edit({ ...form, sourceSystem: value })} />
          <SettingsFields kind={form.kind} values={form.config} errors={errors} onChange={(k, v) => edit({ ...form, config: { ...form.config, [k]: v } })} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the connection" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy} disabled={!canWrite}>
              Create connection
            </Button>
            <Link className={buttonClassName("ghost")} to="/migration/connections">
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

/** One connection: its settings (editable), its write-only secret, and the connection test. */
export function ConnectionDetailPage() {
  const { id = "" } = useParams();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_CONNECTION_WRITE);
  const state = useLoad((signal) => getConnection(id, signal), id);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [testing, setTesting] = useState(false);
  const [result, setResult] = useState<ConnectionTestResult | null>(null);
  const [testFailure, setTestFailure] = useState<Problem | null>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Connection" />
        <Alert tone="danger" title="Could not load this connection" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
        <Link className={buttonClassName("ghost")} to="/migration/connections">
          Back to connections
        </Link>
      </div>
    );
  }
  const profile = state.data?.id === id ? state.data : null;
  if (!profile) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Connection" description="Loading..." />
      </div>
    );
  }

  async function toggle(current: ConnectionProfile) {
    setBusy(true);
    setFailure(null);
    try {
      state.set(await updateConnection(current.id, current.version, { status: current.status === "active" ? "disabled" : "active" }));
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  async function test(current: ConnectionProfile) {
    setTesting(true);
    setResult(null);
    setTestFailure(null);
    try {
      setResult(await testConnection(current.id));
    } catch (e) {
      setTestFailure(describeProblem(e));
    } finally {
      setTesting(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={profile.name}
        description={`${CONNECTION_KIND_LABELS[profile.kind] ?? profile.kind} for ${sourceLabel(profile.sourceSystem)}. Created ${formatDateTime(profile.createdAt)}.`}
        actions={
          <>
            <StatusPill status={profile.status} />
            {canWrite ? (
              <>
                <Button variant="primary" loading={testing} onClick={() => test(profile)}>
                  Test connection
                </Button>
                <Button loading={busy} onClick={() => toggle(profile)}>
                  {profile.status === "active" ? "Disable" : "Enable"}
                </Button>
              </>
            ) : null}
          </>
        }
      />
      {failure ? (
        <Alert tone="danger" title="Could not change the connection" live="alert" action={<Button size="sm" onClick={state.reload}>Reload</Button>}>
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <TestResultCard result={result} failure={testFailure} testing={testing} />
      <SettingsCard profile={profile} canEdit={canWrite} onSaved={state.set} />
      <SecretCard profile={profile} canEdit={canWrite} onSaved={state.set} />
      <Link className={buttonClassName("ghost")} to="/migration/connections">
        Back to connections
      </Link>
    </div>
  );
}

function SettingsCard({ profile, canEdit, onSaved }: { profile: ConnectionProfile; canEdit: boolean; onSaved: (saved: ConnectionProfile) => void }) {
  const [form, setForm] = useState<{ name: string; config: Record<string, string> } | null>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [cleared, setCleared] = useState(false);

  function open() {
    setForm({ name: profile.name, config: configToText(profile.config) });
    setErrors({});
    setFailure(null);
    setCleared(false);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!form) return;
    setFailure(null);
    const name = form.name.trim();
    const parsed = parseConnectionConfig(profile.kind, form.config);
    const found = { ...parsed.errors, ...(name === "" ? { name: "Enter a connection name." } : {}) };
    setErrors(found);
    if (!parsed.config || name === "") return;
    setBusy(true);
    try {
      const saved = await updateConnection(profile.id, profile.version, { name, config: parsed.config });
      // Repointing the connection clears its secret on the server; say so instead of letting the next test fail.
      setCleared(profile.secretSet && !saved.secretSet);
      onSaved(saved);
      setForm(null);
    } catch (e) {
      const problem = describeProblem(e);
      setFailure(problem);
      setErrors((prev) => ({ ...prev, ...configErrors(problem) }));
    } finally {
      setBusy(false);
    }
  }

  if (form && canEdit) {
    return (
      <Card title="Settings">
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="edit-conn-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} value={form.name} invalid={Boolean(errors.name)} onChange={(event) => setForm({ ...form, name: event.target.value })} />}
          </FormField>
          <SettingsFields kind={profile.kind} values={form.config} errors={errors} onChange={(k, v) => setForm({ ...form, config: { ...form.config, [k]: v } })} />
          {profile.secretSet && SECRET_BOUND_LABELS[profile.kind] ? (
            <p className={styles.note}>Changing the {SECRET_BOUND_LABELS[profile.kind]} clears the stored secret; set it again after saving.</p>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the settings" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save settings
            </Button>
            <Button onClick={() => setForm(null)}>Cancel</Button>
          </div>
        </form>
      </Card>
    );
  }
  const fields = CONNECTION_FIELDS[profile.kind].filter((f) => profile.config[f.key] !== undefined);
  return (
    <Card title="Settings" actions={canEdit ? <Button size="sm" onClick={open}>Edit settings</Button> : undefined}>
      {cleared ? (
        <Alert tone="warning" title="The secret was cleared" live="status">
          The connection now points somewhere else, so its stored secret was removed. Set it again before testing.
        </Alert>
      ) : null}
      <dl className={styles.details}>
        <Entry term="Source system">{sourceLabel(profile.sourceSystem)}</Entry>
        {fields.map((f) => (
          <Entry key={f.key} term={f.label}>
            <span className={styles.mono}>{String(profile.config[f.key])}</span>
          </Entry>
        ))}
        <Entry term="Version">{profile.version}</Entry>
      </dl>
    </Card>
  );
}

/** The write-only secret: it says whether one is set and when, and takes a new value; it never shows the old one. */
function SecretCard({ profile, canEdit, onSaved }: { profile: ConnectionProfile; canEdit: boolean; onSaved: (saved: ConnectionProfile) => void }) {
  const noun = secretNoun(profile.kind, profile.config);
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  if (noun === null) {
    return (
      <Card title="Secret">
        <p className={styles.note}>{profile.kind === "file" ? "A drop directory needs no secret." : "This connection uses no authentication, so it needs no secret."}</p>
      </Card>
    );
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setSaved(false);
    const problem = checkSecret(value);
    setError(problem);
    if (problem) return;
    setBusy(true);
    try {
      onSaved(await setConnectionSecret(profile.id, value));
      setSaved(true);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      // The typed secret never stays in the page, whether or not it was accepted.
      setValue("");
      setBusy(false);
    }
  }

  return (
    <Card title="Secret">
      <p className={styles.note} role="status">
        {profile.secretSet ? `A ${noun} is set (updated ${profile.secretUpdatedAt ? formatDateTime(profile.secretUpdatedAt) : "earlier"}). It is sealed and cannot be shown.` : `No ${noun} is set yet.`}
      </p>
      {canEdit ? (
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="conn-secret" label={profile.secretSet ? `New ${noun}` : `The ${noun}`} error={error} hint="Write-only. Once saved it cannot be read back; set it again to change it.">
            {(control) => <Input {...control} type="password" autoComplete="new-password" value={value} invalid={Boolean(error)} onChange={(event) => setValue(event.target.value)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={`Could not set the ${noun}`} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          {saved ? (
            <div className={styles.formActions}>
              <Alert tone="success" title={`The ${noun} was saved`} live="status">
                Run the connection test to check it.
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              {profile.secretSet ? `Replace ${noun}` : `Set ${noun}`}
            </Button>
          </div>
        </form>
      ) : null}
    </Card>
  );
}

type TableRow = NonNullable<ConnectionTestResult["tables"]>[number];

const tableColumns: DataTableColumn<TableRow>[] = [
  { key: "schema", header: "Schema", mono: true },
  { key: "name", header: "Table", mono: true },
  { key: "type", header: "Type" },
  { key: "columns", header: "Columns", render: (row) => (row.columns && row.columns.length > 0 ? row.columns.map((c) => `${c.name} ${c.type}`).join(", ") : "") },
];

const fileColumns: DataTableColumn<{ name: string; bytes: number }>[] = [
  { key: "name", header: "File", mono: true },
  { key: "bytes", header: "Bytes", align: "end" },
];

/** What the last connection test found. It has no secret and no row data by construction. */
function TestResultCard({ result, failure, testing }: { result: ConnectionTestResult | null; failure: Problem | null; testing: boolean }) {
  if (testing) {
    return (
      <Card title="Connection test">
        <p className={styles.note} role="status" aria-busy="true">
          Testing the connection...
        </p>
      </Card>
    );
  }
  if (failure) {
    return (
      <Card title="Connection test">
        <Alert tone="danger" title="The test could not run" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      </Card>
    );
  }
  if (!result) return null;
  return (
    <Card title="Connection test">
      {result.ok ? (
        <Alert tone="success" title={`Connected in ${result.durationMs} ms`} live="status">
          {result.serverVersion ?? (result.httpStatus ? `The endpoint answered ${result.httpStatus}.` : result.fileCount !== undefined ? `${result.fileCount} matching files found.` : "The connection works.")}
        </Alert>
      ) : (
        <Alert tone="danger" title="The connection failed" live="alert">
          {result.failure?.message ?? "Unknown failure"} ({result.failure?.code ?? "MIG-PROBE-FAILED"})
        </Alert>
      )}
      {result.ok && result.schemas ? (
        <p className={styles.note}>
          {result.schemas.length} schemas: {result.schemas.join(", ")}
        </p>
      ) : null}
      {result.ok && result.tables ? (
        <DataTable label="Tables found" columns={tableColumns} rows={result.tables} getRowKey={(row) => `${row.schema}.${row.name}`} empty={{ title: "No tables visible to this user" }} />
      ) : null}
      {result.ok && result.files ? (
        <DataTable label="Files found" columns={fileColumns} rows={result.files} getRowKey={(row) => row.name} empty={{ title: "No matching files in the drop directory" }} />
      ) : null}
      {result.truncated ? <p className={styles.note}>The lists are cut at their limits.</p> : null}
    </Card>
  );
}
