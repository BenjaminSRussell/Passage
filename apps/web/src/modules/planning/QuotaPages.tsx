import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  type DataTableColumn,
} from "../../shell/ui";
import {
  createQuotaArrangement,
  getQuotaArrangement,
  listQuotaArrangements,
  updateQuotaArrangement,
  type QuotaArrangement,
  type QuotaArrangementInput,
  type QuotaSource,
  type QuotaSourceInput,
  type QuotaSourceType,
} from "./api/client";
import { describeError, useLoad, type LoadError } from "./hooks";
import { compareQty } from "./ItemCoveragePage";
import { trimQty } from "./NetRequirementsPage";
import styles from "./pages.module.css";

const LIST = "/planning/quota-arrangements";
const UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
/** numeric(18,6), never negative. */
const QTY = /^\d{1,12}(\.\d{1,6})?$/;
/** numeric(9,4), never negative. */
const QUOTA = /^\d{1,5}(\.\d{1,4})?$/;
const MAX_SOURCES = 100;
const dash = "—";

export const sourceTypeLabel: Record<QuotaSourceType, string> = {
  vendor: "Vendor",
  production: "Own production",
  transfer: "Transfer",
};

const refField: Record<QuotaSourceType, "vendorId" | "productionVersionId" | "fromWarehouseId"> = {
  vendor: "vendorId",
  production: "productionVersionId",
  transfer: "fromWarehouseId",
};

const refLabel: Record<QuotaSourceType, string> = {
  vendor: "Vendor",
  production: "Production version",
  transfer: "From warehouse",
};

/** The reference that names a stored source within its type. */
export const sourceRef = (s: Pick<QuotaSource, "type" | "vendorId" | "productionVersionId" | "fromWarehouseId">) => s[refField[s.type]] ?? "";

/**
 * The source the next requirement goes to without split (BR-22.12): the lowest
 * rating among unblocked sources with a quota, ties by priority then position;
 * null when there is none (the quota-0 fallback then applies).
 */
export function nextSource(sources: QuotaSource[]): QuotaSource | null {
  let best: QuotaSource | null = null;
  for (const s of sources) {
    if (s.blocked || s.rating === null) continue;
    if (!best || best.rating === null) {
      best = s;
      continue;
    }
    const c = compareQty(s.rating, best.rating);
    if (c < 0 || (c === 0 && s.priority < best.priority)) best = s;
  }
  return best;
}

/** One source row as typed. */
export interface SourceDraft {
  type: QuotaSourceType;
  ref: string;
  quota: string;
  baseQty: string;
  allocatedQty: string;
  maxQtyPerPeriod: string;
  periodDays: string;
  priority: string;
  blocked: boolean;
}

export interface QuotaDraft {
  itemId: string;
  siteId: string;
  validFrom: string;
  validTo: string;
  split: boolean;
  minSplitQty: string;
  sources: SourceDraft[];
}

export const EMPTY_SOURCE: SourceDraft = {
  type: "vendor",
  ref: "",
  quota: "",
  baseQty: "0",
  allocatedQty: "0",
  maxQtyPerPeriod: "",
  periodDays: "",
  priority: "0",
  blocked: false,
};

export const EMPTY_QUOTA: QuotaDraft = { itemId: "", siteId: "", validFrom: "", validTo: "", split: false, minSplitQty: "0", sources: [EMPTY_SOURCE] };

export function quotaDraftOf(a: QuotaArrangement): QuotaDraft {
  return {
    itemId: a.itemId,
    siteId: a.siteId,
    validFrom: a.validFrom,
    validTo: a.validTo,
    split: a.split,
    minSplitQty: trimQty(a.minSplitQty),
    sources: a.sources.map((s) => ({
      type: s.type,
      ref: sourceRef(s),
      quota: trimQty(s.quota),
      baseQty: trimQty(s.baseQty),
      allocatedQty: trimQty(s.allocatedQty),
      maxQtyPerPeriod: s.maxQtyPerPeriod === null ? "" : trimQty(s.maxQtyPerPeriod),
      periodDays: s.periodDays === null ? "" : String(s.periodDays),
      priority: String(s.priority),
      blocked: s.blocked,
    })),
  };
}

/** Field errors: top-level keys, and per source `sources.<i>.<field>`. */
export type QuotaErrors = Record<string, string>;

/** Checks a draft with the service's rules (FR-22.17 data model invariants) and returns the request body or the field errors. */
export function validateQuota(d: QuotaDraft): { errors: QuotaErrors; input: QuotaArrangementInput | null } {
  const errors: QuotaErrors = {};
  const id = (v: string) => v.trim().toLowerCase();
  if (!UUID.test(id(d.itemId))) errors.itemId = "Enter the item id as a UUID.";
  if (!UUID.test(id(d.siteId))) errors.siteId = "Enter the site id as a UUID.";
  if (!DATE.test(d.validFrom)) errors.validFrom = "Enter the first valid day.";
  if (!DATE.test(d.validTo)) errors.validTo = "Enter the last valid day.";
  else if (DATE.test(d.validFrom) && d.validTo < d.validFrom) errors.validTo = "The last day must be on or after the first.";
  if (!QTY.test(d.minSplitQty.trim())) errors.minSplitQty = "Enter a quantity of 0 or more with at most 6 decimals.";
  if (d.sources.length === 0) errors.sources = "Add at least one source.";
  let anyQuota = false;
  const seen = new Map<string, number>();
  const sources: QuotaSourceInput[] = d.sources.map((s, i) => {
    const at = (f: string) => `sources.${i}.${f}`;
    const ref = id(s.ref);
    if (!UUID.test(ref)) errors[at("ref")] = `Enter the ${refLabel[s.type].toLowerCase()} id as a UUID.`;
    else if (seen.has(`${s.type}:${ref}`)) errors[at("ref")] = `Repeats source ${(seen.get(`${s.type}:${ref}`) ?? 0) + 1}.`;
    else seen.set(`${s.type}:${ref}`, i);
    const quota = s.quota.trim();
    if (!QUOTA.test(quota)) errors[at("quota")] = "Enter a quota from 0 to 99999.9999.";
    else if (compareQty(quota, "0") > 0) anyQuota = true;
    if (!QTY.test(s.baseQty.trim())) errors[at("baseQty")] = "Enter 0 or more.";
    if (!QTY.test(s.allocatedQty.trim())) errors[at("allocatedQty")] = "Enter 0 or more.";
    const max = s.maxQtyPerPeriod.trim();
    const period = s.periodDays.trim();
    if ((max === "") !== (period === "")) errors[at("maxQtyPerPeriod")] = "Give the maximum and its period together, or neither.";
    else if (max !== "" && (!QTY.test(max) || compareQty(max, "0") <= 0)) errors[at("maxQtyPerPeriod")] = "Enter a positive quantity.";
    if (period !== "" && (!/^\d+$/.test(period) || Number(period) < 1 || Number(period) > 366)) errors[at("periodDays")] = "Enter 1 to 366 days.";
    if (!/^\d+$/.test(s.priority.trim()) || Number(s.priority) > 999) errors[at("priority")] = "Enter a whole number from 0 to 999.";
    return {
      type: s.type,
      ...(s.type === "vendor" ? { vendorId: ref } : s.type === "production" ? { productionVersionId: ref } : { fromWarehouseId: ref }),
      quota,
      baseQty: s.baseQty.trim(),
      allocatedQty: s.allocatedQty.trim(),
      maxQtyPerPeriod: max === "" ? null : max,
      periodDays: period === "" ? null : Number(period),
      priority: Number(s.priority.trim()),
      blocked: s.blocked,
    };
  });
  if (d.sources.length > 0 && !anyQuota && !Object.keys(errors).some((k) => k.endsWith(".quota"))) {
    errors.sources = "At least one source needs a quota above 0.";
  }
  if (Object.keys(errors).length > 0) return { errors, input: null };
  return {
    errors,
    input: {
      itemId: id(d.itemId),
      siteId: id(d.siteId),
      validFrom: d.validFrom,
      validTo: d.validTo,
      split: d.split,
      minSplitQty: d.minSplitQty.trim(),
      sources,
    },
  };
}

function newKey(): string {
  const c = globalThis.crypto as Crypto | undefined;
  return c?.randomUUID ? c.randomUUID() : `qa-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function LoadAlert({ what, error }: { what: string; error: LoadError | null }) {
  return (
    <Alert tone="danger" title={error ? `Could not load ${what}` : undefined} live="alert">
      {error ? `${error.message} (${error.code})` : null}
    </Alert>
  );
}

function sourceSummary(s: QuotaSource): string {
  return `${sourceTypeLabel[s.type]} ${trimQty(s.quota)}`;
}

const columns: DataTableColumn<QuotaArrangement>[] = [
  { key: "itemId", header: "Item", mono: true },
  { key: "siteId", header: "Site", mono: true },
  { key: "validity", header: "Valid", render: (a) => `${a.validFrom} to ${a.validTo}` },
  { key: "sources", header: "Sources (quota)", render: (a) => a.sources.map(sourceSummary).join(" · ") },
  {
    key: "next",
    header: "Next requirement to",
    render: (a) => {
      const s = nextSource(a.sources);
      return s ? `${sourceTypeLabel[s.type]} ${sourceRef(s)}` : dash;
    },
  },
  { key: "split", header: "Split", render: (a) => (a.split ? <Badge tone="info">Split</Badge> : dash) },
];

/** Quota arrangements (FR-22.17), filtered by item, site and a validity date kept in the query. */
export function QuotaArrangementsPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const pick = (k: string, re: RegExp) => {
    const v = params.get(k) ?? "";
    return re.test(v) ? v.toLowerCase() : "";
  };
  const item = pick("item", UUID);
  const site = pick("site", UUID);
  const validOn = pick("validOn", DATE);
  const [filter, setFilter] = useState({ item, site, validOn });
  const [filterErrors, setFilterErrors] = useState<{ item?: string; site?: string }>({});
  const state = useLoad((signal) => listQuotaArrangements({ itemId: item, siteId: site, validOn }, { signal }), [item, site, validOn]);

  function submit(e: FormEvent) {
    e.preventDefault();
    const found: typeof filterErrors = {};
    const i = filter.item.trim();
    const s = filter.site.trim();
    if (i !== "" && !UUID.test(i)) found.item = "Enter the item id as a UUID, or leave it blank.";
    if (s !== "" && !UUID.test(s)) found.site = "Enter the site id as a UUID, or leave it blank.";
    setFilterErrors(found);
    if (Object.keys(found).length > 0) return;
    const next = new URLSearchParams(params);
    for (const [k, v] of [
      ["item", i.toLowerCase()],
      ["site", s.toLowerCase()],
      ["validOn", filter.validOn],
    ] as const) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    setParams(next);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Quota arrangements"
        description="How an item's net requirements at a site split across vendors, own production and transfers by quota rating (allocated + base) / quota."
        actions={
          <Link className={buttonClassName("primary")} to={`${LIST}/new`}>
            New quota arrangement
          </Link>
        }
      />
      <form className={styles.toolbar} onSubmit={submit} noValidate aria-label="Filter quota arrangements">
        <div className={styles.toolbarFieldWide}>
          <FormField id="qa-filter-item" label="Item" hint="Blank shows every item." error={filterErrors.item}>
            {(a11y) => <Input {...a11y} className={styles.mono} value={filter.item} onChange={(e) => setFilter((f) => ({ ...f, item: e.target.value }))} />}
          </FormField>
        </div>
        <div className={styles.toolbarFieldWide}>
          <FormField id="qa-filter-site" label="Site" error={filterErrors.site}>
            {(a11y) => <Input {...a11y} className={styles.mono} value={filter.site} onChange={(e) => setFilter((f) => ({ ...f, site: e.target.value }))} />}
          </FormField>
        </div>
        <div className={styles.toolbarField}>
          <FormField id="qa-filter-validOn" label="Valid on">
            {(a11y) => <Input {...a11y} type="date" value={filter.validOn} onChange={(e) => setFilter((f) => ({ ...f, validOn: e.target.value }))} />}
          </FormField>
        </div>
        <Button type="submit">Show arrangements</Button>
      </form>
      <LoadAlert what="quota arrangements" error={state.error} />
      {state.error ? null : (
        <DataTable
          label="Quota arrangements"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(a) => a.id}
          loading={state.loading}
          onRowOpen={(a) => navigate(`${LIST}/${encodeURIComponent(a.id)}`)}
          empty={{
            title: item || site || validOn ? "No arrangement matches these filters" : "No quota arrangements yet",
            description: "Without one, planned purchases go to the item coverage's default vendor.",
          }}
        />
      )}
    </div>
  );
}

interface FormProps {
  arrangement?: QuotaArrangement;
  onSaved: (a: QuotaArrangement) => void;
  onReload?: () => void;
}

function QuotaForm({ arrangement, onSaved, onReload }: FormProps) {
  const creating = !arrangement;
  const [draft, setDraft] = useState<QuotaDraft>(() => (arrangement ? quotaDraftOf(arrangement) : EMPTY_QUOTA));
  const [errors, setErrors] = useState<QuotaErrors>({});
  const [failure, setFailure] = useState<LoadError | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  useEffect(() => {
    if (arrangement) {
      setDraft(quotaDraftOf(arrangement));
      setErrors({});
    }
  }, [arrangement?.id, arrangement?.version]);

  const change = (next: (d: QuotaDraft) => QuotaDraft) => {
    setDraft(next);
    key.current = newKey();
  };
  const set = <K extends keyof QuotaDraft>(field: K, value: QuotaDraft[K]) => change((d) => ({ ...d, [field]: value }));
  const setSource = <K extends keyof SourceDraft>(i: number, field: K, value: SourceDraft[K]) =>
    change((d) => ({ ...d, sources: d.sources.map((s, j) => (j === i ? { ...s, [field]: value } : s)) }));

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    const { errors: found, input } = validateQuota(draft);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    setFailure(null);
    try {
      onSaved(arrangement ? await updateQuotaArrangement(arrangement.id, arrangement.version, input) : await createQuotaArrangement(input, key.current));
    } catch (err) {
      setFailure(describeError(err));
    } finally {
      setBusy(false);
    }
  }

  const stale = failure?.status === 412;
  const failureTitle = stale
    ? "Someone else changed this quota arrangement"
    : failure?.code === "PLAN-QUOTA-OVERLAP"
      ? "Another arrangement of this item and site is valid in that period"
      : "The quota arrangement was not saved";

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label={creating ? "New quota arrangement" : "Edit quota arrangement"}>
      <div className={styles.wide}>
        <Alert
          tone="danger"
          title={failure ? failureTitle : undefined}
          live="alert"
          action={
            stale && onReload ? (
              <Button size="sm" onClick={onReload}>
                Reload arrangement
              </Button>
            ) : undefined
          }
        >
          {failure ? `${failure.message} (${failure.code})` : null}
        </Alert>
      </div>
      <FormField id="qa-item" label="Item" error={errors.itemId}>
        {(a11y) => <Input {...a11y} className={styles.mono} value={draft.itemId} onChange={(e) => set("itemId", e.target.value)} />}
      </FormField>
      <FormField id="qa-site" label="Site" error={errors.siteId}>
        {(a11y) => <Input {...a11y} className={styles.mono} value={draft.siteId} onChange={(e) => set("siteId", e.target.value)} />}
      </FormField>
      <FormField id="qa-validFrom" label="Valid from" error={errors.validFrom}>
        {(a11y) => <Input {...a11y} type="date" value={draft.validFrom} onChange={(e) => set("validFrom", e.target.value)} />}
      </FormField>
      <FormField id="qa-validTo" label="Valid to" hint="Inclusive." error={errors.validTo}>
        {(a11y) => <Input {...a11y} type="date" value={draft.validTo} onChange={(e) => set("validTo", e.target.value)} />}
      </FormField>
      <FormField id="qa-minSplit" label="Minimum split quantity" hint="With split on, smaller shares are dropped." error={errors.minSplitQty}>
        {(a11y) => <Input {...a11y} inputMode="decimal" value={draft.minSplitQty} onChange={(e) => set("minSplitQty", e.target.value)} />}
      </FormField>
      <div className={styles.checks}>
        <span className={styles.check}>
          <Input id="qa-split" type="checkbox" checked={draft.split} onChange={(e) => set("split", e.target.checked)} />
          <label htmlFor="qa-split">Split one requirement across sources</label>
        </span>
      </div>
      {draft.sources.map((s, i) => (
        <fieldset key={i} className={`${styles.fieldset} ${styles.wide}`}>
          <legend className={styles.legend}>Source {i + 1}</legend>
          <div className={styles.form}>
            <FormField id={`qa-s${i}-type`} label="Source type">
              {(a11y) => (
                <Select {...a11y} value={s.type} onChange={(e) => setSource(i, "type", e.target.value as QuotaSourceType)}>
                  {(Object.keys(sourceTypeLabel) as QuotaSourceType[]).map((t) => (
                    <option key={t} value={t}>
                      {sourceTypeLabel[t]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id={`qa-s${i}-ref`} label={refLabel[s.type]} error={errors[`sources.${i}.ref`]}>
              {(a11y) => <Input {...a11y} className={styles.mono} value={s.ref} onChange={(e) => setSource(i, "ref", e.target.value)} />}
            </FormField>
            <FormField id={`qa-s${i}-quota`} label="Quota" hint="0 is a fallback when the others are blocked." error={errors[`sources.${i}.quota`]}>
              {(a11y) => <Input {...a11y} inputMode="decimal" value={s.quota} onChange={(e) => setSource(i, "quota", e.target.value)} />}
            </FormField>
            <FormField id={`qa-s${i}-allocated`} label="Allocated quantity" hint="Carried over or booked by orders." error={errors[`sources.${i}.allocatedQty`]}>
              {(a11y) => <Input {...a11y} inputMode="decimal" value={s.allocatedQty} onChange={(e) => setSource(i, "allocatedQty", e.target.value)} />}
            </FormField>
            <FormField id={`qa-s${i}-base`} label="Base quantity" error={errors[`sources.${i}.baseQty`]}>
              {(a11y) => <Input {...a11y} inputMode="decimal" value={s.baseQty} onChange={(e) => setSource(i, "baseQty", e.target.value)} />}
            </FormField>
            <FormField id={`qa-s${i}-priority`} label="Priority" hint="Breaks rating ties; lower first." error={errors[`sources.${i}.priority`]}>
              {(a11y) => <Input {...a11y} inputMode="numeric" value={s.priority} onChange={(e) => setSource(i, "priority", e.target.value)} />}
            </FormField>
            <FormField id={`qa-s${i}-max`} label="Maximum per period" optional error={errors[`sources.${i}.maxQtyPerPeriod`]}>
              {(a11y) => <Input {...a11y} inputMode="decimal" value={s.maxQtyPerPeriod} onChange={(e) => setSource(i, "maxQtyPerPeriod", e.target.value)} />}
            </FormField>
            <FormField id={`qa-s${i}-period`} label="Period (days)" optional error={errors[`sources.${i}.periodDays`]}>
              {(a11y) => <Input {...a11y} inputMode="numeric" value={s.periodDays} onChange={(e) => setSource(i, "periodDays", e.target.value)} />}
            </FormField>
            <div className={styles.inlineActions}>
              <span className={styles.check}>
                <Input id={`qa-s${i}-blocked`} type="checkbox" checked={s.blocked} onChange={(e) => setSource(i, "blocked", e.target.checked)} />
                <label htmlFor={`qa-s${i}-blocked`}>Blocked</label>
              </span>
              <Button
                size="sm"
                variant="ghost"
                disabled={draft.sources.length === 1}
                onClick={() => change((d) => ({ ...d, sources: d.sources.filter((_, j) => j !== i) }))}
              >
                Remove source {i + 1}
              </Button>
            </div>
          </div>
        </fieldset>
      ))}
      <div className={`${styles.wide} ${styles.inlineActions}`}>
        <Button
          size="sm"
          disabled={draft.sources.length >= MAX_SOURCES}
          onClick={() => change((d) => ({ ...d, sources: [...d.sources, EMPTY_SOURCE] }))}
        >
          Add source
        </Button>
        {errors.sources ? <p className={styles.formError}>{errors.sources}</p> : null}
      </div>
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          {creating ? "Create quota arrangement" : "Save changes"}
        </Button>
        <Link className={buttonClassName("secondary")} to={LIST}>
          Cancel
        </Link>
      </div>
    </form>
  );
}

export function NewQuotaArrangementPage() {
  const navigate = useNavigate();
  return (
    <div className={styles.stack}>
      <PageHeader title="New quota arrangement" description="Validity must not overlap another arrangement of the same item and site." />
      <Card>
        <QuotaForm onSaved={(a) => navigate(`${LIST}/${encodeURIComponent(a.id)}`)} />
      </Card>
    </div>
  );
}

const sourceColumns: DataTableColumn<QuotaSource>[] = [
  { key: "type", header: "Source", render: (s) => sourceTypeLabel[s.type] },
  { key: "ref", header: "Reference", mono: true, render: sourceRef },
  { key: "quota", header: "Quota", align: "end", render: (s) => trimQty(s.quota) },
  { key: "allocated", header: "Allocated", align: "end", render: (s) => trimQty(s.allocatedQty) },
  { key: "base", header: "Base", align: "end", render: (s) => trimQty(s.baseQty) },
  { key: "rating", header: "Rating", align: "end", render: (s) => (s.rating === null ? "Fallback" : trimQty(s.rating)) },
  { key: "priority", header: "Priority", align: "end", render: (s) => String(s.priority) },
  { key: "blocked", header: "Status", render: (s) => (s.blocked ? <Badge tone="warning">Blocked</Badge> : <Badge tone="success">Open</Badge>) },
];

/** Shows one arrangement with its source ratings and edits it under If-Match on its version. */
export function QuotaArrangementPage() {
  const { id = "" } = useParams();
  const [reload, setReload] = useState(0);
  const state = useLoad((signal) => getQuotaArrangement(id, { signal }), [id, reload]);
  const [saved, setSaved] = useState<QuotaArrangement | null>(null);
  const arrangement = saved && saved.id === id ? saved : state.data;
  const next = arrangement ? nextSource(arrangement.sources) : null;

  useEffect(() => setSaved(null), [state.data]);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Quota arrangement"
        description={arrangement ? `Item ${arrangement.itemId} at site ${arrangement.siteId}, valid ${arrangement.validFrom} to ${arrangement.validTo}.` : undefined}
        actions={
          <Link className={buttonClassName("secondary")} to={LIST}>
            All quota arrangements
          </Link>
        }
      />
      <LoadAlert what="the quota arrangement" error={state.error} />
      <Alert tone="success" live="status">
        {saved ? `Saved. Version ${saved.version}.` : null}
      </Alert>
      {state.loading ? (
        <Card>
          <div className={styles.stack} aria-busy="true" aria-label="Loading quota arrangement">
            <Skeleton width="1/2" />
            <Skeleton height="control" />
            <Skeleton height="control" />
          </div>
        </Card>
      ) : null}
      {arrangement ? (
        <>
          <Card title="Sources and ratings">
            <p className={styles.summary}>
              {next
                ? `Without split, the next requirement goes to ${sourceTypeLabel[next.type].toLowerCase()} ${sourceRef(next)} (lowest rating ${trimQty(next.rating ?? "0")}).`
                : "No open source has a quota; a quota-0 fallback takes the requirement."}
            </p>
            <DataTable label="Quota sources" columns={sourceColumns} rows={arrangement.sources} getRowKey={(s) => s.id} empty={{ title: "No sources" }} />
          </Card>
          <Card title="Settings">
            <QuotaForm arrangement={arrangement} onSaved={setSaved} onReload={() => setReload((n) => n + 1)} />
          </Card>
        </>
      ) : null}
    </div>
  );
}
