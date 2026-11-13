import { useMemo, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  buttonClassName,
  type DataTableColumn,
  ListPage,
  type ListColumn,
  type ListData,
  type ListFilterDef,
  type ListQuery,
  FormErrorSummary,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type FormFieldTarget,
  FormSection,
} from "../../shell/ui";
import {
  actOnCatalog,
  addCatalogEntries,
  createCatalog,
  describeProblem,
  getCatalog,
  listCatalogs,
  newIdempotencyKey,
  listCatalogEntries,
  removeCatalogEntry,
  searchCatalogEntries,
  type Catalog,
  type CatalogAction,
  type CatalogEntry,
  type CatalogEntryInput,
  type CatalogHit,
  type CatalogInput,
  type CatalogStatus,
  type CatalogSummary,
} from "./api/client";
import {
  Details,
  LoadError,
  StatusBadge,
  currencyPattern,
  datePattern,
  decimalPattern,
  money,
  statusLabel,
  todayIso,
  useResource,
  uuidPattern,
  type Failure,
  Named,
  useProcurementPages,
} from "./parts";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const STATUSES: CatalogStatus[] = ["DRAFT", "PUBLISHED", "RETIRED"];

const validity = (c: { validFrom: string; validTo?: string }) => (c.validTo ? `${c.validFrom} to ${c.validTo}` : `From ${c.validFrom}, open-ended`);

const listColumns: ListColumn<CatalogSummary>[] = [
  { key: "number", header: "Number", mono: true },
  { key: "name", header: "Name" },
  { key: "status", header: "Status", render: (c) => <StatusBadge status={c.status} /> },
  { key: "validity", header: "Validity", mono: true, render: validity },
  { key: "entryCount", header: "Entries", align: "end" },
  { key: "legalEntityId", header: "Legal entity", render: (c) => <Named entity="legalEntity" id={c.legalEntityId} /> },
];

const listFilters: ListFilterDef[] = [{ kind: "select", key: "status", label: "Status", anyLabel: "All statuses", options: STATUSES.map((s) => ({ value: s, label: statusLabel(s) })) }];

function useCatalogRows(query: ListQuery): ListData<CatalogSummary> {
  const status = (query.filters.status ?? "") as CatalogStatus | "";
  return useProcurementPages(status, (cursor, signal) => listCatalogs({ status }, { cursor, signal }));
}

/** Procurement catalog list with a status filter and load-more paging (FR-17.18). */
export function CatalogsPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="purchasing-catalogs"
      title="Procurement catalogs"
      description="Vendor products with agreed prices that requesters pick from when they raise a requisition."
      actions={
        <>
          <Link className={buttonClassName("secondary")} to="/purchasing/catalog-imports">
            Vendor imports
          </Link>
          <Link className={buttonClassName("primary")} to="/purchasing/catalogs/new">
            New catalog
          </Link>
        </>
      }
      noun="catalogs"
      singular="catalog"
      columns={listColumns}
      filters={listFilters}
      useRows={useCatalogRows}
      search={(c) => ({ number: c.number, name: c.name })}
      getRowKey={(c) => c.id}
      onRowOpen={(c) => navigate(`/purchasing/catalogs/${c.id}`)}
      empty={{ title: "No catalogs yet", description: "Create a catalog, add vendor items and publish it to your operating units." }}
    />
  );
}

interface EntryDraft {
  key: string;
  vendorId: string;
  vendorPartNo: string;
  productId: string;
  name: string;
  category: string;
  unit: string;
  unitPrice: string;
  currency: string;
  leadTimeDays: string;
  preferred: boolean;
  imageUrl: string;
}

type Errors = Record<string, string>;

function emptyEntry(currency = "USD"): EntryDraft {
  return {
    key: newIdempotencyKey(),
    vendorId: "",
    vendorPartNo: "",
    productId: "",
    name: "",
    category: "",
    unit: "EA",
    unitPrice: "",
    currency,
    leadTimeDays: "",
    preferred: false,
    imageUrl: "",
  };
}

/** Mirrors the service rules for one entry; keys are `${prefix}.field`. */
export function validateEntry(e: EntryDraft, prefix: string): Errors {
  const errors: Errors = {};
  const f = (name: string) => `${prefix}.${name}`;
  if (!uuidPattern.test(e.vendorId)) errors[f("vendorId")] = "Enter the vendor id (a UUID).";
  if (e.productId !== "" && !uuidPattern.test(e.productId)) errors[f("productId")] = "Product id must be a UUID.";
  if (e.name.trim() === "") errors[f("name")] = "Enter a name.";
  if (e.category.trim() === "") errors[f("category")] = "Enter a category.";
  if (e.unit.trim() === "") errors[f("unit")] = "Enter a unit.";
  if (!decimalPattern.test(e.unitPrice)) errors[f("unitPrice")] = "Enter a price of zero or more.";
  if (!currencyPattern.test(e.currency)) errors[f("currency")] = "Use a three-letter ISO code such as USD.";
  if (e.leadTimeDays !== "" && !/^\d{1,4}$/.test(e.leadTimeDays)) errors[f("leadTimeDays")] = "Enter whole days.";
  if (e.imageUrl.trim() !== "" && (!/^https?:\/\/[^\s/]+/i.test(e.imageUrl.trim()) || e.imageUrl.trim().length > 2000)) {
    errors[f("imageUrl")] = "Enter an http(s) image link.";
  }
  return errors;
}

function toEntryInput(e: EntryDraft): CatalogEntryInput {
  const opt = (v: string) => (v.trim() === "" ? undefined : v.trim());
  return {
    vendorId: e.vendorId.trim(),
    vendorPartNo: opt(e.vendorPartNo),
    productId: opt(e.productId),
    name: e.name.trim(),
    category: e.category.trim(),
    unit: e.unit.trim(),
    unitPrice: e.unitPrice.trim(),
    currency: e.currency,
    leadTimeDays: e.leadTimeDays === "" ? undefined : Number(e.leadTimeDays),
    preferred: e.preferred ? true : undefined,
    imageUrl: opt(e.imageUrl),
  };
}

/** The form fields of one entry, shared by the create form and the add-entry card. */
function EntryFields({
  entry,
  prefix,
  errors,
  onChange,
}: {
  entry: EntryDraft;
  prefix: string;
  errors: Errors;
  onChange: (patch: Partial<EntryDraft>) => void;
}) {
  const id = (name: string) => `${prefix.replace(/[^a-z0-9]+/gi, "-")}-${name}`;
  const err = (name: string) => errors[`${prefix}.${name}`];
  return (
    <>
      <div className={styles.grid}>
        <FormField id={id("name")} label="Item name" error={err("name")}>
          {(c) => <Input {...c} value={entry.name} onChange={(e) => onChange({ name: e.target.value })} />}
        </FormField>
        <FormField id={id("vendor")} label="Vendor" error={err("vendorId")}>
          {(c) => <EntityLookup entity="vendor" {...c} value={entry.vendorId} onChange={(e) => onChange({ vendorId: e.target.value })} />}
        </FormField>
        <FormField id={id("part")} label="Vendor part number" optional>
          {(c) => <Input {...c} value={entry.vendorPartNo} onChange={(e) => onChange({ vendorPartNo: e.target.value })} />}
        </FormField>
        <FormField id={id("product")} label="Product" optional error={err("productId")}>
          {(c) => <EntityLookup entity="product" {...c} value={entry.productId} onChange={(e) => onChange({ productId: e.target.value })} />}
        </FormField>
        <FormField id={id("category")} label="Category" error={err("category")}>
          {(c) => <Input {...c} value={entry.category} onChange={(e) => onChange({ category: e.target.value })} />}
        </FormField>
        <FormField id={id("unit")} label="Unit" error={err("unit")}>
          {(c) => <Input {...c} value={entry.unit} onChange={(e) => onChange({ unit: e.target.value })} />}
        </FormField>
        <FormField id={id("price")} label="Unit price" error={err("unitPrice")}>
          {(c) => <Input {...c} inputMode="decimal" value={entry.unitPrice} onChange={(e) => onChange({ unitPrice: e.target.value })} />}
        </FormField>
        <FormField id={id("currency")} label="Currency" error={err("currency")}>
          {(c) => <Input {...c} value={entry.currency} maxLength={3} onChange={(e) => onChange({ currency: e.target.value.toUpperCase() })} />}
        </FormField>
        <FormField id={id("lead")} label="Lead time (days)" optional error={err("leadTimeDays")}>
          {(c) => <Input {...c} inputMode="numeric" value={entry.leadTimeDays} onChange={(e) => onChange({ leadTimeDays: e.target.value })} />}
        </FormField>
        <FormField id={id("image")} label="Image link" optional hint="An http(s) link to the item picture" error={err("imageUrl")}>
          {(c) => <Input {...c} type="url" value={entry.imageUrl} onChange={(e) => onChange({ imageUrl: e.target.value })} />}
        </FormField>
      </div>
      <div className={styles.checkboxRow}>
        <Input id={id("preferred")} type="checkbox" checked={entry.preferred} onChange={(e) => onChange({ preferred: e.target.checked })} />
        <label htmlFor={id("preferred")}>Preferred item</label>
      </div>
    </>
  );
}

const CATALOG_CREATE_FIELDS: Record<string, FormFieldTarget> = {
  legalEntityId: { id: "cat-legal-entity", label: "Legal entity" },
  name: { id: "cat-name", label: "Name" },
  validFrom: { id: "cat-valid-from", label: "Valid from" },
  validTo: { id: "cat-valid-to", label: "Valid to" },
  orgUnitIds: { id: "cat-units", label: "Operating unit ids" },
};

/** New catalog: header, validity, operating units and optional first entries (FR-17.18). */
export function CatalogCreatePage() {
  const navigate = useNavigate();
  const [header, setHeader] = useState({ legalEntityId: "", name: "", description: "", validFrom: todayIso(), validTo: "", orgUnits: "" });
  const [entries, setEntries] = useState<EntryDraft[]>([]);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useMemo(() => newIdempotencyKey(), [header, entries]);
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([header, entries]));
  const setH = (patch: Partial<typeof header>) => setHeader((h) => ({ ...h, ...patch }));
  const units = header.orgUnits.split(/[\s,]+/).filter((u) => u !== "");

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found: Errors = {};
    if (!uuidPattern.test(header.legalEntityId.trim())) found.legalEntityId = "Enter the legal entity id (a UUID).";
    if (header.name.trim() === "") found.name = "Enter a name.";
    if (!datePattern.test(header.validFrom)) found.validFrom = "Enter a date.";
    if (header.validTo !== "" && (!datePattern.test(header.validTo) || header.validTo < header.validFrom)) {
      found.validTo = "Enter a date on or after valid from.";
    }
    if (units.some((u) => !uuidPattern.test(u))) found.orgUnitIds = "Enter operating unit ids (UUIDs) separated by spaces or commas.";
    entries.forEach((e, i) => Object.assign(found, validateEntry(e, `entries[${i}]`)));
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const input: CatalogInput = {
      legalEntityId: header.legalEntityId.trim(),
      name: header.name.trim(),
      description: header.description.trim() || undefined,
      validFrom: header.validFrom,
      validTo: header.validTo || undefined,
      orgUnitIds: units.length > 0 ? units : undefined,
      entries: entries.length > 0 ? entries.map(toEntryInput) : undefined,
    };
    setBusy(true);
    try {
      const { body } = await createCatalog(input, key);
      release();
      navigate(`/purchasing/catalogs/${body.id}`);
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
        title="New procurement catalog"
        description="A catalog starts as a draft. Add items, then publish it so requesters can pick from it."
      />
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <FormErrorSummary items={summaryItems(errors, CATALOG_CREATE_FIELDS)} attempt={attempt} />
        <Card title="Catalog">
          <div className={styles.grid}>
            <FormField id="cat-legal-entity" label="Legal entity" error={errors.legalEntityId}>
              {(c) => <EntityLookup entity="legalEntity" {...c} value={header.legalEntityId} onChange={(e) => setH({ legalEntityId: e.target.value })} />}
            </FormField>
            <FormField id="cat-name" label="Name" error={errors.name}>
              {(c) => <Input {...c} value={header.name} onChange={(e) => setH({ name: e.target.value })} />}
            </FormField>
            <FormField id="cat-description" label="Description" optional>
              {(c) => <Input {...c} value={header.description} onChange={(e) => setH({ description: e.target.value })} />}
            </FormField>
            <FormField id="cat-valid-from" label="Valid from" error={errors.validFrom}>
              {(c) => <Input {...c} type="date" value={header.validFrom} onChange={(e) => setH({ validFrom: e.target.value })} />}
            </FormField>
            <FormField id="cat-valid-to" label="Valid to" optional hint="Blank is open-ended" error={errors.validTo}>
              {(c) => <Input {...c} type="date" value={header.validTo} onChange={(e) => setH({ validTo: e.target.value })} />}
            </FormField>
            <FormSection collapse="more">
              <FormField id="cat-units" label="Operating unit ids" optional hint="Blank publishes to every unit" error={errors.orgUnitIds}>
                {(c) => <Input {...c} value={header.orgUnits} onChange={(e) => setH({ orgUnits: e.target.value })} />}
              </FormField>
            </FormSection>
          </div>
        </Card>
        <Card
          title="Items"
          actions={
            <Button size="sm" onClick={() => setEntries((es) => [...es, emptyEntry(es[es.length - 1]?.currency)])}>
              Add item
            </Button>
          }
        >
          {entries.length === 0 ? <p className={styles.muted}>No items yet. You can also add them after the catalog is saved.</p> : null}
          {entries.map((entry, i) => (
            <fieldset key={entry.key} className={styles.lineCard} aria-label={`Item ${i + 1}`}>
              <div className={styles.lineHead}>
                <p className={styles.lineTitle}>Item {i + 1}</p>
                <Button size="sm" variant="ghost" onClick={() => setEntries((es) => es.filter((_, j) => j !== i))}>
                  Remove item {i + 1}
                </Button>
              </div>
              <EntryFields
                entry={entry}
                prefix={`entries[${i}]`}
                errors={errors}
                onChange={(patch) => setEntries((es) => es.map((x, j) => (j === i ? { ...x, ...patch } : x)))}
              />
            </fieldset>
          ))}
        </Card>
        <Alert tone="danger" live="alert">
          {failure ? <ProblemMessage problem={failure} /> : null}
        </Alert>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save catalog
          </Button>
          <Link className={buttonClassName("ghost")} to="/purchasing/catalogs">
            Cancel
          </Link>
        </div>
      </form>
      {prompt}
    </div>
  );
}

/** The entry picture, or a dash when it has none; the alt text is the item name. */
const thumbnail = (e: { imageUrl?: string; name: string }) =>
  e.imageUrl ? <img className={styles.thumbnail} src={e.imageUrl} alt={e.name} loading="lazy" /> : "—";

const entryColumns = (remove?: (e: CatalogEntry) => void, busyId?: string | null): DataTableColumn<CatalogEntry>[] => [
  { key: "entryNo", header: "No.", align: "end" },
  { key: "image", header: "Image", render: thumbnail },
  { key: "name", header: "Item", render: (e) => (e.preferred ? `${e.name} (preferred)` : e.name) },
  { key: "category", header: "Category" },
  { key: "vendorId", header: "Vendor", render: (e) => <Named entity="vendor" id={e.vendorId} /> },
  { key: "unit", header: "Unit" },
  { key: "unitPrice", header: "Price", align: "end", render: (e) => money(e.unitPrice, e.currency) },
  { key: "leadTimeDays", header: "Lead time", align: "end", render: (e) => `${e.leadTimeDays ?? 0} d` },
  ...(remove
    ? [
        {
          key: "remove",
          header: "Remove",
          render: (e: CatalogEntry) => (
            <Button size="sm" variant="ghost" loading={busyId === e.id} onClick={() => remove(e)}>
              Remove {e.name}
            </Button>
          ),
        },
      ]
    : []),
];

/** One catalog: summary, entries, publish or retire, and (while DRAFT) add or remove entries. */
export function CatalogDetailPage() {
  const { id = "" } = useParams();
  const resource = useResource((signal) => getCatalog(id, signal), [id]);
  const [changed, setChanged] = useState<Catalog | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [draft, setDraft] = useState<EntryDraft>(() => emptyEntry());
  const [errors, setErrors] = useState<Errors>({});
  const catalog = changed ?? resource.data?.body ?? null;
  const actionKey = useMemo(() => newIdempotencyKey(), [catalog?.version]);
  // Entries past the first page, read for the catalog version they were loaded at.
  const [more, setMore] = useState<{ version: number; items: CatalogEntry[]; next?: string } | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const extra = more && catalog && more.version === catalog.version ? more : null;
  const nextCursor = extra ? extra.next : catalog?.entriesNextCursor;

  async function loadMore() {
    if (!catalog || !nextCursor) return;
    setLoadingMore(true);
    setFailure(null);
    try {
      const page = await listCatalogEntries(catalog.id, { cursor: nextCursor, limit: 200 });
      setMore({ version: catalog.version, items: [...(extra?.items ?? []), ...page.items], next: page.nextCursor });
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  async function run(label: string, call: (c: Catalog) => Promise<{ body: Catalog }>, done: (c: Catalog) => string) {
    if (!catalog) return;
    setBusy(label);
    setFailure(null);
    setNotice(null);
    try {
      const { body } = await call(catalog);
      setChanged(body);
      setNotice(done(body));
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const act = (action: CatalogAction) =>
    run(
      action,
      (c) => actOnCatalog(c.id, action, c.version, `${actionKey}-${action}`),
      (c) => `The catalog is now ${statusLabel(c.status).toLowerCase()}.`,
    );

  async function addEntry(event: FormEvent) {
    event.preventDefault();
    const found = validateEntry(draft, "entry");
    setErrors(found);
    if (Object.keys(found).length > 0 || !catalog) return;
    await run(
      "add",
      (c) => addCatalogEntries(c.id, [toEntryInput(draft)], c.version, `${actionKey}-add-${draft.key}`),
      () => `Added ${draft.name.trim()}.`,
    );
    setDraft((d) => emptyEntry(d.currency));
  }

  if (resource.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Procurement catalog" />
        {resource.error.code === "PRC-CAT-NOT-FOUND" ? (
          <Card>
            <EmptyState
              icon="receipt"
              title="Catalog not found"
              description="It may belong to another legal entity, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/purchasing/catalogs">
                  Back to catalogs
                </Link>
              }
            />
          </Card>
        ) : (
          <LoadError what="the catalog" error={resource.error} onRetry={resource.reload} />
        )}
      </div>
    );
  }
  if (!catalog) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Procurement catalog" />
        <p className={styles.muted} role="status">
          Loading catalog…
        </p>
      </div>
    );
  }

  const isDraft = catalog.status === "DRAFT";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Catalog ${catalog.number}`}
        description={catalog.name}
        meta={<StatusBadge status={catalog.status} />}
        actions={
          <>
            {catalog.status !== "RETIRED" ? (
              <Link className={buttonClassName("secondary")} to={`/purchasing/catalogs/${catalog.id}/import`}>
                Import vendor file
              </Link>
            ) : null}
            {isDraft ? (
              <Button variant="primary" loading={busy === "publish"} disabled={busy !== null && busy !== "publish"} onClick={() => void act("publish")}>
                Publish
              </Button>
            ) : catalog.status === "PUBLISHED" ? (
              <Button variant="secondary" loading={busy === "retire"} disabled={busy !== null && busy !== "retire"} onClick={() => void act("retire")}>
                Retire
              </Button>
            ) : null}
          </>
        }
      />
      <Alert tone="danger" live="alert">
        {failure ? <ProblemMessage problem={failure} /> : null}
      </Alert>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      <Card title="Summary">
        <Details
          items={[
            { label: "Legal entity", value: <Named entity="legalEntity" id={catalog.legalEntityId} /> },
            { label: "Validity", value: validity(catalog), mono: true },
            { label: "Published to", value: catalog.orgUnitIds.length === 0 ? "Every operating unit" : catalog.orgUnitIds.join(", "), mono: catalog.orgUnitIds.length > 0 },
            { label: "Description", value: catalog.description ?? "" },
            { label: "Items", value: String(catalog.entryCount), mono: true },
            { label: "Version", value: String(catalog.version), mono: true },
          ]}
        />
      </Card>
      <DataTable
        label="Catalog items"
        columns={entryColumns(
          isDraft
            ? (e) =>
                void run(
                  e.id,
                  (c) => removeCatalogEntry(c.id, e.id, c.version),
                  () => `Removed ${e.name}.`,
                )
            : undefined,
          busy,
        )}
        rows={extra ? [...catalog.entries, ...extra.items] : catalog.entries}
        getRowKey={(e) => e.id}
        empty={{ title: "No items yet", description: isDraft ? "Add the first item below, then publish the catalog." : "This catalog has no items." }}
      />
      {nextCursor ? (
        <div className={styles.actions}>
          <Button loading={loadingMore} onClick={() => void loadMore()}>
            Show more items
          </Button>
        </div>
      ) : null}
      {isDraft ? (
        <form className={styles.form} onSubmit={addEntry} noValidate>
          <Card title="Add an item">
            <EntryFields entry={draft} prefix="entry" errors={errors} onChange={(patch) => setDraft((d) => ({ ...d, ...patch }))} />
            <div className={styles.actions}>
              <Button type="submit" loading={busy === "add"}>
                Add item
              </Button>
            </div>
          </Card>
        </form>
      ) : null}
    </div>
  );
}

const hitColumns = (onPick: (h: CatalogHit) => void): DataTableColumn<CatalogHit>[] => [
  { key: "image", header: "Image", render: thumbnail },
  { key: "name", header: "Item", render: (h) => (h.preferred ? `${h.name} (preferred)` : h.name) },
  { key: "category", header: "Category" },
  { key: "vendorId", header: "Vendor", render: (h) => <Named entity="vendor" id={h.vendorId} /> },
  { key: "unitPrice", header: "Price", align: "end", render: (h) => `${money(h.unitPrice, h.currency)} per ${h.unit}` },
  { key: "leadTimeDays", header: "Lead time", align: "end", render: (h) => `${h.leadTimeDays ?? 0} d` },
  {
    key: "pick",
    header: "Add",
    render: (h) => (
      <Button size="sm" onClick={() => onPick(h)}>
        Add {h.name}
      </Button>
    ),
  },
];

/**
 * Requisition picker (FR-17.18): searches the catalogs published today to the
 * legal entity and operating unit, and hands the chosen entry to the form.
 */
export function CatalogPicker({
  legalEntityId,
  operatingUnitId,
  onPick,
}: {
  legalEntityId: string;
  operatingUnitId: string;
  onPick: (hit: CatalogHit) => void;
}) {
  const [text, setText] = useState("");
  const [submitted, setSubmitted] = useState<string | null>(null);
  const ready = uuidPattern.test(legalEntityId) && (operatingUnitId === "" || uuidPattern.test(operatingUnitId));
  const results = useResource(
    async (signal) => {
      if (submitted === null || !ready) return null;
      return searchCatalogEntries({ legalEntityId, operatingUnitId: operatingUnitId || undefined, q: submitted }, { signal });
    },
    [submitted, legalEntityId, operatingUnitId, ready],
  );
  const rows = results.data?.items ?? [];

  return (
    <Card title="Pick from a catalog">
      <form
        className={styles.form}
        onSubmit={(e) => {
          e.preventDefault();
          setSubmitted(text.trim());
        }}
        noValidate
      >
        <div className={styles.toolbar}>
          <div className={styles.filter}>
            <FormField id="catalog-search" label="Search items" hint={ready ? undefined : "Enter the legal entity id first"}>
              {(c) => <Input {...c} value={text} onChange={(e) => setText(e.target.value)} />}
            </FormField>
          </div>
          <Button type="submit" disabled={!ready}>
            Search catalogs
          </Button>
        </div>
      </form>
      {results.error ? <LoadError what="catalog items" error={results.error} onRetry={results.reload} /> : null}
      {submitted !== null && ready && !results.error ? (
        <DataTable
          label="Catalog search results"
          columns={hitColumns(onPick)}
          rows={rows}
          getRowKey={(h) => h.id}
          loading={results.loading && !results.data}
          empty={{ title: "No catalog items match", description: "Try another word, or add a non-catalog line below." }}
        />
      ) : null}
    </Card>
  );
}
