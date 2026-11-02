import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  createBinding,
  describeProblem,
  getBinding,
  getConnection,
  listBindings,
  listConnections,
  listSpecsPage,
  newKey,
  previewBinding,
  previewDraftBinding,
  updateBinding,
  type BindingInput,
  type ConnectionKind,
  type ConnectionProfile,
  type CursorType,
  type EntityBinding,
  type Problem,
} from "./api/client";
import {
  CURSOR_LABELS,
  CURSOR_TYPES,
  describeSource,
  EMPTY_BINDING_FORM,
  parseBindingForm,
  SOURCE_FIELDS,
  sourceToText,
  type BindingForm,
} from "./bindingDomain";
import { PreviewPanel } from "./BindingPreview";
import { CONNECTION_KIND_LABELS, formatDateTime } from "./domain";
import { LoadMore } from "./LoadMore";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { usePaged } from "./usePaged";

export const DUTY_BINDING_WRITE = "migration.connection.write";

/** Service errors name source fields "source.path"; a bare "source" is shown on the first source field. */
function fieldErrors(problem: Problem): Record<string, string> {
  return { ...(problem.errors ?? {}) };
}

function SourceFields({ kind, values, errors, onChange }: { kind: ConnectionKind; values: Record<string, string>; errors: Record<string, string>; onChange(key: string, value: string): void }) {
  return (
    <>
      {SOURCE_FIELDS[kind].map((field, i) => {
        const error = errors[`source.${field.key}`] ?? (i === 0 ? errors.source : undefined);
        return (
          <FormField key={field.key} id={`bind-${field.key}`} label={field.label} optional={!field.required} hint={field.hint} error={error}>
            {(control) =>
              field.options ? (
                <Select {...control} value={values[field.key] ?? ""} onChange={(event) => onChange(field.key, event.target.value)}>
                  <option value="">Default</option>
                  {field.options.map((o) => (
                    <option key={o} value={o}>
                      {o}
                    </option>
                  ))}
                </Select>
              ) : field.multiline ? (
                <Textarea {...control} mono rows={4} value={values[field.key] ?? ""} invalid={Boolean(error)} onChange={(event) => onChange(field.key, event.target.value)} />
              ) : (
                <Input {...control} value={values[field.key] ?? ""} invalid={Boolean(error)} onChange={(event) => onChange(field.key, event.target.value)} />
              )
            }
          </FormField>
        );
      })}
    </>
  );
}

function KeyAndCursorFields({ form, errors, onChange }: { form: BindingForm; errors: Record<string, string>; onChange(next: BindingForm): void }) {
  return (
    <>
      <FormField id="bind-keys" label="Key fields" error={errors.keyFields} hint="The columns or JSON paths whose values, joined with |, identify a source row. Comma separated, one to five.">
        {(control) => <Input {...control} value={form.keyFields} invalid={Boolean(errors.keyFields)} onChange={(event) => onChange({ ...form, keyFields: event.target.value })} />}
      </FormField>
      <FormField id="bind-cursor" label="Change cursor field" optional error={errors.cursorField} hint="A column or JSON path that changes whenever the row does. Needed for delta sync later.">
        {(control) => <Input {...control} value={form.cursorField} invalid={Boolean(errors.cursorField)} onChange={(event) => onChange({ ...form, cursorField: event.target.value })} />}
      </FormField>
      <FormField id="bind-cursor-type" label="Cursor type" optional error={errors.cursorType}>
        {(control) => (
          <Select {...control} value={form.cursorType} onChange={(event) => onChange({ ...form, cursorType: event.target.value as CursorType | "" })}>
            <option value="">No cursor</option>
            {CURSOR_TYPES.map((t) => (
              <option key={t} value={t}>
                {CURSOR_LABELS[t]}
              </option>
            ))}
          </Select>
        )}
      </FormField>
    </>
  );
}

/** Entity bindings: which table, query, endpoint or file of a connection feeds which Passage entity. */
export function BindingsPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_BINDING_WRITE);
  const [profileId, setProfileId] = useState("");
  const [status, setStatus] = useState("");
  const key = `${profileId}|${status}`;
  const filter = { connectionProfileId: profileId || undefined, status: status || undefined };
  const state = useLoad((signal) => listBindings(filter, signal), key);
  const paged = usePaged(state, key, (cursor) => listBindings({ ...filter, cursor }));
  const profiles = useLoad((signal) => listConnections({ limit: 200 }, signal), "profiles");
  const names = new Map((profiles.data?.items ?? []).map((p) => [p.id, p.name]));
  const columns: DataTableColumn<EntityBinding>[] = [
    { key: "entity", header: "Entity" },
    { key: "connectionProfileId", header: "Connection", render: (row) => names.get(row.connectionProfileId) ?? row.connectionProfileId },
    { key: "source", header: "Source", mono: true, render: (row) => describeSource(kindOf(profiles.data?.items, row.connectionProfileId), row) },
    { key: "keyFields", header: "Key", mono: true, render: (row) => row.keyFields.join(", ") },
    { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
    { key: "updatedAt", header: "Updated", render: (row) => formatDateTime(row.updatedAt) },
  ];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Entity bindings"
        description="Which table, query, endpoint or file of a connection feeds which Passage entity. Preview a binding to see the mapped records and rule errors before any load."
        actions={
          canWrite ? (
            <Link className={buttonClassName("primary")} to="/migration/bindings/new">
              New binding
            </Link>
          ) : null
        }
      />
      <div className={styles.toolbar}>
        <FormField id="bindings-profile" label="Connection">
          {(control) => (
            <Select {...control} value={profileId} onChange={(event) => setProfileId(event.target.value)}>
              <option value="">All connections</option>
              {(profiles.data?.items ?? []).map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="bindings-status" label="Status">
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
        <Alert tone="danger" title="Could not load bindings" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Entity bindings"
          columns={columns}
          rows={paged.rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No entity bindings yet", description: "Create a connection first, then bind one of its tables, endpoints or files to an entity such as customer or item." }}
          onRowOpen={(row) => navigate(`/migration/bindings/${row.id}`)}
        />
      )}
      <LoadMore paged={paged} noun="bindings" />
    </div>
  );
}

function kindOf(profiles: ConnectionProfile[] | undefined, id: string): ConnectionKind {
  return profiles?.find((p) => p.id === id)?.kind ?? "sql";
}

/** Create a binding. The connection and entity are fixed afterwards. A draft can be previewed before it is saved. */
export function NewBindingPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_BINDING_WRITE);
  const profiles = useLoad((signal) => listConnections({ status: "active", limit: 200 }, signal), "active-profiles");
  const specs = useLoad((signal) => listSpecsPage({ limit: 200 }, signal), "specs");
  const [form, setForm] = useState<BindingForm>(EMPTY_BINDING_FORM);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [key, setKey] = useState(newKey());
  const profile = profiles.data?.items.find((p) => p.id === form.connectionProfileId) ?? null;
  const entitySpecs = (specs.data?.items ?? []).filter((s) => s.entity === form.entity.trim());

  function edit(next: BindingForm) {
    setForm(next);
    setKey(newKey());
  }

  function parse(): BindingInput | null {
    const parsed = parseBindingForm(form, profile?.kind ?? null);
    setErrors(parsed.errors);
    return parsed.value ?? null;
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const value = parse();
    if (!value) return;
    setBusy(true);
    try {
      const saved = await createBinding(value, key);
      navigate(`/migration/bindings/${saved.id}`);
    } catch (e) {
      const problem = describeProblem(e);
      setFailure(problem);
      setErrors((prev) => ({ ...prev, ...fieldErrors(problem) }));
      setKey(newKey());
    } finally {
      setBusy(false);
    }
  }

  async function draftPreview(options: Parameters<typeof previewDraftBinding>[1]) {
    const value = parse();
    if (!value) throw Object.assign(new Error("Fix the highlighted fields first."), { code: "UI-FORM" });
    return previewDraftBinding(value, options);
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New entity binding" description="Choose the connection and say where the rows are. Try it with Preview before you save." />
      {canWrite ? null : (
        <Alert tone="warning" title="You can view bindings but not create them">
          Creating a binding needs the migration.connection.write duty.
        </Alert>
      )}
      {profiles.error ? (
        <Alert tone="danger" title="Could not load connections" live="alert" action={<Button size="sm" onClick={profiles.reload}>Try again</Button>}>
          {profiles.error.message} ({profiles.error.code})
        </Alert>
      ) : null}
      {profiles.data && profiles.data.items.length === 0 ? (
        <Alert tone="info" title="No active connection yet">
          A binding reads through a connection. <Link to="/migration/connections/new">Create a connection</Link> first.
        </Alert>
      ) : null}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="bind-profile" label="Connection" error={errors.connectionProfileId}>
            {(control) => (
              <Select {...control} value={form.connectionProfileId} onChange={(event) => edit({ ...form, connectionProfileId: event.target.value, source: {} })}>
                <option value="">Choose</option>
                {(profiles.data?.items ?? []).map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name} ({CONNECTION_KIND_LABELS[p.kind] ?? p.kind})
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="bind-entity" label="Passage entity" error={errors.entity} hint="For example customer, vendor, item. Mapping specs are published per entity.">
            {(control) => <Input {...control} value={form.entity} invalid={Boolean(errors.entity)} onChange={(event) => edit({ ...form, entity: event.target.value })} />}
          </FormField>
          {profile ? <SourceFields kind={profile.kind} values={form.source} errors={errors} onChange={(k, v) => edit({ ...form, source: { ...form.source, [k]: v } })} /> : null}
          <KeyAndCursorFields form={form} errors={errors} onChange={edit} />
          <FormField id="bind-spec" label="Default mapping spec" optional hint="Used by previews. Only specs for this entity are listed.">
            {(control) => (
              <Select {...control} value={form.mappingSpecId} onChange={(event) => edit({ ...form, mappingSpecId: event.target.value })}>
                <option value="">None</option>
                {entitySpecs.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.entity} v{s.version}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the binding" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy} disabled={!canWrite}>
              Create binding
            </Button>
            <Link className={buttonClassName("ghost")} to="/migration/bindings">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      {canWrite ? (
        <PreviewPanel
          isFile={profile?.kind === "file"}
          specs={entitySpecs}
          defaultSpecId={form.mappingSpecId || undefined}
          disabledReason={profile ? undefined : "Choose a connection to preview."}
          run={(options) => draftPreview({ ...options, mappingSpecId: options.mappingSpecId ?? (form.mappingSpecId || undefined) })}
        />
      ) : null}
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

/** One binding: its source (editable), enable or disable, and the preview. */
export function BindingDetailPage() {
  const { id = "" } = useParams();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_BINDING_WRITE);
  const state = useLoad((signal) => getBinding(id, signal), id);
  const binding = state.data?.id === id ? state.data : null;
  const profile = useLoad((signal) => (binding ? getConnection(binding.connectionProfileId, signal) : Promise.resolve(null)), binding?.connectionProfileId ?? "");
  const specs = useLoad((signal) => (binding ? listSpecsPage({ entity: binding.entity, limit: 200 }, signal) : Promise.resolve(null)), binding?.entity ?? "");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Entity binding" />
        <Alert tone="danger" title="Could not load this binding" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link className={buttonClassName("ghost")} to="/migration/bindings">
          Back to bindings
        </Link>
      </div>
    );
  }
  if (!binding) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Entity binding" description="Loading..." />
      </div>
    );
  }
  const kind = profile.data?.kind ?? null;

  async function toggle(current: EntityBinding) {
    setBusy(true);
    setFailure(null);
    try {
      state.set(await updateBinding(current.id, current.version, { status: current.status === "active" ? "disabled" : "active" }));
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${binding.entity} from ${profile.data?.name ?? "connection"}`}
        description={`Created ${formatDateTime(binding.createdAt)}.`}
        actions={
          <>
            <StatusPill status={binding.status} />
            {canWrite ? (
              <Button loading={busy} onClick={() => toggle(binding)}>
                {binding.status === "active" ? "Disable" : "Enable"}
              </Button>
            ) : null}
          </>
        }
      />
      {failure ? (
        <Alert tone="danger" title="Could not change the binding" live="alert" action={<Button size="sm" onClick={state.reload}>Reload</Button>}>
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {profile.error ? (
        <Alert tone="danger" title="Could not load the connection" live="alert" action={<Button size="sm" onClick={profile.reload}>Try again</Button>}>
          {profile.error.message} ({profile.error.code})
        </Alert>
      ) : null}
      <SourceCard binding={binding} kind={kind} canEdit={canWrite && kind !== null} onSaved={state.set} />
      {canWrite && binding.status === "active" ? (
        <PreviewPanel
          isFile={kind === "file"}
          specs={specs.data?.items ?? []}
          defaultSpecId={binding.mappingSpecId}
          run={(options) => previewBinding(binding.id, options)}
        />
      ) : null}
      <Link className={buttonClassName("ghost")} to="/migration/bindings">
        Back to bindings
      </Link>
    </div>
  );
}

function SourceCard({ binding, kind, canEdit, onSaved }: { binding: EntityBinding; kind: ConnectionKind | null; canEdit: boolean; onSaved: (saved: EntityBinding) => void }) {
  const [form, setForm] = useState<BindingForm | null>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  function open() {
    if (kind === null) return;
    setForm({
      connectionProfileId: binding.connectionProfileId,
      entity: binding.entity,
      keyFields: binding.keyFields.join(", "),
      cursorField: binding.cursorField ?? "",
      cursorType: binding.cursorType ?? "",
      mappingSpecId: binding.mappingSpecId ?? "",
      source: sourceToText(kind, binding.source),
    });
    setErrors({});
    setFailure(null);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!form || kind === null) return;
    setFailure(null);
    const parsed = parseBindingForm(form, kind);
    setErrors(parsed.errors);
    if (!parsed.value) return;
    setBusy(true);
    try {
      const v = parsed.value;
      onSaved(
        await updateBinding(binding.id, binding.version, {
          source: v.source,
          keyFields: v.keyFields,
          cursorField: v.cursorField ?? "",
          cursorType: v.cursorType ?? "",
          mappingSpecId: v.mappingSpecId ?? "",
        }),
      );
      setForm(null);
    } catch (e) {
      const problem = describeProblem(e);
      setFailure(problem);
      setErrors((prev) => ({ ...prev, ...fieldErrors(problem) }));
    } finally {
      setBusy(false);
    }
  }

  if (form && canEdit && kind !== null) {
    return (
      <Card title="Source">
        <form className={styles.form} onSubmit={submit} noValidate>
          <SourceFields kind={kind} values={form.source} errors={errors} onChange={(k, v) => setForm({ ...form, source: { ...form.source, [k]: v } })} />
          <KeyAndCursorFields form={form} errors={errors} onChange={setForm} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the binding" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save source
            </Button>
            <Button onClick={() => setForm(null)}>Cancel</Button>
          </div>
        </form>
      </Card>
    );
  }
  return (
    <Card title="Source" actions={canEdit ? <Button size="sm" onClick={open}>Edit source</Button> : undefined}>
      <dl className={styles.details}>
        <Entry term="Entity">{binding.entity}</Entry>
        <Entry term="Source">
          <span className={styles.mono}>{kind ? describeSource(kind, binding) : ""}</span>
        </Entry>
        {binding.source.query ? (
          <Entry term="Query">
            <span className={styles.mono}>{String(binding.source.query)}</span>
          </Entry>
        ) : null}
        <Entry term="Key fields">
          <span className={styles.mono}>{binding.keyFields.join(", ")}</span>
        </Entry>
        <Entry term="Change cursor">{binding.cursorField ? `${binding.cursorField} (${CURSOR_LABELS[binding.cursorType as CursorType] ?? binding.cursorType})` : "None"}</Entry>
        <Entry term="Version">{binding.version}</Entry>
      </dl>
    </Card>
  );
}
