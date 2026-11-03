import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  createOutboundMapping,
  describeProblem,
  getOutboundMapping,
  listOutboundMappings,
  listOutboundVersions,
  newKey,
  previewOutboundDraft,
  previewOutboundMapping,
  updateOutboundMapping,
  type OutboundMapping,
  type OutboundVersion,
  type Problem,
  type SourceSystem,
} from "./api/client";
import { formatDateTime, sourceLabel } from "./domain";
import { LoadMore } from "./LoadMore";
import { EMPTY_OUTBOUND_FORM, parseDefinitionText, parseOutboundForm, toJsonText, type OutboundForm } from "./outboundDomain";
import { OutboundPreviewPanel } from "./OutboundPreview";
import styles from "./pages.module.css";
import { SourceSystemField } from "./SourceSystemField";
import { useLoad } from "./useLoad";
import { usePaged } from "./usePaged";

/** Enforced by the service; not in policies/authz/data.json for the nav, so it gates the buttons only. */
export const DUTY_OUTBOUND_WRITE = "migration.project.write";

function Entry({ term, children }: { term: string; children: React.ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}

/** Outbound mappings: how a Passage event becomes the request the integration hub pushes to the legacy ERP. */
export function OutboundListPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_OUTBOUND_WRITE);
  const [status, setStatus] = useState("");
  const filter = { status: status || undefined };
  const state = useLoad((signal) => listOutboundMappings(filter, signal), status);
  const paged = usePaged(state, status, (cursor) => listOutboundMappings({ ...filter, cursor }));
  const columns: DataTableColumn<OutboundMapping>[] = [
    { key: "name", header: "Name" },
    { key: "eventType", header: "Event type", mono: true },
    { key: "sourceSystem", header: "Legacy system", render: (row) => sourceLabel(row.sourceSystem) },
    { key: "version", header: "Version", align: "end" },
    { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
    { key: "updatedAt", header: "Updated", render: (row) => formatDateTime(row.updatedAt) },
  ];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Outbound mappings"
        description="How a Passage event, such as a confirmed sales order or a posted invoice, becomes the request body the integration hub pushes to the customer's legacy ERP. Customers and products are written with the legacy keys the migration recorded."
        actions={
          canWrite ? (
            <Link className={buttonClassName("primary")} to="/migration/outbound/new">
              New outbound mapping
            </Link>
          ) : null
        }
      />
      <div className={styles.toolbar}>
        <FormField id="outbound-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value)}>
              <option value="">All statuses</option>
              <option value="active">Active</option>
              <option value="archived">Archived</option>
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load outbound mappings" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Outbound mappings"
          columns={columns}
          rows={paged.rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No outbound mappings yet", description: "Create one to push confirmed orders or posted invoices to the legacy ERP through the integration hub." }}
          onRowOpen={(row) => navigate(`/migration/outbound/${row.id}`)}
        />
      )}
      <LoadMore paged={paged} noun="outbound mappings" />
    </div>
  );
}

/** Create a mapping. A draft can be tried with a sample event before it is saved. */
export function NewOutboundPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_OUTBOUND_WRITE);
  const [form, setForm] = useState<OutboundForm>(EMPTY_OUTBOUND_FORM);
  const [errors, setErrors] = useState<Partial<Record<keyof OutboundForm, string>>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [key, setKey] = useState(newKey());

  function edit(next: OutboundForm) {
    setForm(next);
    setKey(newKey());
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const parsed = parseOutboundForm(form);
    setErrors(parsed.errors);
    if (!parsed.value) return;
    setBusy(true);
    try {
      const saved = await createOutboundMapping(parsed.value, key);
      navigate(`/migration/outbound/${saved.id}`);
    } catch (e) {
      setFailure(describeProblem(e));
      setKey(newKey());
    } finally {
      setBusy(false);
    }
  }

  async function draftPreview(event: Record<string, unknown>) {
    const parsed = parseOutboundForm(form);
    setErrors(parsed.errors);
    if (!parsed.value) throw Object.assign(new Error("Fix the highlighted fields first."), { code: "UI-FORM" });
    return previewOutboundDraft(parsed.value.sourceSystem, parsed.value.definition, event);
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New outbound mapping" description="Choose the legacy system and describe the request body. Try it on a sample event with Preview before you save." />
      {canWrite ? null : (
        <Alert tone="warning" title="You can view outbound mappings but not create them">
          Creating a mapping needs the migration.project.write duty.
        </Alert>
      )}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="outbound-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} value={form.name} invalid={Boolean(errors.name)} onChange={(event) => edit({ ...form, name: event.target.value })} />}
          </FormField>
          <SourceSystemField id="outbound-system" value={form.sourceSystem} error={errors.sourceSystem} onChange={(sourceSystem) => edit({ ...form, sourceSystem })} />
          <FormField
            id="outbound-definition"
            label="Definition"
            error={errors.definition}
            hint="JSON: the event type (a name, * for one segment, a trailing ** for the rest), the record the legacy key belongs to, the legacy keys to look up, the lookup table entries to read (lookups), collections that map an event array such as data.lines to a body array (each element has its own xrefs, lookups and fields; fields read the element, parent.*, index), and the mapping fields. Fields read data.* of the event, xref.<name> and lookup.<name>."
          >
            {(control) => <Textarea {...control} mono rows={22} value={form.definition} invalid={Boolean(errors.definition)} onChange={(event) => edit({ ...form, definition: event.target.value })} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the mapping" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy} disabled={!canWrite}>
              Create mapping
            </Button>
            <Link className={buttonClassName("ghost")} to="/migration/outbound">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      <OutboundPreviewPanel run={draftPreview} />
    </div>
  );
}

/** One mapping: its definition (a change writes a new version), the version history, archive and preview. */
export function OutboundDetailPage() {
  const { id = "" } = useParams();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_OUTBOUND_WRITE);
  const [pinned, setPinned] = useState<number | undefined>(undefined);
  const state = useLoad((signal) => getOutboundMapping(id, pinned, signal), `${id}|${pinned ?? ""}`);
  const versions = useLoad((signal) => listOutboundVersions(id, signal), `${id}|${state.data?.version ?? 0}`);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [draft, setDraft] = useState<string | null>(null);
  const [draftError, setDraftError] = useState<string | undefined>();
  const mapping = state.data?.id === id ? state.data : null;

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Outbound mapping" />
        <Alert tone="danger" title="Could not load this outbound mapping" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link className={buttonClassName("ghost")} to="/migration/outbound">
          Back to outbound mappings
        </Link>
      </div>
    );
  }
  if (!mapping) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Outbound mapping" description="Loading..." />
      </div>
    );
  }
  const current = versions.data?.[0]?.version ?? mapping.version;
  const viewingOld = pinned !== undefined && pinned !== current;

  async function change(edit: Parameters<typeof updateOutboundMapping>[2]): Promise<boolean> {
    if (!mapping) return false;
    setBusy(true);
    setFailure(null);
    try {
      setPinned(undefined);
      state.set(await updateOutboundMapping(mapping.id, current, edit));
      return true;
    } catch (e) {
      setFailure(describeProblem(e));
      return false;
    } finally {
      setBusy(false);
    }
  }

  async function saveDraft(event: FormEvent) {
    event.preventDefault();
    if (draft === null) return;
    const parsed = parseDefinitionText(draft);
    setDraftError(parsed.error);
    if (!parsed.value) return;
    if (await change({ definition: parsed.value })) setDraft(null);
  }

  const versionColumns: DataTableColumn<OutboundVersion>[] = [
    { key: "version", header: "Version", align: "end" },
    { key: "createdAt", header: "Saved", render: (row) => formatDateTime(row.createdAt) },
    { key: "createdBy", header: "By", mono: true },
    { key: "checksum", header: "Checksum", mono: true, render: (row) => row.checksum.slice(0, 12) },
    {
      key: "open",
      header: "Definition",
      render: (row) => (
        <Button size="sm" onClick={() => setPinned(row.version === current ? undefined : row.version)}>
          {row.version === current ? "Show current" : `Show v${row.version}`}
        </Button>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={mapping.name}
        description={`${mapping.eventType} to ${sourceLabel(mapping.sourceSystem)}. Created ${formatDateTime(mapping.createdAt)}.`}
        actions={
          <>
            <StatusPill status={mapping.status} />
            {canWrite ? (
              <Button loading={busy} onClick={() => change({ status: mapping.status === "active" ? "archived" : "active" })}>
                {mapping.status === "active" ? "Archive" : "Reactivate"}
              </Button>
            ) : null}
          </>
        }
      />
      {failure ? (
        <Alert tone="danger" title="Could not change the mapping" live="alert" action={<Button size="sm" onClick={state.reload}>Reload</Button>}>
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {mapping.status === "archived" ? (
        <Alert tone="info" title="This mapping is archived">
          The integration hub does not render archived mappings. Reactivate it to push events again.
        </Alert>
      ) : null}
      {draft !== null && canWrite ? (
        <Card title="Edit definition">
          <form className={styles.form} onSubmit={saveDraft} noValidate>
            <FormField id="outbound-draft" label="Definition" error={draftError} hint="Saving writes a new version. Earlier versions stay readable and can still be rendered by version.">
              {(control) => <Textarea {...control} mono rows={22} value={draft} invalid={Boolean(draftError)} onChange={(event) => setDraft(event.target.value)} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Save as version {current + 1}
              </Button>
              <Button onClick={() => setDraft(null)}>Cancel</Button>
            </div>
          </form>
        </Card>
      ) : (
        <Card
          title={viewingOld ? `Definition, version ${mapping.version}` : "Definition"}
          actions={canWrite && !viewingOld && mapping.definition ? <Button size="sm" onClick={() => { setDraft(toJsonText(mapping.definition)); setDraftError(undefined); }}>Edit definition</Button> : undefined}
        >
          <dl className={styles.details}>
            <Entry term="Event type">
              <span className={styles.mono}>{mapping.eventType}</span>
            </Entry>
            <Entry term="Legacy system">{sourceLabel(mapping.sourceSystem as SourceSystem)}</Entry>
            <Entry term="Version">{mapping.version}</Entry>
          </dl>
          <pre className={styles.code} tabIndex={0} aria-label="Definition JSON">{toJsonText(mapping.definition)}</pre>
        </Card>
      )}
      {versions.error ? (
        <Alert tone="danger" title="Could not load the version history" live="alert" action={<Button size="sm" onClick={versions.reload}>Try again</Button>}>
          {versions.error.message} ({versions.error.code})
        </Alert>
      ) : (
        <DataTable label="Versions" columns={versionColumns} rows={versions.data ?? []} getRowKey={(row) => String(row.version)} loading={versions.loading} empty={{ title: "No versions", description: "A mapping always has at least version 1." }} />
      )}
      <OutboundPreviewPanel run={(event) => previewOutboundMapping(mapping.id, event, viewingOld ? mapping.version : undefined)} />
      <Link className={buttonClassName("ghost")} to="/migration/outbound">
        Back to outbound mappings
      </Link>
    </div>
  );
}
