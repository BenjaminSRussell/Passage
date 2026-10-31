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
  createItemCoverage,
  getItemCoverage,
  listCoverageGroups,
  listItemCoverages,
  updateItemCoverage,
  type CoverageGroup,
  type ItemCoverage,
  type ItemCoverageInput,
  type OrderType,
  type PlanningScope,
  type PlanningStrategy,
} from "./api/client";
import { describeError, useLoad, type LoadError } from "./hooks";
import { trimQty } from "./NetRequirementsPage";
import styles from "./pages.module.css";

const LIST = "/planning/item-coverages";
const UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
/** What numeric(18,6) stores: up to 12 integer digits and 6 decimals, never negative. */
const QTY = /^\d{1,12}(\.\d{1,6})?$/;
const dash = "—";

export const scopeLabel: Record<PlanningScope, string> = {
  site: "Site",
  warehouse: "Warehouse (MRP area)",
  subcontractor: "Subcontractor",
};

export const strategyLabel: Record<PlanningStrategy, string> = {
  "mts-net": "Make to stock, net (10)",
  "mts-gross": "Make to stock, gross (11)",
  mto: "Make to order (20)",
  "assemble-to-order-final": "Assemble to order (40)",
  "plan-without-final-assembly": "Plan without final assembly (50)",
  subassembly: "Subassembly (70)",
};

const orderTypeLabel: Record<OrderType, string> = { purchase: "Purchase", production: "Production", transfer: "Transfer" };

/** Whole-number fields of an item coverage with their label, range and help. */
const DAY_FIELDS = [
  { key: "safetyMarginReceiptDays", label: "Receipt margin (days)", min: 0, max: 365, hint: "Days added before a receipt is available.", optional: false },
  { key: "safetyMarginIssueDays", label: "Issue margin (days)", min: 0, max: 365, hint: "Working days each demand is moved earlier (FR-22.5).", optional: false },
  { key: "safetyMarginReorderDays", label: "Reorder margin (days)", min: 0, max: 365, hint: "Working days added to the lead time of planned orders (FR-22.5).", optional: false },
  { key: "leadTimeDays", label: "Lead time override (days)", min: 0, max: 3650, hint: "Blank uses the item's lead time.", optional: true },
  { key: "requiredShelfLifeDays", label: "Required shelf life (days)", min: 0, max: 3650, hint: "Remaining shelf life a demand needs (FR-22.20).", optional: false },
] as const;

type DayKey = (typeof DAY_FIELDS)[number]["key"];

/** The form as typed: ids, quantities and days stay strings until validated. */
export type ItemCoverageDraft = {
  itemId: string;
  siteId: string;
  coverageGroupId: string;
  planningScope: PlanningScope;
  warehouseId: string;
  vendorId: string;
  bufferProfileId: string;
  minQty: string;
  maxQty: string;
  plannedOrderType: OrderType;
  planningStrategy: PlanningStrategy;
  decouplingPoint: boolean;
  priorityBased: boolean;
} & Record<DayKey, string>;

type DraftErrors = Partial<Record<keyof ItemCoverageDraft, string>>;

export const EMPTY_DRAFT: ItemCoverageDraft = {
  itemId: "",
  siteId: "",
  coverageGroupId: "",
  planningScope: "site",
  warehouseId: "",
  vendorId: "",
  bufferProfileId: "",
  minQty: "0",
  maxQty: "0",
  plannedOrderType: "purchase",
  planningStrategy: "mts-net",
  decouplingPoint: false,
  priorityBased: false,
  safetyMarginReceiptDays: "0",
  safetyMarginIssueDays: "0",
  safetyMarginReorderDays: "0",
  leadTimeDays: "",
  requiredShelfLifeDays: "0",
};

export function draftOf(c: ItemCoverage): ItemCoverageDraft {
  return {
    itemId: c.itemId,
    siteId: c.siteId,
    coverageGroupId: c.coverageGroupId,
    planningScope: c.planningScope,
    warehouseId: c.warehouseId ?? "",
    vendorId: c.vendorId ?? "",
    bufferProfileId: c.bufferProfileId ?? "",
    minQty: trimQty(c.minQty),
    maxQty: trimQty(c.maxQty),
    plannedOrderType: c.plannedOrderType,
    planningStrategy: c.planningStrategy,
    decouplingPoint: c.decouplingPoint,
    priorityBased: c.priorityBased,
    safetyMarginReceiptDays: String(c.safetyMarginReceiptDays),
    safetyMarginIssueDays: String(c.safetyMarginIssueDays),
    safetyMarginReorderDays: String(c.safetyMarginReorderDays),
    leadTimeDays: c.leadTimeDays === null ? "" : String(c.leadTimeDays),
    requiredShelfLifeDays: String(c.requiredShelfLifeDays),
  };
}

/** Compares two non-negative decimal strings without going through a float. */
export function compareQty(a: string, b: string): number {
  const [ai, af = ""] = trimQty(a).split(".");
  const [bi, bf = ""] = trimQty(b).split(".");
  const ia = ai.replace(/^0+(?=\d)/, "");
  const ib = bi.replace(/^0+(?=\d)/, "");
  if (ia.length !== ib.length) return ia.length < ib.length ? -1 : 1;
  const width = Math.max(af.length, bf.length);
  const x = ia + af.padEnd(width, "0");
  const y = ib + bf.padEnd(width, "0");
  return x === y ? 0 : x < y ? -1 : 1;
}

/** Checks a draft with the service's rules (FR-22.18..FR-22.20) and returns the request body or the field errors. */
export function validateItemCoverage(d: ItemCoverageDraft): { errors: DraftErrors; input: ItemCoverageInput | null } {
  const errors: DraftErrors = {};
  const id = (v: string) => v.trim();
  if (!UUID.test(id(d.itemId))) errors.itemId = "Enter the item id as a UUID.";
  if (!UUID.test(id(d.siteId))) errors.siteId = "Enter the site id as a UUID.";
  if (!UUID.test(d.coverageGroupId)) errors.coverageGroupId = "Choose a coverage group.";
  if (d.planningScope === "warehouse" && !UUID.test(id(d.warehouseId))) errors.warehouseId = "A warehouse scope needs the warehouse id as a UUID.";
  if (d.planningScope === "subcontractor" && !UUID.test(id(d.vendorId))) errors.vendorId = "A subcontractor scope needs the vendor id as a UUID.";
  else if (id(d.vendorId) !== "" && !UUID.test(id(d.vendorId))) errors.vendorId = "Enter the vendor id as a UUID or leave it blank.";
  if (d.planningScope === "subcontractor" && d.plannedOrderType !== "transfer") {
    errors.plannedOrderType = "A subcontractor scope plans provision transfers.";
  }
  if (id(d.bufferProfileId) !== "" && !UUID.test(id(d.bufferProfileId))) errors.bufferProfileId = "Enter the buffer profile id as a UUID or leave it blank.";
  const minQty = d.minQty.trim();
  const maxQty = d.maxQty.trim();
  if (!QTY.test(minQty)) errors.minQty = "Enter a quantity of 0 or more with at most 6 decimals.";
  if (!QTY.test(maxQty)) errors.maxQty = "Enter a quantity of 0 or more with at most 6 decimals.";
  else if (!errors.minQty && compareQty(maxQty, "0") > 0 && compareQty(maxQty, minQty) < 0) {
    errors.maxQty = "Use 0 for no maximum, or at least the minimum.";
  }
  const days = {} as Record<DayKey, number | null>;
  for (const f of DAY_FIELDS) {
    const raw = d[f.key].trim();
    if (raw === "" && f.optional) {
      days[f.key] = null;
      continue;
    }
    const n = Number(raw);
    if (!/^\d+$/.test(raw) || n < f.min || n > f.max) errors[f.key] = `Enter a whole number from ${f.min} to ${f.max}.`;
    else days[f.key] = n;
  }
  if (Object.keys(errors).length > 0) return { errors, input: null };
  const optional = (v: string) => (id(v) === "" ? null : id(v).toLowerCase());
  return {
    errors,
    input: {
      itemId: id(d.itemId).toLowerCase(),
      siteId: id(d.siteId).toLowerCase(),
      coverageGroupId: d.coverageGroupId,
      planningScope: d.planningScope,
      warehouseId: d.planningScope === "warehouse" ? optional(d.warehouseId) : null,
      vendorId: optional(d.vendorId),
      bufferProfileId: optional(d.bufferProfileId),
      minQty,
      maxQty,
      plannedOrderType: d.plannedOrderType,
      planningStrategy: d.planningStrategy,
      decouplingPoint: d.decouplingPoint,
      priorityBased: d.priorityBased,
      safetyMarginReceiptDays: days.safetyMarginReceiptDays ?? 0,
      safetyMarginIssueDays: days.safetyMarginIssueDays ?? 0,
      safetyMarginReorderDays: days.safetyMarginReorderDays ?? 0,
      leadTimeDays: days.leadTimeDays,
      requiredShelfLifeDays: days.requiredShelfLifeDays ?? 0,
    },
  };
}

function newKey(): string {
  const c = globalThis.crypto as Crypto | undefined;
  return c?.randomUUID ? c.randomUUID() : `ic-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function scopeText(c: ItemCoverage): string {
  if (c.planningScope === "warehouse") return `Warehouse ${c.warehouseId ?? ""}`;
  if (c.planningScope === "subcontractor") return `Subcontractor ${c.vendorId ?? ""}`;
  return scopeLabel.site;
}

const columns: DataTableColumn<ItemCoverage>[] = [
  { key: "itemId", header: "Item", mono: true },
  { key: "siteId", header: "Site", mono: true },
  { key: "scope", header: "Scope", render: scopeText },
  { key: "group", header: "Coverage group", mono: true, render: (c) => c.coverageGroupCode },
  { key: "min", header: "Min", align: "end", render: (c) => trimQty(c.minQty) },
  { key: "max", header: "Max", align: "end", render: (c) => (trimQty(c.maxQty) === "0" ? "No maximum" : trimQty(c.maxQty)) },
  { key: "orderType", header: "Order type", render: (c) => orderTypeLabel[c.plannedOrderType] },
  { key: "strategy", header: "Strategy", render: (c) => strategyLabel[c.planningStrategy] },
  {
    key: "flags",
    header: "DDMRP / priority",
    render: (c) =>
      c.decouplingPoint || c.priorityBased ? (
        <span className={styles.inlineActions}>
          {c.decouplingPoint ? <Badge tone="info">Decoupling point</Badge> : null}
          {c.priorityBased ? <Badge tone="neutral">Priority-based</Badge> : null}
        </span>
      ) : (
        dash
      ),
  },
];

function LoadAlert({ what, error }: { what: string; error: LoadError | null }) {
  return (
    <Alert tone="danger" title={error ? `Could not load ${what}` : undefined} live="alert">
      {error ? `${error.message} (${error.code})` : null}
    </Alert>
  );
}

/** Item coverage setup (4.22.4, FR-22.18..FR-22.20): the tenant's coverages, optionally of one item kept in `?item=`. */
export function ItemCoveragesPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const raw = params.get("item") ?? "";
  const item = UUID.test(raw) ? raw.toLowerCase() : "";
  const [filter, setFilter] = useState(item);
  const [filterError, setFilterError] = useState<string | undefined>();
  const state = useLoad((signal) => listItemCoverages(item, { signal }), [item]);

  function submit(e: FormEvent) {
    e.preventDefault();
    const id = filter.trim();
    if (id !== "" && !UUID.test(id)) {
      setFilterError("Enter the item id as a UUID, or leave it blank for all items.");
      return;
    }
    setFilterError(undefined);
    const next = new URLSearchParams(params);
    if (id === "") next.delete("item");
    else next.set("item", id.toLowerCase());
    setParams(next);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Item coverage"
        description="How each item is planned per site, warehouse or subcontractor: coverage group, min/max, lead time, planning strategy and DDMRP position."
        actions={
          <Link className={buttonClassName("primary")} to={`${LIST}/new`}>
            New item coverage
          </Link>
        }
      />
      <form className={styles.toolbar} onSubmit={submit} noValidate aria-label="Filter item coverage">
        <div className={styles.toolbarFieldWide}>
          <FormField id="ic-filter" label="Item" hint="Blank shows every item." error={filterError}>
            {(a11y) => <Input {...a11y} className={styles.mono} value={filter} onChange={(e) => setFilter(e.target.value)} />}
          </FormField>
        </div>
        <Button type="submit">Show coverage</Button>
      </form>
      <LoadAlert what="item coverage" error={state.error} />
      {state.error ? null : (
        <DataTable
          label="Item coverage"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(c) => c.id}
          loading={state.loading}
          onRowOpen={(c) => navigate(`${LIST}/${encodeURIComponent(c.id)}`)}
          empty={{
            title: item ? "This item has no coverage yet" : "No item coverage yet",
            description: "Without one, the run reads coverage from its input. Create one to set how an item is planned.",
          }}
        />
      )}
    </div>
  );
}

interface FormProps {
  /** The coverage being edited; absent when creating. */
  coverage?: ItemCoverage;
  onSaved: (c: ItemCoverage) => void;
  onReload?: () => void;
}

function ItemCoverageForm({ coverage, onSaved, onReload }: FormProps) {
  const creating = !coverage;
  const [draft, setDraft] = useState<ItemCoverageDraft>(() => (coverage ? draftOf(coverage) : EMPTY_DRAFT));
  const [errors, setErrors] = useState<DraftErrors>({});
  const [failure, setFailure] = useState<LoadError | null>(null);
  const [busy, setBusy] = useState(false);
  const groups = useLoad((signal) => listCoverageGroups({ signal }), []);
  // One key per create attempt, kept across retries of the same draft.
  const key = useRef(newKey());

  useEffect(() => {
    if (coverage) {
      setDraft(draftOf(coverage));
      setErrors({});
    }
  }, [coverage?.id, coverage?.version]);

  const set = <K extends keyof ItemCoverageDraft>(field: K, value: ItemCoverageDraft[K]) => {
    setDraft((d) => ({ ...d, [field]: value }));
    key.current = newKey();
  };

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    const { errors: found, input } = validateItemCoverage(draft);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    setFailure(null);
    try {
      const saved = coverage ? await updateItemCoverage(coverage.id, coverage.version, input) : await createItemCoverage(input, key.current);
      onSaved(saved);
    } catch (err) {
      setFailure(describeError(err));
    } finally {
      setBusy(false);
    }
  }

  const stale = failure?.status === 412;
  const groupOptions: Pick<CoverageGroup, "id" | "code">[] = [...(groups.data ?? [])];
  // Keep the current group selectable even before the list loads.
  if (coverage && !groupOptions.some((g) => g.id === coverage.coverageGroupId)) {
    groupOptions.unshift({ id: coverage.coverageGroupId, code: coverage.coverageGroupCode });
  }
  const failureTitle = stale
    ? "Someone else changed this item coverage"
    : failure?.code === "PLAN-ITEM-COVERAGE-EXISTS"
      ? "This item already has a coverage for that site and scope"
      : "The item coverage was not saved";

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label={creating ? "New item coverage" : "Edit item coverage"}>
      <div className={styles.wide}>
        <Alert
          tone="danger"
          title={failure ? failureTitle : undefined}
          live="alert"
          action={
            stale && onReload ? (
              <Button size="sm" onClick={onReload}>
                Reload coverage
              </Button>
            ) : undefined
          }
        >
          {failure ? `${failure.message} (${failure.code})` : null}
        </Alert>
        <Alert tone="danger" title={groups.error ? "Could not load coverage groups" : undefined} live="alert">
          {groups.error ? `${groups.error.message} (${groups.error.code})` : null}
        </Alert>
        {!groups.loading && !groups.error && groupOptions.length === 0 ? (
          <Alert tone="info" title="No coverage groups yet" live="status">
            <>
              Item coverage needs a coverage group. <Link to="/planning/coverage-groups/new">Create a coverage group</Link> first.
            </>
          </Alert>
        ) : null}
      </div>
      <FormField id="ic-item" label="Item" error={errors.itemId}>
        {(a11y) => <Input {...a11y} className={styles.mono} value={draft.itemId} onChange={(e) => set("itemId", e.target.value)} />}
      </FormField>
      <FormField id="ic-site" label="Site" error={errors.siteId}>
        {(a11y) => <Input {...a11y} className={styles.mono} value={draft.siteId} onChange={(e) => set("siteId", e.target.value)} />}
      </FormField>
      <FormField id="ic-group" label="Coverage group" error={errors.coverageGroupId}>
        {(a11y) => (
          <Select {...a11y} value={draft.coverageGroupId} disabled={groups.loading && !coverage} onChange={(e) => set("coverageGroupId", e.target.value)}>
            <option value="">{groups.loading ? "Loading groups…" : "Choose a group"}</option>
            {groupOptions.map((g) => (
              <option key={g.id} value={g.id}>
                {g.code}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="ic-scope" label="Planning scope" hint="A warehouse or subcontractor in its own scope is planned apart from the site.">
        {(a11y) => (
          <Select
            {...a11y}
            value={draft.planningScope}
            onChange={(e) => {
              const scope = e.target.value as PlanningScope;
              set("planningScope", scope);
              if (scope === "subcontractor") set("plannedOrderType", "transfer");
            }}
          >
            {(Object.keys(scopeLabel) as PlanningScope[]).map((s) => (
              <option key={s} value={s}>
                {scopeLabel[s]}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      {draft.planningScope === "warehouse" ? (
        <FormField id="ic-warehouse" label="Warehouse" error={errors.warehouseId}>
          {(a11y) => <Input {...a11y} className={styles.mono} value={draft.warehouseId} onChange={(e) => set("warehouseId", e.target.value)} />}
        </FormField>
      ) : null}
      <FormField
        id="ic-vendor"
        label={draft.planningScope === "subcontractor" ? "Subcontractor (vendor)" : "Default vendor"}
        optional={draft.planningScope !== "subcontractor"}
        error={errors.vendorId}
      >
        {(a11y) => <Input {...a11y} className={styles.mono} value={draft.vendorId} onChange={(e) => set("vendorId", e.target.value)} />}
      </FormField>
      <FormField id="ic-orderType" label="Planned order type" error={errors.plannedOrderType}>
        {(a11y) => (
          <Select {...a11y} value={draft.plannedOrderType} onChange={(e) => set("plannedOrderType", e.target.value as OrderType)}>
            {(Object.keys(orderTypeLabel) as OrderType[]).map((t) => (
              <option key={t} value={t}>
                {orderTypeLabel[t]}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="ic-strategy" label="Planning strategy" hint="How forecast and sales demand interact (FR-22.19).">
        {(a11y) => (
          <Select {...a11y} value={draft.planningStrategy} onChange={(e) => set("planningStrategy", e.target.value as PlanningStrategy)}>
            {(Object.keys(strategyLabel) as PlanningStrategy[]).map((s) => (
              <option key={s} value={s}>
                {strategyLabel[s]}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="ic-min" label="Minimum quantity" error={errors.minQty}>
        {(a11y) => <Input {...a11y} inputMode="decimal" value={draft.minQty} onChange={(e) => set("minQty", e.target.value)} />}
      </FormField>
      <FormField id="ic-max" label="Maximum quantity" hint="0 is no maximum." error={errors.maxQty}>
        {(a11y) => <Input {...a11y} inputMode="decimal" value={draft.maxQty} onChange={(e) => set("maxQty", e.target.value)} />}
      </FormField>
      {DAY_FIELDS.map((f) => (
        <FormField key={f.key} id={`ic-${f.key}`} label={f.label} hint={f.hint} optional={f.optional} error={errors[f.key]}>
          {(a11y) => <Input {...a11y} inputMode="numeric" value={draft[f.key]} onChange={(e) => set(f.key, e.target.value)} />}
        </FormField>
      ))}
      <fieldset className={`${styles.fieldset} ${styles.wide}`}>
        <legend className={styles.legend}>DDMRP and priority (FR-22.16)</legend>
        <div className={styles.checks}>
          <span className={styles.check}>
            <Input id="ic-decoupling" type="checkbox" checked={draft.decouplingPoint} onChange={(e) => set("decouplingPoint", e.target.checked)} />
            <label htmlFor="ic-decoupling">Decoupling point (DDMRP buffer)</label>
          </span>
          <span className={styles.check}>
            <Input id="ic-priority" type="checkbox" checked={draft.priorityBased} onChange={(e) => set("priorityBased", e.target.checked)} />
            <label htmlFor="ic-priority">Priority-based planning</label>
          </span>
        </div>
      </fieldset>
      <FormField id="ic-buffer" label="Buffer profile" optional error={errors.bufferProfileId}>
        {(a11y) => <Input {...a11y} className={styles.mono} value={draft.bufferProfileId} onChange={(e) => set("bufferProfileId", e.target.value)} />}
      </FormField>
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          {creating ? "Create item coverage" : "Save changes"}
        </Button>
        <Link className={buttonClassName("secondary")} to={LIST}>
          Cancel
        </Link>
      </div>
    </form>
  );
}

/** Creates an item coverage; the form starts from the defaults (site scope, purchase, mts-net, no lot limits). */
export function NewItemCoveragePage() {
  const navigate = useNavigate();
  return (
    <div className={styles.stack}>
      <PageHeader title="New item coverage" description="Defaults: site scope, purchase orders, make to stock (net), no minimum or maximum." />
      <Card>
        <ItemCoverageForm onSaved={(c) => navigate(`${LIST}/${encodeURIComponent(c.id)}`)} />
      </Card>
    </div>
  );
}

/** Shows and edits one item coverage under If-Match on its version. */
export function ItemCoveragePage() {
  const { id = "" } = useParams();
  const [reload, setReload] = useState(0);
  const state = useLoad((signal) => getItemCoverage(id, { signal }), [id, reload]);
  const [saved, setSaved] = useState<ItemCoverage | null>(null);
  const coverage = saved && saved.id === id ? saved : state.data;

  useEffect(() => setSaved(null), [state.data]);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Item coverage"
        description={coverage ? `${scopeText(coverage)} coverage of item ${coverage.itemId} under group ${coverage.coverageGroupCode}.` : undefined}
        actions={
          <Link className={buttonClassName("secondary")} to={LIST}>
            All item coverage
          </Link>
        }
      />
      <LoadAlert what="the item coverage" error={state.error} />
      <Alert tone="success" live="status">
        {saved ? `Saved. Version ${saved.version}.` : null}
      </Alert>
      {state.loading ? (
        <Card>
          <div className={styles.stack} aria-busy="true" aria-label="Loading item coverage">
            <Skeleton width="1/2" />
            <Skeleton height="control" />
            <Skeleton height="control" />
            <Skeleton width="3/4" />
          </div>
        </Card>
      ) : null}
      {coverage ? (
        <Card title="Settings">
          <ItemCoverageForm coverage={coverage} onSaved={setSaved} onReload={() => setReload((n) => n + 1)} />
        </Card>
      ) : null}
    </div>
  );
}
