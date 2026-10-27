import { useState, type FormEvent, type ReactNode } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, Textarea, type BadgeTone } from "../../shell/ui";
import {
  allocateQuota,
  allocateShelfLife,
  consumeForecast,
  evaluateBuffer,
  projectSupplySchedule,
  QuotaSourceType,
  type BatchInput,
  type Bucket,
  type BufferResult,
  type BufferZone,
  type ConsumptionMode,
  type ConsumptionResult,
  type DayQty,
  type ForecastPeriodInput,
  type QuotaResult,
  type QuotaSourceInput,
  type SalesLineInput,
  type ShelfLifeResult,
  type SupplySchedule,
} from "./api/calculators";
import { describeError, type LoadError } from "./hooks";
import styles from "./pages.module.css";

const DECIMAL = /^\d+(\.\d{1,6})?$/;
const SIGNED_DECIMAL = /^-?\d+(\.\d{1,6})?$/;
const INTEGER = /^-?\d+$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const CURRENCY = /^[A-Z]{3}$/;
const MAX_DAYS = 3650;

type Errors<F> = Partial<Record<keyof F, string>>;

const isDays = (text: string, min = 0) => INTEGER.test(text) && Number(text) >= min && Number(text) <= MAX_DAYS;

/** Non-empty lines of a textarea, trimmed. */
const lines = (text: string) =>
  text
    .split(/\r?\n/)
    .map((s) => s.trim())
    .filter((s) => s !== "");

/** Parses "day:qty, day:qty" into dated quantities. Blank means none. */
export function parseFlows(text: string): { flows: DayQty[]; error?: string } {
  const flows: DayQty[] = [];
  for (const raw of text.split(",").map((s) => s.trim()).filter((s) => s !== "")) {
    const [day, qty, extra] = raw.split(":").map((s) => s.trim());
    if (extra !== undefined || !day || !qty) return { flows: [], error: `Use day:qty pairs, for example 3:4 (got "${raw}").` };
    if (!INTEGER.test(day) || Math.abs(Number(day)) > MAX_DAYS)
      return { flows: [], error: `Day must be a whole number from -3650 to 3650 (got "${day}").` };
    if (!DECIMAL.test(qty)) return { flows: [], error: `Quantity must be 0 or more with up to 6 decimals (got "${qty}").` };
    flows.push({ day: Number(day), qty });
  }
  return { flows };
}

/** Parses one "id, qty, expiry day" batch per line. */
export function parseBatches(text: string): { batches: BatchInput[]; error?: string } {
  const batches: BatchInput[] = [];
  for (const raw of lines(text)) {
    const [id, qty, expiry, extra] = raw.split(",").map((s) => s.trim());
    if (extra !== undefined || !id || !qty || !expiry)
      return { batches: [], error: `Use one "id, qty, expiry day" per line, for example B1, 100, 20 (got "${raw}").` };
    if (id.length > 64) return { batches: [], error: `Batch id is at most 64 characters (got "${id}").` };
    if (batches.some((b) => b.id === id)) return { batches: [], error: `Batch ${id} is listed twice.` };
    if (!DECIMAL.test(qty)) return { batches: [], error: `Quantity must be 0 or more with up to 6 decimals (got "${qty}").` };
    if (!INTEGER.test(expiry) || Math.abs(Number(expiry)) > MAX_DAYS)
      return { batches: [], error: `Expiry day must be a whole number from -3650 to 3650 (got "${expiry}").` };
    batches.push({ id, qty, expiryDay: Number(expiry) });
  }
  return { batches };
}

/** Parses one "id, type, quota, allocated qty[, priority[, blocked]]" source per line. */
export function parseSources(text: string): { sources: QuotaSourceInput[]; error?: string } {
  const sources: QuotaSourceInput[] = [];
  for (const raw of lines(text)) {
    const [id, type, quota, allocated, priority, blocked, extra] = raw.split(",").map((s) => s.trim());
    const usage = `Use one "id, type, quota, allocated qty, priority, blocked" per line; priority and blocked may be left off (got "${raw}").`;
    if (extra !== undefined || !id || !type || !quota || !allocated) return { sources: [], error: usage };
    if (id.length > 64) return { sources: [], error: `Source id is at most 64 characters (got "${id}").` };
    if (sources.some((s) => s.id === id)) return { sources: [], error: `Source ${id} is listed twice.` };
    const kind = QuotaSourceType.safeParse(type);
    if (!kind.success) return { sources: [], error: `Type must be vendor, production or transfer (got "${type}").` };
    if (!DECIMAL.test(quota) || !DECIMAL.test(allocated))
      return { sources: [], error: `Quota and allocated qty must be 0 or more (got "${quota}", "${allocated}").` };
    if (priority !== undefined && priority !== "" && !INTEGER.test(priority))
      return { sources: [], error: `Priority must be a whole number (got "${priority}").` };
    if (blocked !== undefined && blocked !== "" && blocked !== "blocked")
      return { sources: [], error: `Write "blocked" in the last column, or leave it off (got "${blocked}").` };
    sources.push({
      id,
      type: kind.data,
      quota,
      allocatedQty: allocated,
      ...(priority ? { priority: Number(priority) } : {}),
      ...(blocked === "blocked" ? { blocked: true } : {}),
    });
  }
  return { sources };
}

/** Parses one "id, start date, end date, qty" forecast period per line. */
export function parseForecastPeriods(text: string): { periods: ForecastPeriodInput[]; error?: string } {
  const periods: ForecastPeriodInput[] = [];
  for (const raw of lines(text)) {
    const [id, startDate, endDate, qty, extra] = raw.split(",").map((s) => s.trim());
    if (extra !== undefined || !id || !startDate || !endDate || !qty)
      return { periods: [], error: `Use one "id, start date, end date, qty" per line (got "${raw}").` };
    if (!DATE.test(startDate) || !DATE.test(endDate)) return { periods: [], error: `Dates are YYYY-MM-DD (got "${raw}").` };
    if (endDate < startDate) return { periods: [], error: `Period ${id} ends before it starts.` };
    if (!DECIMAL.test(qty)) return { periods: [], error: `Quantity must be 0 or more (got "${qty}").` };
    periods.push({ id, startDate, endDate, qty });
  }
  return { periods };
}

/** Parses one "id, date, qty" sales line per line. */
export function parseSalesLines(text: string): { sales: SalesLineInput[]; error?: string } {
  const sales: SalesLineInput[] = [];
  for (const raw of lines(text)) {
    const [id, date, qty, extra] = raw.split(",").map((s) => s.trim());
    if (extra !== undefined || !id || !date || !qty) return { sales: [], error: `Use one "id, date, qty" per line (got "${raw}").` };
    if (!DATE.test(date)) return { sales: [], error: `Dates are YYYY-MM-DD (got "${date}").` };
    if (!DECIMAL.test(qty)) return { sales: [], error: `Quantity must be 0 or more (got "${qty}").` };
    sales.push({ id, date, qty });
  }
  return { sales };
}

/** Runs one calculator call at a time and keeps its result or its error. */
function useCalculation<I, R>(call: (input: I) => Promise<R>) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<LoadError | null>(null);
  const [result, setResult] = useState<R | null>(null);
  async function run(input: I) {
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      setResult(await call(input));
    } catch (err) {
      setResult(null);
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  }
  return { busy, error, result, run };
}

function Failure({ error, title }: { error: LoadError | null; title: string }) {
  return (
    <Alert tone="danger" title={error ? title : undefined} live="alert">
      {error ? `${error.message} (${error.code})` : null}
    </Alert>
  );
}

function Details({ label, items }: { label: string; items: [string, ReactNode][] }) {
  return (
    <dl className={styles.details} aria-label={label}>
      {items.map(([term, value]) => (
        <div key={term} className={styles.entry}>
          <dt className={styles.term}>{term}</dt>
          <dd className={styles.value}>{value}</dd>
        </div>
      ))}
    </dl>
  );
}

/** Small text input bound to one form field, with its label, hint and error. */
function Field<F>(props: {
  id: string;
  label: string;
  field: keyof F;
  form: F;
  errors: Errors<F>;
  set: (field: keyof F, value: string) => void;
  hint?: string;
  optional?: boolean;
  multiline?: boolean;
}) {
  const { id, label, field, form, errors, set, hint, optional, multiline } = props;
  const value = String(form[field] ?? "");
  return (
    <div className={multiline ? styles.wide : undefined}>
      <FormField id={id} label={label} hint={hint} optional={optional} error={errors[field]}>
        {(a11y) =>
          multiline ? (
            <Textarea {...a11y} className={styles.mono} rows={4} value={value} onChange={(e) => set(field, e.target.value)} />
          ) : (
            <Input {...a11y} value={value} onChange={(e) => set(field, e.target.value)} />
          )
        }
      </FormField>
    </div>
  );
}

function useForm<F>(initial: F) {
  const [form, setForm] = useState<F>(initial);
  const [errors, setErrors] = useState<Errors<F>>({});
  const set = (field: keyof F, value: string) => setForm((f) => ({ ...f, [field]: value }));
  return { form, errors, setErrors, set };
}

// DDMRP buffer (FR-22.16, BR-22.11)

export interface BufferForm {
  adu: string;
  dltDays: string;
  leadTimeFactor: string;
  variabilityFactor: string;
  orderCycleDays: string;
  minOrderQty: string;
  orderMultiple: string;
  onHand: string;
  openSupply: string;
  demand: string;
}

/** The BR-22.11 worked example: NFP 130, order 115, priority 53.06 %, yellow. */
export const BUFFER_EXAMPLE: BufferForm = {
  adu: "10",
  dltDays: "10",
  leadTimeFactor: "0.5",
  variabilityFactor: "0.5",
  orderCycleDays: "7",
  minOrderQty: "0",
  orderMultiple: "0",
  onHand: "90",
  openSupply: "60",
  demand: "0:20",
};

export function validateBufferForm(f: BufferForm): Errors<BufferForm> {
  const e: Errors<BufferForm> = {};
  for (const k of ["adu", "leadTimeFactor", "variabilityFactor", "minOrderQty", "orderMultiple", "openSupply"] as const) {
    if (!DECIMAL.test(f[k])) e[k] = "Enter 0 or more, with up to 6 decimals.";
  }
  if (!DECIMAL.test(f.dltDays) || Number(f.dltDays) > MAX_DAYS) e.dltDays = "Enter 0 to 3650 days.";
  if (!isDays(f.orderCycleDays)) e.orderCycleDays = "Enter whole days from 0 to 3650.";
  if (!SIGNED_DECIMAL.test(f.onHand)) e.onHand = "Enter a number with up to 6 decimals.";
  const demand = parseFlows(f.demand);
  if (demand.error) e.demand = demand.error;
  return e;
}

const zoneTone: Record<BufferZone, BadgeTone> = { red: "danger", yellow: "warning", green: "success" };

export const percent = (ratio: string) => `${(Number(ratio) * 100).toFixed(2)} %`;

export function BufferCalculator() {
  const { form, errors, setErrors, set } = useForm(BUFFER_EXAMPLE);
  const calc = useCalculation(evaluateBuffer);
  const field = { form, errors, set };

  function submit(e: FormEvent) {
    e.preventDefault();
    const found = validateBufferForm(form);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void calc.run({
      profile: {
        leadTimeFactor: form.leadTimeFactor,
        variabilityFactor: form.variabilityFactor,
        orderCycleDays: Number(form.orderCycleDays),
        minOrderQty: form.minOrderQty,
      },
      adu: form.adu,
      dltDays: form.dltDays,
      onHand: form.onHand,
      openSupply: form.openSupply,
      demand: parseFlows(form.demand).flows,
      orderMultiple: form.orderMultiple,
    });
  }

  const r: BufferResult | null = calc.result;
  return (
    <Card title="DDMRP buffer">
      <div className={styles.stack}>
        <p className={styles.summary}>
          Buffer zones, net-flow position and the replenishment order for one decoupled item. Filled in with the BR-22.11 example.
        </p>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="DDMRP buffer">
          <Field<BufferForm> id="ddmrp-adu" label="Average daily usage" field="adu" {...field} />
          <Field<BufferForm> id="ddmrp-dlt" label="Decoupled lead time (days)" field="dltDays" {...field} />
          <Field<BufferForm> id="ddmrp-ltf" label="Lead-time factor" field="leadTimeFactor" {...field} />
          <Field<BufferForm> id="ddmrp-vf" label="Variability factor" field="variabilityFactor" {...field} />
          <Field<BufferForm> id="ddmrp-cycle" label="Order cycle (days)" field="orderCycleDays" {...field} />
          <Field<BufferForm> id="ddmrp-moq" label="Minimum order qty" field="minOrderQty" {...field} />
          <Field<BufferForm> id="ddmrp-multiple" label="Order multiple" field="orderMultiple" hint="0 means none." {...field} />
          <Field<BufferForm> id="ddmrp-on-hand" label="On-hand" field="onHand" {...field} />
          <Field<BufferForm> id="ddmrp-open" label="Open supply" field="openSupply" hint="Open orders due inside the DLT." {...field} />
          <Field<BufferForm>
            id="ddmrp-demand"
            label="Demand"
            field="demand"
            hint="day:qty pairs from today (day 0); past-due and today's demand always count, later days only as spikes."
            optional
            {...field}
          />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={calc.busy}>
              Evaluate buffer
            </Button>
          </div>
        </form>
        <Failure error={calc.error} title="The buffer could not be evaluated" />
        {r ? (
          <Details
            label="Buffer status"
            items={[
              ["Zone", <Badge tone={zoneTone[r.zone]}>{r.zone[0].toUpperCase() + r.zone.slice(1)}</Badge>],
              ["Planned order qty", r.plannedQty],
              ["Planning priority", percent(r.priority)],
              ["Net-flow position", r.netFlow],
              ["Qualified demand", r.qualifiedDemand],
              ["Top of red", r.zones.topOfRed],
              ["Top of yellow", r.zones.topOfYellow],
              ["Top of green", r.zones.topOfGreen],
            ]}
          />
        ) : null}
      </div>
    </Card>
  );
}

// Quota allocation (FR-22.17, BR-22.12)

export interface QuotaForm {
  qty: string;
  sources: string;
  split: "no" | "yes";
  minSplitQty: string;
}

/** The BR-22.12 example: B (rating 2.50) takes the 100 and ends at 5.00. */
export const QUOTA_EXAMPLE: QuotaForm = {
  qty: "100",
  sources: "A, vendor, 60, 300, 1\nB, vendor, 40, 100, 2",
  split: "no",
  minSplitQty: "0",
};

export function validateQuotaForm(f: QuotaForm): Errors<QuotaForm> {
  const e: Errors<QuotaForm> = {};
  if (!DECIMAL.test(f.qty) || Number(f.qty) <= 0) e.qty = "Enter a quantity above 0.";
  if (!DECIMAL.test(f.minSplitQty)) e.minSplitQty = "Enter 0 or more.";
  const parsed = parseSources(f.sources);
  if (parsed.error) e.sources = parsed.error;
  else if (parsed.sources.length === 0) e.sources = "Add at least one source.";
  else if (parsed.sources.length > 100) e.sources = "At most 100 sources.";
  return e;
}

export function QuotaCalculator() {
  const { form, errors, setErrors, set } = useForm(QUOTA_EXAMPLE);
  const calc = useCalculation(allocateQuota);
  const field = { form, errors, set };

  function submit(e: FormEvent) {
    e.preventDefault();
    const found = validateQuotaForm(form);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void calc.run({
      qty: form.qty,
      sources: parseSources(form.sources).sources,
      split: form.split === "yes",
      minSplitQty: form.minSplitQty,
    });
  }

  const r: QuotaResult | null = calc.result;
  return (
    <Card title="Quota arrangement">
      <div className={styles.stack}>
        <p className={styles.summary}>
          Which source a requirement goes to: the lowest (allocated + base) / quota wins. Filled in with the BR-22.12 example.
        </p>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Quota arrangement">
          <Field<QuotaForm> id="quota-qty" label="Requirement qty" field="qty" {...field} />
          <FormField id="quota-split" label="Split across sources">
            {(a11y) => (
              <Select {...a11y} value={form.split} onChange={(e) => set("split", e.target.value)}>
                <option value="no">No, one source takes all</option>
                <option value="yes">Yes, equalise ratings</option>
              </Select>
            )}
          </FormField>
          <Field<QuotaForm> id="quota-min-split" label="Minimum split qty" field="minSplitQty" {...field} />
          <Field<QuotaForm>
            id="quota-sources"
            label="Sources"
            field="sources"
            hint='One per line: id, vendor|production|transfer, quota, allocated qty, priority, and "blocked" when the source cannot be used.'
            multiline
            {...field}
          />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={calc.busy}>
              Allocate
            </Button>
          </div>
        </form>
        <Failure error={calc.error} title="The requirement could not be allocated" />
        {r || calc.busy ? (
          <DataTable
            label="Quota assignments"
            loading={calc.busy}
            rows={r?.assignments ?? []}
            getRowKey={(a) => a.sourceId}
            columns={[
              { key: "sourceId", header: "Source", mono: true },
              { key: "qty", header: "Quantity", align: "end" },
              { key: "ratingAfter", header: "Rating after", align: "end" },
            ]}
            empty={{ title: "No source received a quantity" }}
          />
        ) : null}
      </div>
    </Card>
  );
}

// Forecast consumption (FR-22.19, BR-22.13)

export interface ConsumptionForm {
  mode: ConsumptionMode;
  backwardDays: string;
  forwardDays: string;
  forecast: string;
  sales: string;
}

/** AC-22.14: forecast 100 for November, a sales line of 40 on 20 November; gross is 100, not 140. */
export const CONSUMPTION_EXAMPLE: ConsumptionForm = {
  mode: "backward-forward",
  backwardDays: "30",
  forwardDays: "0",
  forecast: "NOV, 2026-11-01, 2026-11-30, 100",
  sales: "SO-1, 2026-11-20, 40",
};

export function validateConsumptionForm(f: ConsumptionForm): Errors<ConsumptionForm> {
  const e: Errors<ConsumptionForm> = {};
  if (!isDays(f.backwardDays)) e.backwardDays = "Enter whole days from 0 to 3650.";
  if (!isDays(f.forwardDays)) e.forwardDays = "Enter whole days from 0 to 3650.";
  const periods = parseForecastPeriods(f.forecast);
  if (periods.error) e.forecast = periods.error;
  const sales = parseSalesLines(f.sales);
  if (sales.error) e.sales = sales.error;
  return e;
}

export function ConsumptionCalculator() {
  const { form, errors, setErrors, set } = useForm(CONSUMPTION_EXAMPLE);
  const calc = useCalculation(consumeForecast);
  const field = { form, errors, set };

  function submit(e: FormEvent) {
    e.preventDefault();
    const found = validateConsumptionForm(form);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void calc.run({
      mode: form.mode,
      backwardDays: Number(form.backwardDays),
      forwardDays: Number(form.forwardDays),
      forecast: parseForecastPeriods(form.forecast).periods,
      sales: parseSalesLines(form.sales).sales,
    });
  }

  const r: ConsumptionResult | null = calc.result;
  return (
    <Card title="Forecast consumption">
      <div className={styles.stack}>
        <p className={styles.summary}>
          How sales lines consume the forecast under assemble-to-order. Filled in with the AC-22.14 example.
        </p>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Forecast consumption">
          <FormField id="consume-mode" label="Consumption mode">
            {(a11y) => (
              <Select {...a11y} value={form.mode} onChange={(e) => set("mode", e.target.value)}>
                <option value="backward">Backward</option>
                <option value="forward">Forward</option>
                <option value="backward-forward">Backward, then forward</option>
              </Select>
            )}
          </FormField>
          <Field<ConsumptionForm> id="consume-backward" label="Backward days" field="backwardDays" {...field} />
          <Field<ConsumptionForm> id="consume-forward" label="Forward days" field="forwardDays" {...field} />
          <Field<ConsumptionForm>
            id="consume-forecast"
            label="Forecast periods"
            field="forecast"
            hint="One per line: id, start date, end date, qty."
            multiline
            {...field}
          />
          <Field<ConsumptionForm> id="consume-sales" label="Sales lines" field="sales" hint="One per line: id, date, qty." multiline {...field} />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={calc.busy}>
              Consume forecast
            </Button>
          </div>
        </form>
        <Failure error={calc.error} title="The forecast could not be consumed" />
        {r ? (
          <>
            <p role="status" className={styles.summary}>
              Gross requirement {r.grossQty}
            </p>
            <DataTable
              label="Sales consumption"
              rows={r.sales}
              getRowKey={(s, i) => `${s.id}-${i}`}
              columns={[
                { key: "id", header: "Sales line", mono: true },
                { key: "consumedQty", header: "Consumed", align: "end" },
                { key: "unconsumedQty", header: "Added on top", align: "end" },
              ]}
              empty={{ title: "No sales lines", description: "Add sales lines to see what they consume." }}
            />
            <DataTable
              label="Forecast after consumption"
              rows={r.forecast}
              getRowKey={(f, i) => `${f.id}-${i}`}
              columns={[
                { key: "id", header: "Period", mono: true },
                { key: "remainingQty", header: "Remaining forecast", align: "end" },
              ]}
              empty={{ title: "No forecast periods" }}
            />
          </>
        ) : null}
      </div>
    </Card>
  );
}

// Supply schedule and inventory forecast (FR-22.21, FR-22.22)

export interface SupplyForm {
  horizonDays: string;
  bucket: Bucket;
  openingOnHand: string;
  safetyStock: string;
  unitCost: string;
  currency: string;
  demand: string;
  supply: string;
  planned: string;
}

export const SUPPLY_EXAMPLE: SupplyForm = {
  horizonDays: "28",
  bucket: "week",
  openingOnHand: "10",
  safetyStock: "5",
  unitCost: "2.50",
  currency: "EUR",
  demand: "3:4, 10:6, 20:15",
  supply: "5:20",
  planned: "12:8",
};

export function validateSupplyForm(f: SupplyForm): Errors<SupplyForm> {
  const e: Errors<SupplyForm> = {};
  if (!isDays(f.horizonDays, 1)) e.horizonDays = "Enter whole days from 1 to 3650.";
  for (const k of ["openingOnHand", "safetyStock", "unitCost"] as const) {
    if (!DECIMAL.test(f[k])) e[k] = "Enter 0 or more, with up to 6 decimals.";
  }
  if (!CURRENCY.test(f.currency)) e.currency = "Enter a three-letter currency code, such as EUR.";
  for (const k of ["demand", "supply", "planned"] as const) {
    const parsed = parseFlows(f[k]);
    if (parsed.error) e[k] = parsed.error;
  }
  return e;
}

/** Days-of-supply cell: "n days", or a dash when no demand follows the bucket. */
export function formatDays(value: string | null): string {
  return value === null ? "—" : `${value} days`;
}

export function SupplyScheduleCalculator() {
  const { form, errors, setErrors, set } = useForm(SUPPLY_EXAMPLE);
  const calc = useCalculation(projectSupplySchedule);
  const field = { form, errors, set };

  function submit(e: FormEvent) {
    e.preventDefault();
    const found = validateSupplyForm(form);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void calc.run({
      horizonDays: Number(form.horizonDays),
      bucket: form.bucket,
      openingOnHand: form.openingOnHand,
      safetyStock: form.safetyStock,
      unitCost: form.unitCost,
      currency: form.currency,
      demand: parseFlows(form.demand).flows,
      supply: parseFlows(form.supply).flows,
      planned: parseFlows(form.planned).flows,
    });
  }

  const r: SupplySchedule | null = calc.result;
  return (
    <Card title="Supply schedule and inventory forecast">
      <div className={styles.stack}>
        <p className={styles.summary}>
          Projected on-hand, value and days of supply per period for one item. Quantities are day:qty pairs counted from today (day 0).
        </p>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Supply schedule">
          <Field<SupplyForm> id="supply-horizon" label="Horizon (days)" field="horizonDays" {...field} />
          <FormField id="supply-bucket" label="Period">
            {(a11y) => (
              <Select {...a11y} value={form.bucket} onChange={(e) => set("bucket", e.target.value)}>
                <option value="day">Day</option>
                <option value="week">Week</option>
                <option value="month">Month (30 days)</option>
              </Select>
            )}
          </FormField>
          <Field<SupplyForm> id="supply-opening" label="Opening on-hand" field="openingOnHand" {...field} />
          <Field<SupplyForm> id="supply-safety" label="Safety stock" field="safetyStock" {...field} />
          <Field<SupplyForm> id="supply-cost" label="Unit cost" field="unitCost" {...field} />
          <Field<SupplyForm> id="supply-currency" label="Currency" field="currency" {...field} />
          <Field<SupplyForm> id="supply-demand" label="Demand" field="demand" optional {...field} />
          <Field<SupplyForm> id="supply-receipts" label="Scheduled receipts" field="supply" optional {...field} />
          <Field<SupplyForm> id="supply-planned" label="Planned supply" field="planned" optional {...field} />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={calc.busy}>
              Project
            </Button>
          </div>
        </form>
        <Failure error={calc.error} title="The schedule could not be projected" />
        {r || calc.busy ? (
          <DataTable
            label="Supply schedule"
            loading={calc.busy}
            rows={r?.periods ?? []}
            getRowKey={(p) => String(p.startDay)}
            columns={[
              { key: "days", header: "Days", render: (p) => `${p.startDay}–${p.endDay}` },
              { key: "demand", header: "Demand", align: "end" },
              { key: "supply", header: "Receipts", align: "end" },
              { key: "planned", header: "Planned", align: "end" },
              { key: "closingOnHand", header: "Projected on-hand", align: "end" },
              {
                key: "shortage",
                header: "Below safety stock",
                render: (p) => (p.shortage ? <Badge tone="warning">Yes</Badge> : "No"),
              },
              { key: "value", header: `Value (${r?.currency ?? form.currency})`, align: "end" },
              { key: "daysOfSupply", header: "Days of supply", align: "end", render: (p) => formatDays(p.daysOfSupply) },
            ]}
            empty={{ title: "No periods in the horizon" }}
          />
        ) : null}
      </div>
    </Card>
  );
}

// Shelf-life allocation (FR-22.20, BR-22.15)

export interface ShelfLifeForm {
  demandDay: string;
  demandQty: string;
  requiredShelfLifeDays: string;
  horizonDay: string;
  batches: string;
}

/** AC-22.15: a batch expiring on day 20 cannot serve demand on day 5 that needs 30 days left. */
export const SHELF_LIFE_EXAMPLE: ShelfLifeForm = {
  demandDay: "5",
  demandQty: "30",
  requiredShelfLifeDays: "30",
  horizonDay: "25",
  batches: "B1, 100, 20",
};

export function validateShelfLifeForm(f: ShelfLifeForm): Errors<ShelfLifeForm> {
  const e: Errors<ShelfLifeForm> = {};
  if (!isDays(f.demandDay)) e.demandDay = "Enter whole days from 0 to 3650.";
  if (!DECIMAL.test(f.demandQty)) e.demandQty = "Enter 0 or more, with up to 6 decimals.";
  if (!isDays(f.requiredShelfLifeDays)) e.requiredShelfLifeDays = "Enter whole days from 0 to 3650.";
  if (f.horizonDay !== "" && !isDays(f.horizonDay)) e.horizonDay = "Enter whole days from 0 to 3650, or leave it empty.";
  const parsed = parseBatches(f.batches);
  if (parsed.error) e.batches = parsed.error;
  return e;
}

export function ShelfLifeCalculator() {
  const { form, errors, setErrors, set } = useForm(SHELF_LIFE_EXAMPLE);
  const calc = useCalculation(allocateShelfLife);
  const field = { form, errors, set };

  function submit(e: FormEvent) {
    e.preventDefault();
    const found = validateShelfLifeForm(form);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void calc.run({
      demandDay: Number(form.demandDay),
      demandQty: form.demandQty,
      requiredShelfLifeDays: Number(form.requiredShelfLifeDays),
      ...(form.horizonDay === "" ? {} : { horizonDay: Number(form.horizonDay) }),
      batches: parseBatches(form.batches).batches,
    });
  }

  const r: ShelfLifeResult | null = calc.result;
  return (
    <Card title="Shelf-life check">
      <div className={styles.stack}>
        <p className={styles.summary}>
          Batches serve a demand first-expiry-first, and only while they keep the required remaining shelf life. Filled in with the AC-22.15
          example.
        </p>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Shelf-life check">
          <Field<ShelfLifeForm> id="shelf-day" label="Demand day" field="demandDay" hint="Days from today." {...field} />
          <Field<ShelfLifeForm> id="shelf-qty" label="Demand qty" field="demandQty" {...field} />
          <Field<ShelfLifeForm> id="shelf-life" label="Required shelf life (days)" field="requiredShelfLifeDays" {...field} />
          <Field<ShelfLifeForm>
            id="shelf-horizon"
            label="List batches expiring by day"
            field="horizonDay"
            optional
            {...field}
          />
          <Field<ShelfLifeForm>
            id="shelf-batches"
            label="Batches"
            field="batches"
            hint="One per line: id, qty, expiry day (days from today)."
            multiline
            {...field}
          />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={calc.busy}>
              Check batches
            </Button>
          </div>
        </form>
        <Failure error={calc.error} title="The shelf-life check failed" />
        {r || calc.busy ? (
          <>
            {r ? (
              <p role="status" className={styles.summary}>
                {r.allocations.length === 0 ? "No batch can serve this demand." : `${r.allocations.length} batches pegged.`} Shortfall{" "}
                {r.shortfallQty}
              </p>
            ) : null}
            <DataTable
              label="Batch allocations"
              loading={calc.busy}
              rows={r?.allocations ?? []}
              getRowKey={(a) => a.batchId}
              columns={[
                { key: "batchId", header: "Batch", mono: true },
                { key: "qty", header: "Pegged qty", align: "end" },
              ]}
              empty={{ title: "No usable batch", description: "Planned supply has to cover the whole demand." }}
            />
            {r && r.expiringUnpegged.length > 0 ? (
              <DataTable
                label="Batches expiring unpegged"
                rows={r.expiringUnpegged}
                getRowKey={(b) => b.batchId}
                columns={[
                  { key: "batchId", header: "Batch", mono: true },
                  { key: "qty", header: "Qty left", align: "end" },
                  { key: "expiryDay", header: "Expires on day", align: "end" },
                ]}
              />
            ) : null}
          </>
        ) : null}
      </div>
    </Card>
  );
}

/**
 * Planning calculators: the DDMRP, quota, strategy, shelf-life and supply
 * schedule rules of PSG-22 slice 3, run on figures the planner enters. The
 * service stores nothing for them.
 */
export function CalculatorsPage() {
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Planning calculators"
        description="Try the buffer, quota, forecast-consumption, supply-schedule and shelf-life rules on your own figures. Nothing is saved."
      />
      <BufferCalculator />
      <QuotaCalculator />
      <ConsumptionCalculator />
      <SupplyScheduleCalculator />
      <ShelfLifeCalculator />
    </div>
  );
}
