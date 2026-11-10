import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  createWorkOrder,
  describeProblem,
  listAssets,
  listWorkOrders,
  newKey,
  type NewWorkOrder,
  type Problem,
  type WorkOrder,
  type WorkOrderType,
} from "./api/client";
import { addMoney, CODE, CURRENCY, dayOf, typeLabel } from "./format";
import { Failure, PagedTable, State, StatusFilter } from "./parts";
import styles from "./pages.module.css";
import { useLoad, usePaged } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<WorkOrder>[] = [
  { key: "number", header: "Work order", mono: true },
  { key: "workOrderType", header: "Type", render: (row) => typeLabel[row.workOrderType] ?? row.workOrderType },
  { key: "status", header: "Status", render: (row) => <State value={row.status} /> },
  { key: "priority", header: "Priority", align: "end" },
  { key: "jobType", header: "Job type", mono: true },
  { key: "dueAt", header: "Due", mono: true, render: (row) => dayOf(row.dueAt) },
  { key: "cost", header: "Cost", align: "end", mono: true, render: (row) => `${addMoney(row.partsCost, row.laborCost)} ${row.currency}` },
];

const STATUSES = ["CREATED", "SCHEDULED", "RELEASED", "IN_PROGRESS", "ON_HOLD", "COMPLETED", "CLOSED", "CANCELED"];

/** Work order list (8.44.14), newest first, filterable by status and asset. */
export function WorkOrdersPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const status = params.get("status") ?? "";
  const asset = params.get("asset") ?? "";
  const state = usePaged((cursor, signal) => listWorkOrders({ status, asset, cursor }, signal), `orders|${status}|${asset}`);

  function filter(next: string) {
    const p: Record<string, string> = {};
    if (next) p.status = next;
    if (asset) p.asset = asset;
    setParams(p);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Work orders"
        description="Preventive orders from plans, corrective orders from requests, and orders you raise yourself."
        actions={
          <Link className={buttonClassName("primary")} to={asset ? `/eam/work-orders/new?asset=${asset}` : "/eam/work-orders/new"}>
            New work order
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <StatusFilter id="wo-status" value={status} options={STATUSES} onChange={filter} />
        {asset ? (
          <p className={styles.note}>
            Showing one asset. <Link to={status ? `/eam/work-orders?status=${status}` : "/eam/work-orders"}>Show every asset</Link>
          </p>
        ) : null}
      </div>
      <PagedTable
        label="Work orders"
        noun="work orders"
        columns={columns}
        state={state}
        empty={status || asset ? { title: "No work orders match", description: "Choose another status." } : { title: "No work orders yet", description: "Create one, convert a request, or add a maintenance plan." }}
        onRowOpen={(row) => navigate(`/eam/work-orders/${row.id}`)}
      />
    </div>
  );
}

interface OrderValues {
  assetId: string;
  workOrderType: WorkOrderType;
  priority: string;
  currency: string;
  jobType: string;
  dueAt: string;
}

type OrderErrors = Partial<Record<keyof OrderValues, string>>;

/** The checks the service makes on a new work order, and the request the form sends. */
export function validateWorkOrder(values: OrderValues): { errors: OrderErrors; input?: NewWorkOrder } {
  const errors: OrderErrors = {};
  if (!values.assetId) errors.assetId = "Choose an active asset.";
  const currency = values.currency.trim().toUpperCase();
  if (!CURRENCY.test(currency)) errors.currency = "Enter a three-letter currency code.";
  const jobType = values.jobType.trim().toUpperCase();
  if (jobType && !CODE.test(jobType)) errors.jobType = "Use 2 to 20 characters: A to Z, 0 to 9 or an underscore.";
  if (values.dueAt && !/^\d{4}-\d{2}-\d{2}$/.test(values.dueAt)) errors.dueAt = "Enter the date as YYYY-MM-DD.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewWorkOrder = { assetId: values.assetId, workOrderType: values.workOrderType, currency };
  if (values.priority && values.workOrderType !== "EMERGENCY") input.priority = Number(values.priority);
  if (jobType) input.jobType = jobType;
  if (values.dueAt) input.dueAt = `${values.dueAt}T00:00:00Z`;
  return { errors, input };
}

/** Raise a work order on an active asset (FR-44.7). */
export function NewWorkOrderPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const assets = useLoad((signal) => listAssets({ status: "ACTIVE", limit: 200 }, signal), "active-assets");
  const [values, setValues] = useState<OrderValues>({
    assetId: params.get("asset") ?? "",
    workOrderType: "CORRECTIVE",
    priority: "",
    currency: "USD",
    jobType: "",
    dueAt: "",
  });
  const [errors, setErrors] = useState<OrderErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<OrderValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateWorkOrder(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const wo = await createWorkOrder(input, key.current);
      navigate(`/eam/work-orders/${wo.id}`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New work order" description="The order starts in Created. Schedule and release it from its page." />
      <Card>
        {assets.error ? (
          <Failure title="Could not load the active assets" problem={assets.error} retry={assets.reload} />
        ) : assets.loading || !assets.data ? (
          <div aria-busy="true">
            <Skeleton height="control" />
          </div>
        ) : assets.data.items.length === 0 ? (
          <Alert tone="info" title="No active assets">
            Work orders need an active asset. <Link to="/eam/assets">Open assets</Link> and activate one first.
          </Alert>
        ) : (
          <form className={styles.form} onSubmit={submit} noValidate>
            <FormField id="wo-asset" label="Asset" error={errors.assetId}>
              {(control) => (
                <Select {...control} value={values.assetId} invalid={Boolean(errors.assetId)} onChange={(e) => update({ assetId: e.target.value })}>
                  <option value="">Choose an asset</option>
                  {assets.data!.items.map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.assetNumber} {a.name}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="wo-type" label="Type">
              {(control) => (
                <Select {...control} value={values.workOrderType} onChange={(e) => update({ workOrderType: e.target.value as WorkOrderType })}>
                  {(["CORRECTIVE", "PREVENTIVE", "INSPECTION", "EMERGENCY"] as const).map((t) => (
                    <option key={t} value={t}>
                      {typeLabel[t]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="wo-priority" label="Priority" hint="Leave as default to use the asset's criticality. Emergency orders are always priority 1." optional>
              {(control) => (
                <Select {...control} value={values.priority} disabled={values.workOrderType === "EMERGENCY"} onChange={(e) => update({ priority: e.target.value })}>
                  <option value="">Default</option>
                  {[1, 2, 3, 4, 5].map((n) => (
                    <option key={n} value={String(n)}>
                      {n}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="wo-currency" label="Currency" error={errors.currency}>
              {(control) => <Input {...control} value={values.currency} maxLength={3} invalid={Boolean(errors.currency)} onChange={(e) => update({ currency: e.target.value })} />}
            </FormField>
            <FormField id="wo-jobtype" label="Job type" hint="A code such as LUBE or REPAIR." optional error={errors.jobType}>
              {(control) => <Input {...control} value={values.jobType} maxLength={20} invalid={Boolean(errors.jobType)} onChange={(e) => update({ jobType: e.target.value })} />}
            </FormField>
            <FormField id="wo-due" label="Due date" optional error={errors.dueAt}>
              {(control) => <Input {...control} type="date" value={values.dueAt} invalid={Boolean(errors.dueAt)} onChange={(e) => update({ dueAt: e.target.value })} />}
            </FormField>
            {failure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="Could not create the work order" live="alert">
                  <ProblemMessage problem={failure} />
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Create work order
              </Button>
              <Link className={buttonClassName("ghost")} to="/eam/work-orders">
                Cancel
              </Link>
            </div>
          </form>
        )}
      </Card>
    </div>
  );
}
