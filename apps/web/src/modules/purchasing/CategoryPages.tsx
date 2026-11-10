import { useEffect, useMemo, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, buttonClassName, type DataTableColumn } from "../../shell/ui";
import {
  changeCategoryVendor,
  createCategoryManager,
  createCategoryVendor,
  describeProblem,
  getCategoryVendor,
  listCategoryManagers,
  listCategoryVendors,
  newIdempotencyKey,
  removeCategoryManager,
  type AssignmentStatus,
  type CategoryManager,
  type CategoryVendorAssignment,
} from "./api/client";
import { Details, LoadError, ShortId, StatusBadge, datePattern, statusLabel, todayIso, useResource, uuidPattern, type Failure } from "./parts";
import styles from "./pages.module.css";

const STATUSES: AssignmentStatus[] = ["APPROVED", "PROVISIONAL", "BLOCKED"];

type Errors = Record<string, string>;

const window_ = (a: { validFrom: string; validTo?: string }) => (a.validTo ? `${a.validFrom} to ${a.validTo}` : `From ${a.validFrom}, open-ended`);

const columns: DataTableColumn<CategoryVendorAssignment>[] = [
  { key: "category", header: "Category" },
  { key: "vendorId", header: "Vendor", render: (a) => <ShortId id={a.vendorId} /> },
  { key: "status", header: "Status", render: (a) => <StatusBadge status={a.status} /> },
  { key: "window", header: "Valid", mono: true, render: window_ },
  { key: "legalEntityId", header: "Legal entity", render: (a) => <ShortId id={a.legalEntityId} /> },
];

/** Category vendor assignments with status and category filters and load-more paging (FR-17.20). */
export function CategoryVendorsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<AssignmentStatus | "">("");
  const [category, setCategory] = useState("");
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<CategoryVendorAssignment[]>([]);
  const page = useResource(
    async (signal) => {
      const result = await listCategoryVendors({ status, category }, { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [status, category, cursor],
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Category vendors"
        description="Which vendors may supply a procurement category. A category with an approved or provisional vendor is restricted, and a blocked vendor is refused while its block is in force: the purchasing policy decides whether a vendor outside the rule is refused, warned about or allowed."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/purchasing/category-managers">
              Category managers
            </Link>
            <Link className={buttonClassName("primary")} to="/purchasing/category-vendors/new">
              New assignment
            </Link>
          </>
        }
      />
      <div className={styles.toolbar}>
        <div className={styles.filter}>
          <FormField id="cv-status-filter" label="Status">
            {(control) => (
              <Select
                {...control}
                value={status}
                onChange={(e) => {
                  setCursor(undefined);
                  setStatus(e.target.value as AssignmentStatus | "");
                }}
              >
                <option value="">All statuses</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {statusLabel(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
        <div className={styles.filter}>
          <FormField id="cv-category-filter" label="Category" optional>
            {(control) => (
              <Input
                {...control}
                value={category}
                onChange={(e) => {
                  setCursor(undefined);
                  setCategory(e.target.value);
                }}
              />
            )}
          </FormField>
        </div>
      </div>
      {page.error ? <LoadError what="category vendors" error={page.error} onRetry={page.reload} /> : null}
      <DataTable
        label="Category vendor assignments"
        columns={columns}
        rows={page.error ? [] : rows}
        getRowKey={(a) => a.id}
        loading={page.loading && rows.length === 0}
        empty={{ title: "No assignments yet", description: "Assign vendors to a category to restrict who may supply it." }}
        onRowOpen={(a) => navigate(`/purchasing/category-vendors/${a.id}`)}
      />
      {page.data?.nextCursor ? (
        <div className={styles.actions}>
          <Button loading={page.loading} onClick={() => setCursor(page.data?.nextCursor)}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

/** Create form: assign a vendor to a category with a status and validity window. */
export function CategoryVendorCreatePage() {
  const navigate = useNavigate();
  const [form, setForm] = useState({
    legalEntityId: "",
    category: "",
    vendorId: "",
    status: "APPROVED" as AssignmentStatus,
    validFrom: todayIso(),
    validTo: "",
    notes: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useMemo(() => newIdempotencyKey(), [form]);
  const set = (patch: Partial<typeof form>) => setForm((f) => ({ ...f, ...patch }));

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found: Errors = {};
    if (!uuidPattern.test(form.legalEntityId.trim())) found.legalEntityId = "Enter the legal entity id (a UUID).";
    if (form.category.trim() === "") found.category = "Enter the category.";
    if (!uuidPattern.test(form.vendorId.trim())) found.vendorId = "Enter the vendor id (a UUID).";
    if (!datePattern.test(form.validFrom)) found.validFrom = "Enter a date.";
    if (form.validTo !== "" && (!datePattern.test(form.validTo) || form.validTo < form.validFrom)) {
      found.validTo = "Enter a date on or after valid from.";
    }
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await createCategoryVendor(
        {
          legalEntityId: form.legalEntityId.trim(),
          category: form.category.trim(),
          vendorId: form.vendorId.trim(),
          status: form.status,
          validFrom: form.validFrom,
          validTo: form.validTo || undefined,
          notes: form.notes.trim() || undefined,
        },
        key,
      );
      navigate(`/purchasing/category-vendors/${body.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New category vendor assignment"
        description="A vendor has one assignment per category. Change its status or window afterwards instead of adding another."
      />
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <Card title="Assignment">
          <div className={styles.grid}>
            <FormField id="cv-legal-entity" label="Legal entity" error={errors.legalEntityId}>
              {(c) => <EntityLookup entity="legalEntity" {...c} value={form.legalEntityId} onChange={(e) => set({ legalEntityId: e.target.value })} />}
            </FormField>
            <FormField id="cv-category" label="Category" hint="As written on requisition and PO lines and catalog entries" error={errors.category}>
              {(c) => <Input {...c} value={form.category} onChange={(e) => set({ category: e.target.value })} />}
            </FormField>
            <FormField id="cv-vendor" label="Vendor" error={errors.vendorId}>
              {(c) => <EntityLookup entity="vendor" {...c} value={form.vendorId} onChange={(e) => set({ vendorId: e.target.value })} />}
            </FormField>
            <FormField id="cv-status" label="Status" error={errors.status}>
              {(c) => (
                <Select {...c} value={form.status} onChange={(e) => set({ status: e.target.value as AssignmentStatus })}>
                  {STATUSES.map((s) => (
                    <option key={s} value={s}>
                      {statusLabel(s)}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="cv-valid-from" label="Valid from" error={errors.validFrom}>
              {(c) => <Input {...c} type="date" value={form.validFrom} onChange={(e) => set({ validFrom: e.target.value })} />}
            </FormField>
            <FormField id="cv-valid-to" label="Valid to" optional hint="Blank is open-ended" error={errors.validTo}>
              {(c) => <Input {...c} type="date" value={form.validTo} onChange={(e) => set({ validTo: e.target.value })} />}
            </FormField>
            <FormField id="cv-notes" label="Notes" optional>
              {(c) => <Input {...c} value={form.notes} onChange={(e) => set({ notes: e.target.value })} />}
            </FormField>
          </div>
        </Card>
        <Alert tone="danger" live="alert">
          {failure ? `${failure.message} (${failure.code})` : null}
        </Alert>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save assignment
          </Button>
          <Link className={buttonClassName("ghost")} to="/purchasing/category-vendors">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}

/** One assignment: read it and change status, window and notes under its version. */
export function CategoryVendorDetailPage() {
  const { id = "" } = useParams();
  const resource = useResource(async (signal) => (await getCategoryVendor(id, signal)).body, [id]);
  const [saved, setSaved] = useState<CategoryVendorAssignment | null>(null);
  const [draft, setDraft] = useState({ status: "APPROVED" as AssignmentStatus, validFrom: "", validTo: "", notes: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const a = resource.data;
    if (a) {
      setSaved(a);
      setDraft({ status: a.status, validFrom: a.validFrom, validTo: a.validTo ?? "", notes: a.notes ?? "" });
    }
  }, [resource.data]);

  function reload() {
    setFailure(null);
    setNotice(null);
    resource.reload();
  }

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    if (!saved) return;
    const found: Errors = {};
    if (!datePattern.test(draft.validFrom)) found.validFrom = "Enter a date.";
    if (draft.validTo !== "" && (!datePattern.test(draft.validTo) || draft.validTo < draft.validFrom)) {
      found.validTo = "Enter a date on or after valid from.";
    }
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await changeCategoryVendor(
        saved.id,
        { status: draft.status, validFrom: draft.validFrom, validTo: draft.validTo || undefined, notes: draft.notes.trim() || undefined },
        saved.version,
      );
      setSaved(body);
      setNotice(`Assignment saved as version ${body.version}.`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const stale = failure?.code === "PRC-CAT-013";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Category vendor assignment"
        actions={
          <Link className={buttonClassName("secondary")} to="/purchasing/category-vendors">
            All assignments
          </Link>
        }
      />
      {resource.error ? <LoadError what="the assignment" error={resource.error} onRetry={resource.reload} /> : null}
      {resource.loading && !saved ? <Skeleton /> : null}
      {saved ? (
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <Card>
            <Details
              items={[
                { label: "Category", value: saved.category },
                { label: "Vendor", value: saved.vendorId, mono: true },
                { label: "Legal entity", value: saved.legalEntityId, mono: true },
                { label: "Status", value: <StatusBadge status={saved.status} /> },
                { label: "Valid", value: window_(saved), mono: true },
                { label: "Version", value: String(saved.version), mono: true },
              ]}
            />
          </Card>
          <Card title="Change">
            <div className={styles.grid}>
              <FormField id="cvd-status" label="Status">
                {(c) => (
                  <Select {...c} value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as AssignmentStatus })}>
                    {STATUSES.map((s) => (
                      <option key={s} value={s}>
                        {statusLabel(s)}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="cvd-valid-from" label="Valid from" error={errors.validFrom}>
                {(c) => <Input {...c} type="date" value={draft.validFrom} onChange={(e) => setDraft({ ...draft, validFrom: e.target.value })} />}
              </FormField>
              <FormField id="cvd-valid-to" label="Valid to" optional hint="Blank is open-ended" error={errors.validTo}>
                {(c) => <Input {...c} type="date" value={draft.validTo} onChange={(e) => setDraft({ ...draft, validTo: e.target.value })} />}
              </FormField>
              <FormField id="cvd-notes" label="Notes" optional>
                {(c) => <Input {...c} value={draft.notes} onChange={(e) => setDraft({ ...draft, notes: e.target.value })} />}
              </FormField>
            </div>
          </Card>
          <Alert
            tone="danger"
            live="alert"
            action={
              stale ? (
                <Button type="button" variant="secondary" onClick={reload}>
                  Reload assignment
                </Button>
              ) : undefined
            }
          >
            {failure
              ? stale
                ? `Someone else changed this assignment since you opened it. Reload to see their change, then save again. (${failure.code})`
                : `${failure.message} (${failure.code})`
              : null}
          </Alert>
          <Alert tone="success" live="status">
            {notice}
          </Alert>
          <div className={styles.actions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save changes
            </Button>
          </div>
        </form>
      ) : null}
    </div>
  );
}

const managerColumns = (onRemove: (m: CategoryManager) => void, removing: string | null): DataTableColumn<CategoryManager>[] => [
  { key: "category", header: "Category" },
  { key: "workerId", header: "Worker", render: (m) => <ShortId id={m.workerId} /> },
  { key: "legalEntityId", header: "Legal entity", render: (m) => <ShortId id={m.legalEntityId} /> },
  {
    key: "remove",
    header: "Actions",
    render: (m) => (
      <Button size="sm" variant="ghost" loading={removing === m.id} onClick={() => onRemove(m)}>
        Remove {m.category} manager
      </Button>
    ),
  },
];

/** Category managers: the list with a category filter, an add form and removal (FR-17.20). */
export function CategoryManagersPage() {
  const [category, setCategory] = useState("");
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<CategoryManager[]>([]);
  const page = useResource(
    async (signal) => {
      const result = await listCategoryManagers({ category }, { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [category, cursor],
  );
  const [form, setForm] = useState({ legalEntityId: "", category: "", workerId: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);
  const key = useMemo(() => newIdempotencyKey(), [form]);

  async function onAdd(event: FormEvent) {
    event.preventDefault();
    const found: Errors = {};
    if (!uuidPattern.test(form.legalEntityId.trim())) found.legalEntityId = "Enter the legal entity id (a UUID).";
    if (form.category.trim() === "") found.category = "Enter the category.";
    if (!uuidPattern.test(form.workerId.trim())) found.workerId = "Enter the worker id (a UUID).";
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await createCategoryManager(
        { legalEntityId: form.legalEntityId.trim(), category: form.category.trim(), workerId: form.workerId.trim() },
        key,
      );
      setNotice(`${form.category.trim()} manager added.`);
      setForm({ ...form, workerId: "" });
      setCursor(undefined);
      page.reload();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors);
    } finally {
      setBusy(false);
    }
  }

  async function onRemove(manager: CategoryManager) {
    setRemoving(manager.id);
    setFailure(null);
    setNotice(null);
    try {
      await removeCategoryManager(manager.id);
      setNotice(`${manager.category} manager removed.`);
      setCursor(undefined);
      page.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setRemoving(null);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Category managers"
        description="The workers who manage each procurement category."
        actions={
          <Link className={buttonClassName("secondary")} to="/purchasing/category-vendors">
            Category vendors
          </Link>
        }
      />
      <Card title="Add a manager">
        <form className={styles.form} onSubmit={onAdd} noValidate>
          <div className={styles.grid}>
            <FormField id="cm-legal-entity" label="Legal entity" error={errors.legalEntityId}>
              {(c) => <EntityLookup entity="legalEntity" {...c} value={form.legalEntityId} onChange={(e) => setForm({ ...form, legalEntityId: e.target.value })} />}
            </FormField>
            <FormField id="cm-category" label="Category" error={errors.category}>
              {(c) => <Input {...c} value={form.category} onChange={(e) => setForm({ ...form, category: e.target.value })} />}
            </FormField>
            <FormField id="cm-worker" label="Worker" error={errors.workerId}>
              {(c) => <EntityLookup entity="worker" {...c} value={form.workerId} onChange={(e) => setForm({ ...form, workerId: e.target.value })} />}
            </FormField>
          </div>
          <div className={styles.actions}>
            <Button type="submit" variant="primary" loading={busy}>
              Add manager
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      <div className={styles.toolbar}>
        <div className={styles.filter}>
          <FormField id="cm-category-filter" label="Category" optional>
            {(control) => (
              <Input
                {...control}
                value={category}
                onChange={(e) => {
                  setCursor(undefined);
                  setCategory(e.target.value);
                }}
              />
            )}
          </FormField>
        </div>
      </div>
      {page.error ? <LoadError what="category managers" error={page.error} onRetry={page.reload} /> : null}
      <DataTable
        label="Category managers"
        columns={managerColumns(onRemove, removing)}
        rows={page.error ? [] : rows}
        getRowKey={(m) => m.id}
        loading={page.loading && rows.length === 0}
        empty={{ title: "No category managers yet", description: "Add the workers who manage a category." }}
      />
      {page.data?.nextCursor ? (
        <div className={styles.actions}>
          <Button loading={page.loading} onClick={() => setCursor(page.data?.nextCursor)}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}
