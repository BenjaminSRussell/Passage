import { useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { getPlanProjection, type PlanProjection, type ProjectionPeriod } from "./api/client";
import { DEFAULT_PLAN_CODE, PLAN_CODE, useLoad } from "./hooks";
import { trimQty } from "./NetRequirementsPage";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const DECIMAL = /^\d{1,12}(\.\d{1,6})?$/;
const CURRENCY = /^[A-Z]{3}$/;
const dash = "—";
export const MAX_ITEMS = 20;
const BUCKETS = ["day", "week", "month"] as const;
type Bucket = (typeof BUCKETS)[number];

/** What the page reads from `?plan=&items=&bucket=&horizon=&cost=&currency=`; the URL is the state. */
export interface ScheduleSelection {
  plan: string;
  items: string[];
  bucket: Bucket;
  horizon: number;
  cost: string;
  currency: string;
}

export function readSelection(params: URLSearchParams): ScheduleSelection {
  const rawPlan = params.get("plan") ?? DEFAULT_PLAN_CODE;
  const rawBucket = params.get("bucket") ?? "week";
  const horizon = Number(params.get("horizon") ?? "84");
  const items = (params.get("items") ?? "")
    .split(/[\s,]+/)
    .filter((id) => UUID.test(id))
    .map((id) => id.toLowerCase());
  return {
    plan: PLAN_CODE.test(rawPlan) ? rawPlan : DEFAULT_PLAN_CODE,
    items: [...new Set(items)].slice(0, MAX_ITEMS),
    bucket: (BUCKETS as readonly string[]).includes(rawBucket) ? (rawBucket as Bucket) : "week",
    horizon: Number.isInteger(horizon) && horizon >= 1 && horizon <= 730 ? horizon : 84,
    cost: DECIMAL.test(params.get("cost") ?? "") ? (params.get("cost") as string) : "",
    currency: CURRENCY.test(params.get("currency") ?? "") ? (params.get("currency") as string) : "",
  };
}

interface Draft {
  plan: string;
  items: string;
  bucket: Bucket;
  horizon: string;
  cost: string;
  currency: string;
}

type DraftErrors = Partial<Record<keyof Draft, string>>;

/** Field checks for the selection form; the service repeats them. */
export function validateDraft(d: Draft): DraftErrors {
  const e: DraftErrors = {};
  if (!PLAN_CODE.test(d.plan.trim().toUpperCase())) e.plan = "Use 2 to 20 capital letters, digits, _ or -.";
  const ids = d.items.split(/[\s,]+/).filter(Boolean);
  if (ids.length === 0) e.items = "Enter at least one item id.";
  else if (ids.some((id) => !UUID.test(id))) e.items = "Every item id must be a UUID, separated by commas or spaces.";
  else if (new Set(ids.map((id) => id.toLowerCase())).size > MAX_ITEMS) e.items = `Choose at most ${MAX_ITEMS} items.`;
  const horizon = Number(d.horizon);
  if (!Number.isInteger(horizon) || horizon < 1 || horizon > 730) e.horizon = "Enter whole days from 1 to 730.";
  if (d.cost !== "" && !DECIMAL.test(d.cost)) e.cost = "Enter 0 or more, with up to 6 decimals.";
  if (d.currency !== "" && !CURRENCY.test(d.currency)) e.currency = "Enter a three-letter currency code, such as EUR.";
  if ((d.cost === "") !== (d.currency === "")) e.currency = "Give the unit cost and the currency together, or neither.";
  return e;
}

function SelectionForm({ selection, onSelect }: { selection: ScheduleSelection; onSelect: (next: ScheduleSelection) => void }) {
  const [draft, setDraft] = useState<Draft>({
    plan: selection.plan,
    items: selection.items.join("\n"),
    bucket: selection.bucket,
    horizon: String(selection.horizon),
    cost: selection.cost,
    currency: selection.currency,
  });
  const [errors, setErrors] = useState<DraftErrors>({});
  const set = <K extends keyof Draft>(key: K, value: Draft[K]) => setDraft((d) => ({ ...d, [key]: value }));

  function submit(e: FormEvent) {
    e.preventDefault();
    const found = validateDraft(draft);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    onSelect({
      plan: draft.plan.trim().toUpperCase(),
      items: [...new Set(draft.items.split(/[\s,]+/).filter(Boolean).map((id) => id.toLowerCase()))],
      bucket: draft.bucket,
      horizon: Number(draft.horizon),
      cost: draft.cost,
      currency: draft.currency,
    });
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Choose plan and items">
      <FormField id="sched-plan" label="Plan" error={errors.plan}>
        {(a11y) => <Input {...a11y} value={draft.plan} maxLength={20} onChange={(e) => set("plan", e.target.value)} />}
      </FormField>
      <FormField id="sched-items" label="Items" hint={`Item ids, up to ${MAX_ITEMS}, separated by commas or spaces.`} error={errors.items}>
        {(a11y) => <Input {...a11y} className={styles.mono} value={draft.items} onChange={(e) => set("items", e.target.value)} />}
      </FormField>
      <FormField id="sched-bucket" label="Period">
        {(a11y) => (
          <Select {...a11y} value={draft.bucket} onChange={(e) => set("bucket", e.target.value as Bucket)}>
            <option value="day">Day</option>
            <option value="week">Week</option>
            <option value="month">Calendar month</option>
          </Select>
        )}
      </FormField>
      <FormField id="sched-horizon" label="Horizon (days)" error={errors.horizon}>
        {(a11y) => <Input {...a11y} inputMode="numeric" value={draft.horizon} onChange={(e) => set("horizon", e.target.value)} />}
      </FormField>
      <FormField id="sched-cost" label="Unit cost" hint="Optional; values each period's balance." error={errors.cost}>
        {(a11y) => <Input {...a11y} inputMode="decimal" value={draft.cost} onChange={(e) => set("cost", e.target.value)} />}
      </FormField>
      <FormField id="sched-currency" label="Currency" error={errors.currency}>
        {(a11y) => <Input {...a11y} maxLength={3} value={draft.currency} onChange={(e) => set("currency", e.target.value.toUpperCase())} />}
      </FormField>
      <div className={styles.formActions}>
        <Button type="submit" variant="primary">
          Show schedule
        </Button>
      </div>
    </form>
  );
}

/** Period cell: the day, or the first and last day of a longer bucket. */
export function periodCell(p: Pick<ProjectionPeriod, "startDate" | "endDate">): string {
  return p.startDate === p.endDate ? p.startDate : `${p.startDate} to ${p.endDate}`;
}

/** Days-of-supply cell: "n days", or a dash when no demand follows the bucket. */
export function daysCell(value: string | null): string {
  return value === null ? dash : `${trimQty(value)} days`;
}

function columns(currency: string | null): DataTableColumn<ProjectionPeriod>[] {
  return [
    { key: "startDate", header: "Period", render: periodCell },
    { key: "demand", header: "Demand", align: "end", render: (p) => trimQty(p.demand) },
    { key: "supply", header: "Receipts", align: "end", render: (p) => trimQty(p.supply) },
    { key: "planned", header: "Planned", align: "end", render: (p) => trimQty(p.planned) },
    {
      key: "closingOnHand",
      header: "Projected balance",
      align: "end",
      render: (p) => (p.shortage ? <Badge tone="danger">{trimQty(p.closingOnHand)}</Badge> : trimQty(p.closingOnHand)),
    },
    { key: "value", header: currency ? `Value (${currency})` : "Value", align: "end", render: (p) => p.value ?? dash },
    { key: "daysOfSupply", header: "Days of supply", align: "end", render: (p) => daysCell(p.daysOfSupply) },
  ];
}

function Schedules({ data }: { data: PlanProjection }) {
  return (
    <>
      <p className={styles.summary}>
        Version {data.version} of plan {data.planCode} for site {data.siteId}, as of {data.asOf}. A shortage is a balance below zero; the run has already held back the safety stock.
      </p>
      {data.items.map((item) => (
        <Card key={item.itemId} title={item.itemId}>
          {item.planned ? (
            <>
              <p className={styles.summary}>
                Opening on-hand {trimQty(item.openingOnHand)}
                {item.shortagePeriods > 0 ? `; ${item.shortagePeriods} ${item.shortagePeriods === 1 ? "period" : "periods"} short.` : "; no shortages."}
              </p>
              <DataTable
                label={`Supply schedule of ${item.itemId}`}
                columns={columns(data.currency)}
                rows={item.periods}
                getRowKey={(p) => String(p.startDay)}
                empty={{ title: "No periods in the horizon" }}
              />
            </>
          ) : (
            <EmptyState
              compact
              icon="grid"
              title="Not in this plan"
              description="The current version has no demand or supply for this item. Check the item id, or run the plan."
            />
          )}
        </Card>
      ))}
    </>
  );
}

/**
 * Supply schedule (FR-22.21, FR-22.22): the bucketed demand, receipts, planned
 * supply, projected balance, value and days of supply of up to 20 items,
 * read from the current version of a plan.
 */
export function SupplySchedulePage() {
  const [params, setParams] = useSearchParams();
  const selection = readSelection(params);
  const key = JSON.stringify(selection);
  const state = useLoad(
    (signal) =>
      selection.items.length === 0
        ? Promise.resolve(null)
        : getPlanProjection(
            selection.plan,
            {
              itemIds: selection.items,
              bucket: selection.bucket,
              horizonDays: selection.horizon,
              unitCost: selection.cost || undefined,
              currency: selection.currency || undefined,
            },
            { signal },
          ),
    [key],
  );

  function select(next: ScheduleSelection) {
    const p = new URLSearchParams();
    p.set("plan", next.plan);
    p.set("items", next.items.join(","));
    p.set("bucket", next.bucket);
    p.set("horizon", String(next.horizon));
    if (next.cost) p.set("cost", next.cost);
    if (next.currency) p.set("currency", next.currency);
    setParams(p);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Supply schedule"
        description={`Demand, receipts, planned supply, projected balance, value and days of supply per period for items in the current version of plan ${selection.plan}.`}
      />
      <SelectionForm key={key} selection={selection} onSelect={select} />
      {selection.items.length === 0 ? (
        <Card>
          <EmptyState compact icon="grid" title="Choose items" description="Enter the item ids to see how the plan supplies them period by period." />
        </Card>
      ) : state.error ? (
        <Alert tone="danger" title="Could not load the supply schedule" live="alert">
          {<ProblemMessage problem={state.error} />}
        </Alert>
      ) : state.loading || !state.data ? (
        <Card>
          <DataTable label="Supply schedule" columns={columns(null)} rows={[]} getRowKey={(p) => String(p.startDay)} loading empty={{ title: "No periods" }} />
        </Card>
      ) : (
        <Schedules data={state.data} />
      )}
    </div>
  );
}
