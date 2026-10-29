import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import {
  backlog,
  costByAsset,
  listEmergencyOverdue,
  listPlanReviews,
  type AssetCost,
  type BacklogRow,
  type PlanReview,
  type RegularizationAlert,
} from "./api/client";
import { CODE, dayOf, DATE, minuteOf, money, statusLabel } from "./format";
import { Failure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** The default cost window: the 30 days up to today, as YYYY-MM-DD in UTC. */
export function defaultWindow(today: Date): { from: string; to: string } {
  const to = today.toISOString().slice(0, 10);
  const from = new Date(today.getTime() - 30 * 24 * 3600 * 1000).toISOString().slice(0, 10);
  return { from, to };
}

interface Filter {
  from: string;
  to: string;
  assetType: string;
}

/** Checks the cost filter. The window is [from, to + 1 day) so the "to" day is included. */
export function validateFilter(values: Filter): { errors: Partial<Filter>; query?: { from: string; to: string; assetType: string } } {
  const errors: Partial<Filter> = {};
  if (!DATE.test(values.from)) errors.from = "Enter the first day as YYYY-MM-DD.";
  if (!DATE.test(values.to)) errors.to = "Enter the last day as YYYY-MM-DD.";
  else if (DATE.test(values.from) && values.to < values.from) errors.to = "The last day must not be before the first.";
  const assetType = values.assetType.trim().toUpperCase();
  if (assetType && !CODE.test(assetType)) errors.assetType = "Use the asset type code, for example PUMP.";
  if (Object.keys(errors).length > 0) return { errors };
  const end = new Date(`${values.to}T00:00:00Z`);
  end.setUTCDate(end.getUTCDate() + 1);
  return { errors, query: { from: `${values.from}T00:00:00Z`, to: end.toISOString().replace(".000Z", "Z"), assetType } };
}

const costColumns: DataTableColumn<AssetCost & { id: string }>[] = [
  { key: "assetNumber", header: "Asset", mono: true, render: (row) => <Link to={`/eam/assets/${row.assetId}`}>{row.assetNumber}</Link> },
  { key: "name", header: "Name" },
  { key: "assetType", header: "Type", mono: true },
  { key: "workOrders", header: "Work orders", align: "end" },
  { key: "partsCost", header: "Parts", align: "end", mono: true, render: (row) => money(row.partsCost) },
  { key: "laborCost", header: "Labor", align: "end", mono: true, render: (row) => money(row.laborCost) },
  { key: "total", header: "Total", align: "end", mono: true, render: (row) => `${money(row.total)} ${row.currency}` },
];

const backlogColumns: DataTableColumn<BacklogRow & { id: string }>[] = [
  { key: "priority", header: "Priority", align: "end", render: (row) => (row.priority === 1 ? "1 (emergency)" : row.priority) },
  { key: "openCount", header: "Open work orders", align: "end" },
  { key: "overdueCount", header: "Overdue", align: "end" },
  { key: "oldestDueAt", header: "Oldest due", mono: true, render: (row) => dayOf(row.oldestDueAt) },
];

const alertColumns: DataTableColumn<RegularizationAlert>[] = [
  { key: "number", header: "Work order", mono: true, render: (row) => <Link to={`/eam/work-orders/${row.id}`}>{row.number}</Link> },
  { key: "status", header: "Status", render: (row) => statusLabel[row.status] ?? row.status },
  { key: "createdAt", header: "Raised", mono: true, render: (row) => minuteOf(row.createdAt) },
  { key: "detail", header: "Warning", render: (row) => `${row.detail} (${row.code})` },
];

const reviewColumns: DataTableColumn<PlanReview & { id: string }>[] = [
  { key: "assetNumber", header: "Asset", mono: true, render: (row) => <Link to={`/eam/assets/${row.assetId}`}>{row.assetNumber}</Link> },
  { key: "name", header: "Name" },
  { key: "criticality", header: "Criticality", align: "end" },
  { key: "lastReviewedAt", header: "Last assessed", mono: true, render: (row) => (row.lastReviewedAt ? dayOf(row.lastReviewedAt) : "Never") },
  { key: "dueAt", header: "Review due", mono: true, render: (row) => (row.dueAt ? dayOf(row.dueAt) : "Now") },
];

/** KPI dashboard (8.44.6, FR-44.13) with the two reminders of this slice: emergency regularization (BR-44.7) and plan reviews (BR-44.15). */
export function KpiPage() {
  const [values, setValues] = useState<Filter>(() => ({ ...defaultWindow(new Date()), assetType: "" }));
  const [errors, setErrors] = useState<Partial<Filter>>({});
  const [applied, setApplied] = useState(() => validateFilter({ ...defaultWindow(new Date()), assetType: "" }).query!);
  const [hoursText, setHoursText] = useState("24");
  const [hours, setHours] = useState(24);
  const [hoursError, setHoursError] = useState<string | undefined>();

  const costs = useLoad((signal) => costByAsset(applied.from, applied.to, applied.assetType, signal), `cost|${applied.from}|${applied.to}|${applied.assetType}`);
  const open = useLoad((signal) => backlog(signal), "backlog");
  const alerts = useLoad((signal) => listEmergencyOverdue(hours, signal), `overdue|${hours}`);
  const reviews = useLoad((signal) => listPlanReviews(signal), "reviews");

  function apply(event: FormEvent) {
    event.preventDefault();
    const { errors: found, query } = validateFilter(values);
    setErrors(found);
    if (query) setApplied(query);
  }

  function applyHours(event: FormEvent) {
    event.preventDefault();
    const n = Number(hoursText);
    if (!/^\d{1,2}$/.test(hoursText.trim()) || n < 1 || n > 72) {
      setHoursError("Enter 1 to 72 hours.");
      return;
    }
    setHoursError(undefined);
    setHours(n);
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Maintenance KPIs" description="Cost per asset, the open backlog, and the reminders that need a planner: emergency orders to regularize and critical assets due for a plan review." />
      <Card title="Cost per asset">
        <div className={styles.stack}>
          <form className={styles.toolbar} onSubmit={apply} noValidate aria-label="Cost filter">
            <FormField id="kpi-from" label="First day" error={errors.from}>
              {(control) => <Input {...control} type="date" value={values.from} invalid={Boolean(errors.from)} onChange={(e) => setValues({ ...values, from: e.target.value })} />}
            </FormField>
            <FormField id="kpi-to" label="Last day" error={errors.to}>
              {(control) => <Input {...control} type="date" value={values.to} invalid={Boolean(errors.to)} onChange={(e) => setValues({ ...values, to: e.target.value })} />}
            </FormField>
            <FormField id="kpi-type" label="Asset type" optional error={errors.assetType}>
              {(control) => <Input {...control} value={values.assetType} maxLength={20} invalid={Boolean(errors.assetType)} onChange={(e) => setValues({ ...values, assetType: e.target.value })} />}
            </FormField>
            <Button type="submit">Show costs</Button>
          </form>
          {costs.error ? (
            <Failure title="Could not load the costs" problem={costs.error} retry={costs.reload} />
          ) : (
            <DataTable
              label="Cost per asset"
              columns={costColumns}
              rows={(costs.data ?? []).map((c) => ({ ...c, id: `${c.assetId}|${c.currency}` }))}
              getRowKey={(row) => row.id}
              loading={costs.loading}
              empty={{ title: "No completed work orders in this window", description: "Costs count work orders completed or closed between the two days." }}
            />
          )}
          <p className={styles.note}>Parts and labor as booked on the work orders. External services, overhead and budget comparison come with the ledger posting.</p>
        </div>
      </Card>
      <Card title="Backlog by priority">
        {open.error ? (
          <Failure title="Could not load the backlog" problem={open.error} retry={open.reload} />
        ) : (
          <DataTable
            label="Backlog by priority"
            columns={backlogColumns}
            rows={(open.data ?? []).map((b) => ({ ...b, id: String(b.priority) }))}
            getRowKey={(row) => row.id}
            loading={open.loading}
            empty={{ title: "No open work orders", description: "Every work order is completed, closed or canceled." }}
          />
        )}
      </Card>
      <Card title="Emergency orders to regularize">
        <div className={styles.stack}>
          <form className={styles.toolbar} onSubmit={applyHours} noValidate aria-label="Regularization window">
            <FormField id="kpi-hours" label="Window in hours" hint="Emergency orders never scheduled or released after this long." error={hoursError}>
              {(control) => <Input {...control} inputMode="numeric" value={hoursText} invalid={Boolean(hoursError)} onChange={(e) => setHoursText(e.target.value)} />}
            </FormField>
            <Button type="submit">Check</Button>
          </form>
          {alerts.error ? (
            <Failure title="Could not load the emergency orders" problem={alerts.error} retry={alerts.reload} />
          ) : (
            <DataTable
              label="Emergency orders to regularize"
              columns={alertColumns}
              rows={alerts.data ?? []}
              getRowKey={(row) => row.id}
              loading={alerts.loading}
              empty={{ title: "Nothing to regularize", description: `Every emergency order raised more than ${hours} h ago has been scheduled or released.` }}
            />
          )}
        </div>
      </Card>
      <Card title="Plan reviews due">
        {reviews.error ? (
          <Failure title="Could not load the plan reviews" problem={reviews.error} retry={reviews.reload} />
        ) : (
          <div className={styles.stack}>
            <DataTable
              label="Plan reviews due"
              columns={reviewColumns}
              rows={(reviews.data ?? []).map((r) => ({ ...r, id: r.assetId }))}
              getRowKey={(row) => row.id}
              loading={reviews.loading}
              empty={{ title: "No plan reviews due", description: "Assets of criticality 4 or 5 are reviewed every 12 months." }}
            />
            <p className={styles.note}>Record a new criticality assessment on the asset's page to log the review.</p>
          </div>
        )}
      </Card>
    </div>
  );
}
