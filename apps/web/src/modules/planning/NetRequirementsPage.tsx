import { useState, type FormEvent } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  StatusPill,
  type DataTableColumn,
} from "../../shell/ui";
import { listNetRequirements, listPlannedOrders, type NetRequirement, type PlannedOrder } from "./api/client";
import { DEFAULT_PLAN_CODE, PLAN_CODE, useLoad, withPlan, type LoadError } from "./hooks";
import styles from "./pages.module.css";

const UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const dash = "—";

/** Drops trailing decimal zeros of a decimal string ("400.000000" → "400") without going through a float. */
export function trimQty(qty: string): string {
  return qty.includes(".") ? qty.replace(/\.?0+$/, "") : qty;
}

const isNegative = (qty: string) => qty.startsWith("-") && trimQty(qty) !== "-0";

const columns: DataTableColumn<NetRequirement>[] = [
  { key: "date", header: "Date" },
  { key: "grossRequirements", header: "Gross requirements", align: "end", render: (r) => trimQty(r.grossRequirements) },
  { key: "scheduledReceipts", header: "Scheduled receipts", align: "end", render: (r) => trimQty(r.scheduledReceipts) },
  { key: "plannedReceipts", header: "Planned receipts", align: "end", render: (r) => trimQty(r.plannedReceipts) },
  {
    key: "projectedAvailableBalance",
    header: "Projected balance",
    align: "end",
    render: (r) =>
      isNegative(r.projectedAvailableBalance) ? (
        <Badge tone="danger">{trimQty(r.projectedAvailableBalance)}</Badge>
      ) : (
        trimQty(r.projectedAvailableBalance)
      ),
  },
  {
    key: "netRequirement",
    header: "Net requirement",
    align: "end",
    render: (r) => (trimQty(r.netRequirement) === "0" ? dash : trimQty(r.netRequirement)),
  },
];

const orderColumns: DataTableColumn<PlannedOrder>[] = [
  { key: "number", header: "Number", mono: true },
  { key: "warehouseId", header: "Scope", mono: true, render: (o) => (o.warehouseId ? `Warehouse ${o.warehouseId}` : "Site") },
  { key: "qty", header: "Quantity", align: "end", render: (o) => trimQty(o.qty) },
  { key: "orderDate", header: "Order date" },
  { key: "deliveryDate", header: "Delivery date" },
  { key: "futuresDate", header: "Expected", render: (o) => o.futuresDate ?? dash },
  { key: "status", header: "Status", render: (o) => <StatusPill status={o.status} /> },
];

function LoadAlert({ what, error }: { what: string; error: LoadError | null }) {
  return (
    <Alert tone="danger" title={error ? `Could not load ${what}` : undefined} live="alert">
      {error ? `${error.message} (${error.code})` : null}
    </Alert>
  );
}

/**
 * The plan, item and warehouse scope in view, kept in `?plan=`, `?item=` and `?warehouse=` so links and
 * reloads keep them. No warehouse is the item's site scope.
 */
function useSelection(): { plan: string; item: string; warehouse: string; select: (plan: string, item: string, warehouse: string) => void } {
  const [params, setParams] = useSearchParams();
  const rawPlan = params.get("plan") ?? DEFAULT_PLAN_CODE;
  const plan = PLAN_CODE.test(rawPlan) ? rawPlan : DEFAULT_PLAN_CODE;
  const rawItem = params.get("item") ?? "";
  const item = UUID.test(rawItem) ? rawItem.toLowerCase() : "";
  const rawWarehouse = params.get("warehouse") ?? "";
  const warehouse = UUID.test(rawWarehouse) ? rawWarehouse.toLowerCase() : "";
  const select = (nextPlan: string, nextItem: string, nextWarehouse: string) => {
    const next = new URLSearchParams(params);
    next.set("plan", nextPlan);
    next.set("item", nextItem);
    if (nextWarehouse) next.set("warehouse", nextWarehouse);
    else next.delete("warehouse");
    setParams(next);
  };
  return { plan, item, warehouse, select };
}

function SelectionForm({
  plan,
  item,
  warehouse,
  onSelect,
}: {
  plan: string;
  item: string;
  warehouse: string;
  onSelect: (plan: string, item: string, warehouse: string) => void;
}) {
  const [draft, setDraft] = useState({ plan, item, warehouse });
  const [errors, setErrors] = useState<{ plan?: string; item?: string; warehouse?: string }>({});
  function submit(e: FormEvent) {
    e.preventDefault();
    const code = draft.plan.trim().toUpperCase();
    const id = draft.item.trim();
    const wh = draft.warehouse.trim();
    const found: { plan?: string; item?: string; warehouse?: string } = {};
    if (!PLAN_CODE.test(code)) found.plan = "Use 2 to 20 capital letters, digits, _ or -.";
    if (!UUID.test(id)) found.item = "Enter the item id as a UUID.";
    if (wh !== "" && !UUID.test(wh)) found.warehouse = "Enter the warehouse id as a UUID, or leave it empty for the site.";
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setDraft({ plan: code, item: id, warehouse: wh });
    onSelect(code, id.toLowerCase(), wh.toLowerCase());
  }
  return (
    <form className={styles.toolbar} onSubmit={submit} noValidate aria-label="Choose plan and item">
      <div className={styles.toolbarField}>
        <FormField id="netreq-plan" label="Plan" error={errors.plan}>
          {(a11y) => <Input {...a11y} value={draft.plan} maxLength={20} onChange={(e) => setDraft((d) => ({ ...d, plan: e.target.value }))} />}
        </FormField>
      </div>
      <div className={styles.toolbarFieldWide}>
        <FormField id="netreq-item" label="Item" error={errors.item}>
          {(a11y) => (
            <Input {...a11y} className={styles.mono} value={draft.item} onChange={(e) => setDraft((d) => ({ ...d, item: e.target.value }))} />
          )}
        </FormField>
      </div>
      <div className={styles.toolbarFieldWide}>
        <FormField id="netreq-warehouse" label="Warehouse scope (empty: site)" error={errors.warehouse}>
          {(a11y) => (
            <Input {...a11y} className={styles.mono} value={draft.warehouse} onChange={(e) => setDraft((d) => ({ ...d, warehouse: e.target.value }))} />
          )}
        </FormField>
      </div>
      <Button type="submit">Show net requirements</Button>
    </form>
  );
}

function ItemView({ plan, item, warehouse }: { plan: string; item: string; warehouse: string }) {
  const navigate = useNavigate();
  const rows = useLoad((signal) => listNetRequirements(plan, item, { signal }, warehouse || undefined), [plan, item, warehouse]);
  const orders = useLoad(
    (signal) => listPlannedOrders({ planCode: plan, itemId: item, ...(warehouse ? { warehouseId: warehouse } : {}) }, { signal }),
    [plan, item, warehouse],
  );
  return (
    <>
      <LoadAlert what="net requirements" error={rows.error} />
      {rows.error ? null : (
        <DataTable
          label="Net requirements"
          columns={columns}
          rows={rows.data ?? []}
          getRowKey={(r) => r.date}
          loading={rows.loading}
          empty={{
            title: "No net requirements",
            description: warehouse
              ? `The current version of plan ${plan} has no demand or supply for this item in that warehouse. Check the warehouse, or run the plan with a warehouse scope for it.`
              : `The current version of plan ${plan} has no demand or supply for this item. Check the item, or run the plan.`,
          }}
        />
      )}
      <Card title={warehouse ? "Planned orders of this item in the warehouse" : "Planned orders of this item"}>
        <p className={styles.summary}>Open an order to see the demand it is pegged to.</p>
        <LoadAlert what="planned orders" error={orders.error} />
        {orders.error ? null : (
          <DataTable
            label="Planned orders of the item"
            columns={orderColumns}
            rows={orders.data ?? []}
            getRowKey={(o) => o.id}
            loading={orders.loading}
            empty={{ title: "No planned orders", description: "Existing supply and on-hand cover this item." }}
            onRowOpen={(o) => navigate(withPlan(`/planning/planned-orders/${o.id}`, plan))}
          />
        )}
      </Card>
    </>
  );
}

/**
 * Net requirements (MD04, FR-22.12): the time-phased view of one item in the
 * current plan version, with the item's planned orders to drill into pegging.
 */
export function NetRequirementsPage() {
  const { plan, item, warehouse, select } = useSelection();
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Net requirements"
        description={`Gross requirements, receipts and projected available balance per day of one item in the current version of plan ${plan}. A warehouse planned in its own scope is netted apart from the site.`}
      />
      <SelectionForm key={`${plan}/${item}/${warehouse}`} plan={plan} item={item} warehouse={warehouse} onSelect={select} />
      {item ? (
        <ItemView plan={plan} item={item} warehouse={warehouse} />
      ) : (
        <Card>
          <EmptyState
            compact
            icon="grid"
            title="Choose an item"
            description="Enter the item id to see its demand, supply and projected balance day by day."
          />
        </Card>
      )}
    </div>
  );
}
