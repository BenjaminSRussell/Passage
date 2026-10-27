import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  CostingApiError,
  createVersion,
  describeError,
  listVersions,
  type CostingVersion,
  type CreateVersionRequest,
  type VersionStatus,
  type VersionType,
} from "./api/client";
import styles from "./pages.module.css";
import { CURRENCY } from "./shared";
import { useLoad } from "./useLoad";
import { formatDateTime, LoadError, UUID } from "./versionParts";

const CODE = /^[A-Z0-9][A-Z0-9_-]{1,31}$/;
const DAY = /^\d{4}-\d{2}-\d{2}$/;

const columns: DataTableColumn<CostingVersion>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "type", header: "Type", render: (row) => (row.type === "STANDARD" ? "Standard" : "Planned") },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "currency", header: "Currency", mono: true },
  { key: "effectiveFrom", header: "Effective from", mono: true },
  { key: "itemCount", header: "Item costs", align: "end" },
  { key: "activatedAt", header: "Activated", render: (row) => formatDateTime(row.activatedAt) },
];

/** Costing versions (FR-20.5): standard and planned cost sets per legal entity, by code. */
export function VersionsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<VersionStatus | "">("");
  const [type, setType] = useState<VersionType | "">("");
  const [more, setMore] = useState<{ key: string; rows: CostingVersion[]; next?: string }>({ key: "", rows: [] });
  const [moreError, setMoreError] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const filterKey = `${status}|${type}`;
  const state = useLoad((signal) => listVersions({ status, type }, signal), filterKey);

  const extra = more.key === filterKey ? more : { key: filterKey, rows: [], next: undefined };
  const rows = [...(state.data?.items ?? []), ...extra.rows];
  const next = extra.rows.length > 0 ? extra.next : state.data?.nextCursor;

  async function loadMore() {
    if (!next) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listVersions({ status, type, cursor: next });
      setMore({ key: filterKey, rows: [...extra.rows, ...page.items], next: page.nextCursor });
    } catch (err) {
      setMoreError(describeError(err, "The next page could not be loaded."));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Costing versions"
        description="Standard and planned costs per product and site. Fill a draft by import or copy, preview the revaluation of on-hand, then activate it."
        actions={
          <Link className={buttonClassName("primary")} to="/costing/versions/new">
            New costing version
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="versions-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as VersionStatus | "")}>
              <option value="">All</option>
              <option value="DRAFT">Draft</option>
              <option value="ACTIVE">Active</option>
              <option value="ARCHIVED">Archived</option>
            </Select>
          )}
        </FormField>
        <FormField id="versions-type" label="Type">
          {(c) => (
            <Select {...c} value={type} onChange={(e) => setType(e.target.value as VersionType | "")}>
              <option value="">All</option>
              <option value="STANDARD">Standard</option>
              <option value="PLANNED">Planned</option>
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load costing versions" error={state.error} onRetry={state.reload} />
      ) : (
        <DataTable
          label="Costing versions"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading && !state.data}
          empty={{ title: "No costing versions yet", description: "Create a standard cost version, such as STD-2026, to set standard costs." }}
          onRowOpen={(row) => navigate(`/costing/versions/${row.id}`)}
        />
      )}
      {moreError ? (
        <Alert tone="danger" live="alert">
          {moreError}
        </Alert>
      ) : null}
      {next && !state.error ? (
        <div className={styles.more}>
          <Button loading={loadingMore} onClick={loadMore}>
            Load more versions
          </Button>
        </div>
      ) : null}
    </div>
  );
}

type Field = keyof CreateVersionRequest;
type Errors = Partial<Record<Field, string>>;

/** Validates the create form as the service does, so most mistakes never leave the page. */
export function validateVersion(v: CreateVersionRequest): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId)) errors.legalEntityId = "Enter the legal entity's id (a UUID).";
  if (!CODE.test(v.code)) errors.code = "Use 2 to 32 capital letters, digits, _ or -, such as STD-2026.";
  if (!CURRENCY.test(v.currency)) errors.currency = "Use a three-letter ISO 4217 code, such as EUR.";
  if (!DAY.test(v.effectiveFrom)) errors.effectiveFrom = "Enter the date the costs take effect.";
  return errors;
}

/** Create a DRAFT costing version, then open it to fill in costs. */
export function CreateVersionPage() {
  const navigate = useNavigate();
  const [form, setForm] = useState<CreateVersionRequest>({
    legalEntityId: "",
    code: "",
    type: "STANDARD",
    currency: "EUR",
    effectiveFrom: new Date().toISOString().slice(0, 10),
    blockActivation: false,
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per form content: a retried submit of the same form is a replay, an edited form is a new request.
  const key = useRef(crypto.randomUUID());

  function set<K extends Field>(field: K, value: CreateVersionRequest[K]) {
    key.current = crypto.randomUUID();
    setForm((f) => ({ ...f, [field]: value }));
  }

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setFailure(null);
    const body = { ...form, legalEntityId: form.legalEntityId.trim(), code: form.code.trim(), currency: form.currency.trim() };
    const found = validateVersion(body);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const v = await createVersion(body, key.current);
      navigate(`/costing/versions/${v.id}`);
    } catch (err) {
      if (err instanceof CostingApiError && err.problem.errors) {
        setErrors(err.problem.errors as Errors);
      }
      setFailure(describeError(err, "The costing version could not be created."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New costing version"
        description="A version starts as a draft. Import or copy its costs, then preview and activate it."
        actions={
          <Link className={buttonClassName("ghost")} to="/costing/versions">
            Back to costing versions
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <FormField id="version-le" label="Legal entity" hint="The legal entity's id. Its sites are the sites the costs apply to." error={errors.legalEntityId}>
            {(c) => <Input {...c} className={styles.mono} value={form.legalEntityId} invalid={Boolean(errors.legalEntityId)} onChange={(e) => set("legalEntityId", e.target.value)} />}
          </FormField>
          <FormField id="version-code" label="Code" hint="Unique in the legal entity, such as STD-2026." error={errors.code}>
            {(c) => <Input {...c} value={form.code} invalid={Boolean(errors.code)} onChange={(e) => set("code", e.target.value.toUpperCase())} />}
          </FormField>
          <FormField id="version-type" label="Type" hint="Only standard versions are activated; planned costs feed cost roll-up.">
            {(c) => (
              <Select {...c} value={form.type} onChange={(e) => set("type", e.target.value as VersionType)}>
                <option value="STANDARD">Standard</option>
                <option value="PLANNED">Planned</option>
              </Select>
            )}
          </FormField>
          <FormField id="version-currency" label="Currency" error={errors.currency}>
            {(c) => <Input {...c} value={form.currency} invalid={Boolean(errors.currency)} onChange={(e) => set("currency", e.target.value.toUpperCase())} />}
          </FormField>
          <FormField id="version-effective" label="Effective from" error={errors.effectiveFrom}>
            {(c) => <Input {...c} type="date" value={form.effectiveFrom} invalid={Boolean(errors.effectiveFrom)} onChange={(e) => set("effectiveFrom", e.target.value)} />}
          </FormField>
          <span className={styles.check}>
            <Input id="version-block" type="checkbox" checked={form.blockActivation} onChange={(e) => set("blockActivation", e.target.checked)} />
            <label htmlFor="version-block">Block activation</label>
          </span>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create version
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" live="alert">
        {failure}
      </Alert>
    </div>
  );
}
