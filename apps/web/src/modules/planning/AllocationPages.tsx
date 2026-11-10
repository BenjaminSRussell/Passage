import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  allocateForecast,
  createAllocationKey,
  forecastModelVersion,
  getAllocationKey,
  listAllocationKeys,
  updateAllocationKey,
  type AllocateForecastResult,
  type AllocateForecastWrite,
  type AllocationKey,
  type AllocationKeyWrite,
} from "./api/client";
import { describeError, PLAN_CODE, useLoad, type LoadError } from "./hooks";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { EntityLookup } from "../../shell/lookup/EntityLookup";

const LIST = "/planning/allocation-keys";
const UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const SHARE = /^\d{1,3}(?:\.\d{1,6})?$/;
const FACTOR = /^\d{1,7}(?:\.\d{1,9})?$/;
const QTY = /^(?:0|[1-9]\d{0,11})(?:\.\d{1,6})?$/;
const UOM = /^[A-Za-z0-9._/-]{1,10}$/;
const MONTH_START = /^\d{4}-\d{2}-01$/;
const MAX_LINES = 1000;
const MAX_PERIODS = 60;

export interface LineDraft {
  itemId: string;
  share: string;
  uomFactor: string;
}

export interface KeyDraft {
  code: string;
  name: string;
  forecastUom: string;
  lines: LineDraft[];
}

/** Field errors: top-level keys, and per line `lines.<i>.<field>`. */
export type KeyErrors = Record<string, string>;

const EMPTY_LINE: LineDraft = { itemId: "", share: "", uomFactor: "1" };

export const emptyKeyDraft = (): KeyDraft => ({ code: "", name: "", forecastUom: "", lines: [{ ...EMPTY_LINE }] });

export function draftOfKey(k: AllocationKey): KeyDraft {
  return { code: k.code, name: k.name, forecastUom: k.forecastUom, lines: k.lines.map((l) => ({ itemId: l.itemId, share: l.share, uomFactor: l.uomFactor })) };
}

/** A decimal string of up to 6 decimals as millionths, so shares add up exactly. */
function micro(share: string): number {
  const [whole, frac = ""] = share.split(".");
  return Number(whole) * 1_000_000 + Number(frac.padEnd(6, "0"));
}

/** The sum of the valid shares of a draft, for the live total (a percentage as typed, 6 decimals). */
export function shareTotal(lines: LineDraft[]): string {
  const total = lines.reduce((sum, l) => (SHARE.test(l.share.trim()) ? sum + micro(l.share.trim()) : sum), 0);
  const whole = Math.floor(total / 1_000_000);
  const frac = String(total % 1_000_000).padStart(6, "0").replace(/0+$/, "");
  return frac ? `${whole}.${frac}` : String(whole);
}

/** Checks a key draft with the service's rules and returns the body or the field errors. */
export function validateKey(d: KeyDraft, checkCode: boolean): { errors: KeyErrors; body: AllocationKeyWrite | null } {
  const errors: KeyErrors = {};
  if (checkCode && !PLAN_CODE.test(d.code.trim().toUpperCase())) errors.code = "Use 2 to 20 capital letters, digits, _ or -.";
  const name = d.name.trim();
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of 1 to 120 characters.";
  if (!UOM.test(d.forecastUom.trim())) errors.forecastUom = "Enter a unit of 1 to 10 characters, such as CASE.";
  if (d.lines.length === 0 || d.lines.length > MAX_LINES) errors.lines = `Add 1 to ${MAX_LINES} items.`;
  const seen = new Set<string>();
  let sumOk = true;
  d.lines.forEach((l, i) => {
    const at = (f: string) => `lines.${i}.${f}`;
    const item = l.itemId.trim().toLowerCase();
    if (!UUID.test(item)) errors[at("itemId")] = "Enter the item id as a UUID.";
    else if (seen.has(item)) errors[at("itemId")] = "This item is already on the key.";
    seen.add(item);
    const share = l.share.trim();
    if (!SHARE.test(share) || micro(share) <= 0 || micro(share) > 100_000_000) {
      errors[at("share")] = "Enter a share above 0 and up to 100, with at most 6 decimals.";
      sumOk = false;
    }
    const factor = l.uomFactor.trim();
    if (!FACTOR.test(factor) || Number(factor) <= 0 || Number(factor) > 1_000_000) errors[at("uomFactor")] = "Enter a factor above 0 and up to 1000000, with at most 9 decimals.";
  });
  if (sumOk && d.lines.length > 0 && !errors.lines && shareTotal(d.lines) !== "100") errors.lines = `The shares add up to ${shareTotal(d.lines)}; they must add up to 100.`;
  if (Object.keys(errors).length > 0) return { errors, body: null };
  return {
    errors,
    body: {
      name,
      forecastUom: d.forecastUom.trim(),
      lines: d.lines.map((l) => ({ itemId: l.itemId.trim().toLowerCase(), share: l.share.trim(), uomFactor: l.uomFactor.trim() })),
    },
  };
}

function newKey(): string {
  const c = globalThis.crypto as Crypto | undefined;
  return c?.randomUUID ? c.randomUUID() : `ak-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

const columns: DataTableColumn<AllocationKey>[] = [
  { key: "code", header: "Code", mono: true, render: (k) => k.code },
  { key: "name", header: "Name", render: (k) => k.name },
  { key: "uom", header: "Forecast unit", render: (k) => k.forecastUom },
  { key: "items", header: "Items", align: "end", render: (k) => k.lineCount },
  { key: "version", header: "Version", align: "end", render: (k) => k.version },
];

/** Allocation keys (FR-22.8): the list of the tenant's keys. */
export function AllocationKeysPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listAllocationKeys({ signal }), []);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Allocation keys"
        description="Split a forecast made for a product family across its items by percentage, converting the family's unit to each item's inventory unit."
        actions={
          <Link className={buttonClassName("primary")} to={`${LIST}/new`}>
            New allocation key
          </Link>
        }
      />
      <Alert tone="danger" title={state.error ? "Could not load allocation keys" : undefined} live="alert">
        {state.error ? <ProblemMessage problem={state.error} /> : null}
      </Alert>
      {state.error ? null : (
        <DataTable
          label="Allocation keys"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(k) => k.id}
          loading={state.loading}
          onRowOpen={(k) => navigate(`${LIST}/${encodeURIComponent(k.code)}`)}
          empty={{ title: "No allocation keys yet", description: "Create one to split an aggregate forecast across items." }}
        />
      )}
    </div>
  );
}

interface FormProps {
  /** The key being edited; absent when creating. */
  allocationKey?: AllocationKey;
  onSaved: (k: AllocationKey) => void;
  onReload?: () => void;
}

function AllocationKeyForm({ allocationKey, onSaved, onReload }: FormProps) {
  const creating = !allocationKey;
  const [draft, setDraft] = useState<KeyDraft>(() => (allocationKey ? draftOfKey(allocationKey) : emptyKeyDraft()));
  const [errors, setErrors] = useState<KeyErrors>({});
  const [failure, setFailure] = useState<LoadError | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per create attempt, kept across retries of the same draft.
  const key = useRef(newKey());

  useEffect(() => {
    if (allocationKey) {
      setDraft(draftOfKey(allocationKey));
      setErrors({});
    }
  }, [allocationKey?.id, allocationKey?.version]);

  const change = (fn: (d: KeyDraft) => KeyDraft) => {
    setDraft(fn);
    key.current = newKey();
  };
  const setLine = (i: number, field: keyof LineDraft, value: string) =>
    change((d) => ({ ...d, lines: d.lines.map((l, j) => (j === i ? { ...l, [field]: value } : l)) }));

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    const { errors: found, body } = validateKey(draft, creating);
    setErrors(found);
    if (!body) return;
    setBusy(true);
    setFailure(null);
    try {
      const saved = allocationKey
        ? await updateAllocationKey(allocationKey.code, allocationKey.version, body)
        : await createAllocationKey(draft.code.trim().toUpperCase(), body, key.current);
      onSaved(saved);
    } catch (err) {
      setFailure(describeError(err));
    } finally {
      setBusy(false);
    }
  }

  const stale = failure?.status === 412;
  const total = shareTotal(draft.lines);
  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label={creating ? "New allocation key" : `Edit allocation key ${allocationKey.code}`}>
      <div className={styles.wide}>
        <Alert
          tone="danger"
          title={failure ? (stale ? "Someone else changed this allocation key" : "The allocation key was not saved") : undefined}
          live="alert"
          action={
            stale && onReload ? (
              <Button size="sm" onClick={onReload}>
                Reload key
              </Button>
            ) : undefined
          }
        >
          {failure ? <ProblemMessage problem={failure} /> : null}
        </Alert>
      </div>
      {creating ? (
        <FormField id="ak-code" label="Code" hint="2 to 20 capital letters, digits, _ or -." error={errors.code}>
          {(a11y) => <Input {...a11y} className={styles.mono} value={draft.code} onChange={(e) => change((d) => ({ ...d, code: e.target.value }))} />}
        </FormField>
      ) : null}
      <FormField id="ak-name" label="Name" error={errors.name}>
        {(a11y) => <Input {...a11y} value={draft.name} onChange={(e) => change((d) => ({ ...d, name: e.target.value }))} />}
      </FormField>
      <FormField id="ak-uom" label="Forecast unit" hint="The unit of the aggregate quantities, such as CASE." error={errors.forecastUom}>
        {(a11y) => <Input {...a11y} value={draft.forecastUom} onChange={(e) => change((d) => ({ ...d, forecastUom: e.target.value }))} />}
      </FormField>
      {draft.lines.map((l, i) => (
        <fieldset key={i} className={`${styles.fieldset} ${styles.wide}`}>
          <legend className={styles.legend}>Item {i + 1}</legend>
          <div className={styles.form}>
            <FormField id={`ak-l${i}-item`} label="Item" error={errors[`lines.${i}.itemId`]}>
              {(a11y) => <EntityLookup {...a11y} entity="product" value={l.itemId} onChange={(e) => setLine(i, "itemId", e.target.value)} />}
            </FormField>
            <FormField id={`ak-l${i}-share`} label="Share (%)" error={errors[`lines.${i}.share`]}>
              {(a11y) => <Input {...a11y} inputMode="decimal" value={l.share} onChange={(e) => setLine(i, "share", e.target.value)} />}
            </FormField>
            <FormField id={`ak-l${i}-factor`} label="Inventory units per forecast unit" hint="1 when both units are the same." error={errors[`lines.${i}.uomFactor`]}>
              {(a11y) => <Input {...a11y} inputMode="decimal" value={l.uomFactor} onChange={(e) => setLine(i, "uomFactor", e.target.value)} />}
            </FormField>
            <div className={styles.inlineActions}>
              <Button size="sm" variant="ghost" disabled={draft.lines.length === 1} onClick={() => change((d) => ({ ...d, lines: d.lines.filter((_, j) => j !== i) }))}>
                Remove item {i + 1}
              </Button>
            </div>
          </div>
        </fieldset>
      ))}
      <div className={`${styles.wide} ${styles.inlineActions}`}>
        <Button size="sm" disabled={draft.lines.length >= MAX_LINES} onClick={() => change((d) => ({ ...d, lines: [...d.lines, { ...EMPTY_LINE }] }))}>
          Add item
        </Button>
        <p className={styles.summary} aria-live="polite">{`Shares add up to ${total}%.`}</p>
        {errors.lines ? <p className={styles.formError}>{errors.lines}</p> : null}
      </div>
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          {creating ? "Create allocation key" : "Save changes"}
        </Button>
        <Link className={buttonClassName("secondary")} to={LIST}>
          Cancel
        </Link>
      </div>
    </form>
  );
}

/** Creates an allocation key. */
export function NewAllocationKeyPage() {
  const navigate = useNavigate();
  return (
    <div className={styles.stack}>
      <PageHeader title="New allocation key" description="List the items of the family with their percentage shares, which must add up to 100." />
      <Card>
        <AllocationKeyForm onSaved={(k) => navigate(`${LIST}/${encodeURIComponent(k.code)}`)} />
      </Card>
    </div>
  );
}

export interface AllocateDraft {
  modelCode: string;
  siteId: string;
  firstMonth: string;
  quantities: string;
}

type AllocateErrors = Partial<Record<keyof AllocateDraft, string>>;

/** The last day of the month that starts on `start` (YYYY-MM-01). */
export function monthEnd(start: string): string {
  const [y, m] = start.split("-").map(Number);
  return new Date(Date.UTC(y, m, 0)).toISOString().slice(0, 10);
}

/** The first day of the month `n` months after `start`. */
function monthStart(start: string, n: number): string {
  const [y, m] = start.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1 + n, 1)).toISOString().slice(0, 10);
}

/** Checks the allocate form and returns the model code with the request body for one key. */
export function validateAllocate(d: AllocateDraft, keyCode: string): { errors: AllocateErrors; body: AllocateForecastWrite | null } {
  const errors: AllocateErrors = {};
  if (!PLAN_CODE.test(d.modelCode.trim().toUpperCase())) errors.modelCode = "Use 2 to 20 capital letters, digits, _ or -.";
  if (!UUID.test(d.siteId.trim())) errors.siteId = "Enter the site id as a UUID.";
  if (!MONTH_START.test(d.firstMonth)) errors.firstMonth = "Enter the first day of the first month.";
  const quantities = d.quantities.split(/[\s,;]+/).filter((v) => v !== "");
  if (quantities.length === 0 || quantities.length > MAX_PERIODS) errors.quantities = `Enter 1 to ${MAX_PERIODS} monthly quantities.`;
  else if (quantities.some((v) => !QTY.test(v))) errors.quantities = "Every quantity must be 0 or more, with at most 6 decimals.";
  if (Object.keys(errors).length > 0) return { errors, body: null };
  return {
    errors,
    body: {
      keyCode,
      siteId: d.siteId.trim(),
      periods: quantities.map((qty, i) => {
        const periodStart = monthStart(d.firstMonth, i);
        return { periodStart, periodEnd: monthEnd(periodStart), qty };
      }),
    },
  };
}

/** The confirmation shown after an allocation. */
export function allocateOutcome(modelCode: string, r: AllocateForecastResult): string {
  return `Forecast model ${modelCode}: ${r.cellsWritten} manual cells written for ${r.items} items over ${r.periods} months. The model is at version ${r.model.version}.`;
}

function AllocateCard({ allocationKey, onDone }: { allocationKey: AllocationKey; onDone: (message: string) => void }) {
  const [draft, setDraft] = useState<AllocateDraft>({ modelCode: "", siteId: "", firstMonth: "", quantities: "" });
  const [errors, setErrors] = useState<AllocateErrors>({});
  const [failure, setFailure] = useState<LoadError | null>(null);
  const [busy, setBusy] = useState(false);
  const set = <K extends keyof AllocateDraft>(field: K, value: AllocateDraft[K]) => setDraft({ ...draft, [field]: value });

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    const { errors: found, body } = validateAllocate(draft, allocationKey.code);
    setErrors(found);
    if (!body) return;
    const modelCode = draft.modelCode.trim().toUpperCase();
    setBusy(true);
    setFailure(null);
    try {
      const version = await forecastModelVersion(modelCode);
      onDone(allocateOutcome(modelCode, await allocateForecast(modelCode, version, body)));
    } catch (err) {
      setFailure(describeError(err));
    } finally {
      setBusy(false);
    }
  }

  const title = !failure
    ? undefined
    : failure.code === "PLAN-FORECAST-LOCKED"
      ? "That forecast model is submitted or published; its cells are locked"
      : failure.code === "PLAN-ALLOCATION-RANGE"
        ? "A converted quantity is too large to store; check the quantities and factors"
        : failure.status === 412
          ? "Someone else changed that forecast; try again"
          : "The forecast was not allocated";

  return (
    <Card title="Allocate a forecast">
      <p className={styles.summary}>
        {`Enter the forecast of the whole family per month in ${allocationKey.forecastUom}. Each item gets its share, converted to its inventory unit, as manual cells of the model; cells of the same month are replaced.`}
      </p>
      <div className={styles.stack}>
        <Alert tone="danger" title={title} live="alert">
          {failure ? <ProblemMessage problem={failure} /> : null}
        </Alert>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Allocate a forecast">
          <FormField id="al-model" label="Forecast model" hint="Created when it does not exist yet." error={errors.modelCode}>
            {(a11y) => <Input {...a11y} className={styles.mono} value={draft.modelCode} onChange={(e) => set("modelCode", e.target.value)} />}
          </FormField>
          <FormField id="al-site" label="Site" error={errors.siteId}>
            {(a11y) => <EntityLookup {...a11y} entity="site" value={draft.siteId} onChange={(e) => set("siteId", e.target.value)} />}
          </FormField>
          <FormField id="al-first" label="First month" error={errors.firstMonth}>
            {(a11y) => <Input {...a11y} type="date" value={draft.firstMonth} onChange={(e) => set("firstMonth", e.target.value)} />}
          </FormField>
          <FormField id="al-qty" label={`Monthly quantities (${allocationKey.forecastUom})`} hint="Separated by commas, spaces or new lines." error={errors.quantities}>
            {(a11y) => <Input {...a11y} inputMode="decimal" value={draft.quantities} onChange={(e) => set("quantities", e.target.value)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Allocate forecast
            </Button>
          </div>
        </form>
      </div>
    </Card>
  );
}

const lineColumns: DataTableColumn<AllocationKey["lines"][number]>[] = [
  { key: "item", header: "Item", render: (l) => <EntityName entity="product" id={l.itemId} fallback={l.itemId} /> },
  { key: "share", header: "Share (%)", align: "end", render: (l) => l.share },
  { key: "factor", header: "Inventory units per forecast unit", align: "end", render: (l) => l.uomFactor },
];

/** Shows one allocation key, edits it under If-Match and allocates a forecast with it. */
export function AllocationKeyPage() {
  const { code = "" } = useParams();
  const [reload, setReload] = useState(0);
  const state = useLoad((signal) => getAllocationKey(code, { signal }), [code, reload]);
  const [saved, setSaved] = useState<AllocationKey | null>(null);
  const [allocated, setAllocated] = useState<string | null>(null);
  const allocationKey = saved && saved.code === code ? saved : state.data;

  useEffect(() => setSaved(null), [state.data]);

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Allocation key ${code}`}
        description="Changes apply to the next allocation; cells written earlier keep their quantities."
        actions={
          <Link className={buttonClassName("secondary")} to={LIST}>
            All allocation keys
          </Link>
        }
      />
      <Alert tone="danger" title={state.error ? `Could not load allocation key ${code}` : undefined} live="alert">
        {state.error ? <ProblemMessage problem={state.error} /> : null}
      </Alert>
      <Alert tone="success" live="status">
        {saved ? `Saved. Version ${saved.version}.` : allocated}
      </Alert>
      {state.loading ? (
        <Card>
          <div className={styles.stack} aria-busy="true" aria-label="Loading allocation key">
            <Skeleton width="1/2" />
            <Skeleton height="control" />
            <Skeleton height="control" />
            <Skeleton width="3/4" />
          </div>
        </Card>
      ) : null}
      {allocationKey ? (
        <>
          <Card title={`Items (${allocationKey.lineCount})`}>
            <DataTable label="Key items" columns={lineColumns} rows={allocationKey.lines} getRowKey={(l) => l.itemId} empty={{ title: "No items" }} />
          </Card>
          <AllocateCard allocationKey={allocationKey} onDone={setAllocated} />
          <Card title="Settings">
            <AllocationKeyForm allocationKey={allocationKey} onSaved={setSaved} onReload={() => setReload((n) => n + 1)} />
          </Card>
        </>
      ) : null}
    </div>
  );
}
