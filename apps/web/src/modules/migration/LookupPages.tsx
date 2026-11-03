import { useMemo, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  createLookupTable,
  describeProblem,
  getLookupTable,
  listLookupTables,
  listLookupVersions,
  newKey,
  updateLookupTable,
  type LookupTable,
  type LookupVersion,
  type Problem,
} from "./api/client";
import { formatDateTime } from "./domain";
import { LoadMore } from "./LoadMore";
import { EMPTY_LOOKUP_FORM, filterEntries, kindLabel, LOOKUP_KINDS, parseEntriesText, parseLookupForm, starterEntriesText, type LookupForm } from "./lookupDomain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { usePaged } from "./usePaged";

/** Enforced by the service; not in policies/authz/data.json for the nav, so it gates the buttons only. */
export const DUTY_LOOKUP_WRITE = "migration.project.write";

const ENTRY_LIMIT = 200;

function Entry({ term, children }: { term: string; children: React.ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}

/** Lookup tables: enum codes, unit conversions and price lists that outbound mappings read while they render. */
export function LookupListPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_LOOKUP_WRITE);
  const [status, setStatus] = useState("");
  const [kind, setKind] = useState("");
  const filter = { status: status || undefined, kind: kind || undefined };
  const filterKey = `${status}|${kind}`;
  const state = useLoad((signal) => listLookupTables(filter, signal), filterKey);
  const paged = usePaged(state, filterKey, (cursor) => listLookupTables({ ...filter, cursor }));
  const columns: DataTableColumn<LookupTable>[] = [
    { key: "name", header: "Name", mono: true },
    { key: "kind", header: "Holds", render: (row) => kindLabel(row.kind) },
    { key: "entryCount", header: "Entries", align: "end" },
    { key: "version", header: "Version", align: "end" },
    { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
    { key: "updatedAt", header: "Updated", render: (row) => formatDateTime(row.updatedAt) },
  ];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Lookup tables"
        description="Values an outbound mapping looks up by Passage code while it builds the request for the legacy ERP: unit and status codes, unit-of-measure conversion factors and price lists. A mapping reads a table by name; an entry that is missing stops the push instead of sending a guess."
        actions={
          canWrite ? (
            <Link className={buttonClassName("primary")} to="/migration/lookups/new">
              New lookup table
            </Link>
          ) : null
        }
      />
      <div className={styles.toolbar}>
        <FormField id="lookup-kind" label="Holds">
          {(control) => (
            <Select {...control} value={kind} onChange={(event) => setKind(event.target.value)}>
              <option value="">All kinds</option>
              {LOOKUP_KINDS.map((k) => (
                <option key={k.value} value={k.value}>
                  {k.label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="lookup-status" label="Status">
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
        <Alert tone="danger" title="Could not load lookup tables" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Lookup tables"
          columns={columns}
          rows={paged.rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No lookup tables yet", description: "Create one to translate Passage codes, units or prices in an outbound mapping." }}
          onRowOpen={(row) => navigate(`/migration/lookups/${row.id}`)}
        />
      )}
      <LoadMore paged={paged} noun="lookup tables" />
    </div>
  );
}

/** Create a table: a name, what it holds and its entries. */
export function NewLookupPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_LOOKUP_WRITE);
  const [form, setForm] = useState<LookupForm>(EMPTY_LOOKUP_FORM);
  const [errors, setErrors] = useState<Partial<Record<keyof LookupForm, string>>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [key, setKey] = useState(newKey());
  const hint = LOOKUP_KINDS.find((k) => k.value === form.kind)?.hint;

  function edit(next: LookupForm) {
    setForm(next);
    setKey(newKey());
  }

  function changeKind(kind: string) {
    // Replace the example entries with the new kind's, but never overwrite entries the person has typed.
    const untouched = LOOKUP_KINDS.some((k) => starterEntriesText(k.value) === form.entries);
    const next = LOOKUP_KINDS.find((k) => k.value === kind);
    edit({ ...form, kind, entries: untouched && next ? starterEntriesText(next.value) : form.entries });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const parsed = parseLookupForm(form);
    setErrors(parsed.errors);
    if (!parsed.value) return;
    setBusy(true);
    try {
      const saved = await createLookupTable(parsed.value, key);
      navigate(`/migration/lookups/${saved.id}`);
    } catch (e) {
      setFailure(describeProblem(e));
      setKey(newKey());
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New lookup table" description="Name the table, choose what it holds and list its entries. Outbound mappings refer to it by name in their lookups." />
      {canWrite ? null : (
        <Alert tone="warning" title="You can view lookup tables but not create them">
          Creating a table needs the migration.project.write duty.
        </Alert>
      )}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="lookup-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} value={form.name} invalid={Boolean(errors.name)} onChange={(event) => edit({ ...form, name: event.target.value })} />}
          </FormField>
          <FormField id="lookup-new-kind" label="Holds" error={errors.kind} hint={hint}>
            {(control) => (
              <Select {...control} value={form.kind} onChange={(event) => changeKind(event.target.value)}>
                {LOOKUP_KINDS.map((k) => (
                  <option key={k.value} value={k.value}>
                    {k.label}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="lookup-description" label="Description" error={errors.description}>
            {(control) => <Input {...control} value={form.description} invalid={Boolean(errors.description)} onChange={(event) => edit({ ...form, description: event.target.value })} />}
          </FormField>
          <FormField id="lookup-entries" label="Entries" error={errors.entries} hint='JSON object of key to value, both in quotes. A key of two or three parts joins them with |. At most 5000 entries.'>
            {(control) => <Textarea {...control} mono rows={14} value={form.entries} invalid={Boolean(errors.entries)} onChange={(event) => edit({ ...form, entries: event.target.value })} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the table" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy} disabled={!canWrite}>
              Create table
            </Button>
            <Link className={buttonClassName("ghost")} to="/migration/lookups">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

/** One table: its entries (searchable), edit as the next version, archive, and the version history. */
export function LookupDetailPage() {
  const { id = "" } = useParams();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_LOOKUP_WRITE);
  const [pinned, setPinned] = useState<number | undefined>(undefined);
  const state = useLoad((signal) => getLookupTable(id, pinned, signal), `${id}|${pinned ?? ""}`);
  const versions = useLoad((signal) => listLookupVersions(id, signal), `${id}|${state.data?.version ?? 0}`);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [draft, setDraft] = useState<string | null>(null);
  const [draftError, setDraftError] = useState<string | undefined>();
  const [search, setSearch] = useState("");
  const table = state.data?.id === id ? state.data : null;
  const shown = useMemo(() => filterEntries(table?.definition?.entries ?? {}, search, ENTRY_LIMIT), [table, search]);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Lookup table" />
        <Alert tone="danger" title="Could not load this lookup table" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link className={buttonClassName("ghost")} to="/migration/lookups">
          Back to lookup tables
        </Link>
      </div>
    );
  }
  if (!table) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Lookup table" description="Loading..." />
      </div>
    );
  }
  const current = versions.data?.[0]?.version ?? table.version;
  const viewingOld = pinned !== undefined && pinned !== current;

  async function change(edit: Parameters<typeof updateLookupTable>[2]): Promise<boolean> {
    if (!table) return false;
    setBusy(true);
    setFailure(null);
    try {
      setPinned(undefined);
      state.set(await updateLookupTable(table.id, current, edit));
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
    if (draft === null || !table?.definition) return;
    const parsed = parseEntriesText(draft);
    setDraftError(parsed.error);
    if (!parsed.value) return;
    if (await change({ definition: { ...table.definition, entries: parsed.value } })) setDraft(null);
  }

  const entryColumns: DataTableColumn<{ key: string; value: string }>[] = [
    { key: "key", header: "Key", mono: true },
    { key: "value", header: "Value", mono: true },
  ];
  const versionColumns: DataTableColumn<LookupVersion>[] = [
    { key: "version", header: "Version", align: "end" },
    { key: "entryCount", header: "Entries", align: "end" },
    { key: "createdAt", header: "Saved", render: (row) => formatDateTime(row.createdAt) },
    { key: "createdBy", header: "By", mono: true },
    { key: "checksum", header: "Checksum", mono: true, render: (row) => row.checksum.slice(0, 12) },
    {
      key: "open",
      header: "Entries",
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
        title={table.name}
        description={`${kindLabel(table.kind)}, ${table.entryCount} entries. Created ${formatDateTime(table.createdAt)}.${table.definition?.description ? ` ${table.definition.description}` : ""}`}
        actions={
          <>
            <StatusPill status={table.status} />
            {canWrite ? (
              <Button loading={busy} onClick={() => change({ status: table.status === "active" ? "archived" : "active" })}>
                {table.status === "active" ? "Archive" : "Reactivate"}
              </Button>
            ) : null}
          </>
        }
      />
      {failure ? (
        <Alert tone="danger" title="Could not change the table" live="alert" action={<Button size="sm" onClick={state.reload}>Reload</Button>}>
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {table.status === "archived" ? (
        <Alert tone="info" title="This table is archived">
          Outbound mappings cannot read an archived table: renders that need it stop as unresolved. Reactivate it to push events again.
        </Alert>
      ) : null}
      {draft !== null && canWrite ? (
        <Card title="Edit entries">
          <form className={styles.form} onSubmit={saveDraft} noValidate>
            <FormField id="lookup-draft" label="Entries" error={draftError} hint="Replaces all entries. Saving writes a new version; earlier versions stay readable.">
              {(control) => <Textarea {...control} mono rows={18} value={draft} invalid={Boolean(draftError)} onChange={(event) => setDraft(event.target.value)} />}
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
          title={viewingOld ? `Entries, version ${table.version}` : "Entries"}
          actions={canWrite && !viewingOld && table.definition ? <Button size="sm" onClick={() => { setDraft(JSON.stringify(table.definition?.entries ?? {}, null, 2)); setDraftError(undefined); }}>Edit entries</Button> : undefined}
        >
          <dl className={styles.details}>
            <Entry term="Holds">{kindLabel(table.kind)}</Entry>
            <Entry term="Version">{table.version}</Entry>
          </dl>
          <FormField id="lookup-search" label="Search entries">
            {(control) => <Input {...control} value={search} onChange={(event) => setSearch(event.target.value)} />}
          </FormField>
          <DataTable
            label="Entries"
            columns={entryColumns}
            rows={shown.rows}
            getRowKey={(row) => row.key}
            empty={{ title: search ? "No entry matches" : "No entries", description: search ? "Try a shorter search." : "Edit the table to add entries." }}
          />
          {shown.total > shown.rows.length ? <p className={styles.note}>Showing the first {shown.rows.length} of {shown.total} matching entries. Search to narrow the list.</p> : null}
        </Card>
      )}
      {versions.error ? (
        <Alert tone="danger" title="Could not load the version history" live="alert" action={<Button size="sm" onClick={versions.reload}>Try again</Button>}>
          {versions.error.message} ({versions.error.code})
        </Alert>
      ) : (
        <DataTable label="Versions" columns={versionColumns} rows={versions.data ?? []} getRowKey={(row) => String(row.version)} loading={versions.loading} empty={{ title: "No versions", description: "A table always has at least version 1." }} />
      )}
      <Link className={buttonClassName("ghost")} to="/migration/lookups">
        Back to lookup tables
      </Link>
    </div>
  );
}
