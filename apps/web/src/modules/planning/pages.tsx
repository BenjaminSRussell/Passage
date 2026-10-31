import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  Alert,
  Badge,
  buttonClassName,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  StatusPill,
  type BadgeTone,
  type DataTableColumn,
} from "../../shell/ui";
import {
  getPegging,
  getPlannedOrder,
  listMessages,
  listPlannedOrders,
  listVersions,
  type Message,
  type PegNode,
  type PlannedOrder,
  type PlanVersion,
} from "./api/client";
import { FirmCard, isFirmable, rangeIds, useShiftHeld } from "./Firming";
import { PLAN_CODE, useLoad, usePlanCode, withPlan, type LoadError } from "./hooks";
import { MaintainOrderCard } from "./MaintainOrder";
import styles from "./pages.module.css";

export { DEFAULT_PLAN_CODE } from "./hooks";

/** Rows the list endpoints return at most (MaxPageSize in the service). */
export const PAGE_LIMIT = 500;

const dash = "—";

const ORDER_STATUSES = ["unprocessed", "approved", "firmed"] as const;
const MESSAGE_ACTIONS = ["delay", "postpone", "advance", "shortage", "cancel", "decrease", "increase"] as const;

const orderTypeLabel: Record<PlannedOrder["orderType"], string> = {
  purchase: "Purchase",
  production: "Production",
  transfer: "Transfer",
};

const actionTone: Record<Message["action"], BadgeTone> = {
  delay: "danger",
  shortage: "warning",
  advance: "info",
  postpone: "info",
  cancel: "warning",
  decrease: "warning",
  increase: "warning",
};

const targetLabel: Record<NonNullable<PlannedOrder["firmedTargetType"]>, string> = {
  "purchase-order": "Purchase order",
  "production-order": "Production order",
  "transfer-order": "Transfer order",
};

const sentence = (s: string) => s.charAt(0).toUpperCase() + s.slice(1).replace(/-/g, " ");

/** Counts planned orders per type and how many are delayed (the workspace tiles). */
export function summarize(orders: PlannedOrder[], messages: Message[]) {
  const byType = { purchase: 0, production: 0, transfer: 0 };
  let delayed = 0;
  for (const o of orders) {
    byType[o.orderType] += 1;
    if (o.delayDays > 0) delayed += 1;
  }
  return { byType, delayed, messages: messages.length };
}

function LoadAlert({ what, error }: { what: string; error: LoadError | null }) {
  return (
    <Alert tone="danger" title={error ? `Could not load ${what}` : undefined} live="alert">
      {error ? `${error.message} (${error.code})` : null}
    </Alert>
  );
}

/** Picks the plan every planning page shows; the choice lives in `?plan=`. */
export function PlanPicker({ children }: { children?: ReactNode }) {
  const [plan, setPlan] = usePlanCode();
  const [draft, setDraft] = useState(plan);
  const [error, setError] = useState<string | undefined>();
  function submit(e: FormEvent) {
    e.preventDefault();
    const code = draft.trim().toUpperCase();
    if (!PLAN_CODE.test(code)) {
      setError("Use 2 to 20 capital letters, digits, _ or -.");
      return;
    }
    setError(undefined);
    setDraft(code);
    setPlan(code);
  }
  return (
    <form className={styles.toolbar} onSubmit={submit} aria-label="Choose plan">
      <div className={styles.toolbarField}>
        <FormField id="planning-plan" label="Plan" error={error}>
          {(a11y) => <Input {...a11y} value={draft} maxLength={20} onChange={(e) => setDraft(e.target.value)} />}
        </FormField>
      </div>
      <Button type="submit">Show plan</Button>
      {children}
    </form>
  );
}

function Details({ entries }: { entries: [string, ReactNode, boolean?][] }) {
  return (
    <dl className={styles.details}>
      {entries.map(([term, value, mono]) => (
        <div key={term} className={styles.entry}>
          <dt className={styles.term}>{term}</dt>
          <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
        </div>
      ))}
    </dl>
  );
}

/* ---------------------------------------------------------------- Workspace */

interface Workspace {
  orders: PlannedOrder[];
  messages: Message[];
  last: PlanVersion | null;
}

/** Master planning workspace (4.22.6): tiles for the current version and the last run. */
export function PlanningHome() {
  const [plan] = usePlanCode();
  const state = useLoad<Workspace>(
    async (signal) => {
      const [orders, messages, versions] = await Promise.all([
        listPlannedOrders({ planCode: plan, limit: PAGE_LIMIT }, { signal }),
        listMessages(plan, undefined, { signal }),
        listVersions(plan, 1, { signal }),
      ]);
      return { orders, messages, last: versions[0] ?? null };
    },
    [plan],
  );
  const summary = summarize(state.data?.orders ?? [], state.data?.messages ?? []);
  const tile = (n: number) => (state.loading ? <Skeleton width="1/2" /> : n);
  const capped = (state.data?.orders.length ?? 0) >= PAGE_LIMIT ? `Counts the first ${PAGE_LIMIT} planned orders` : undefined;
  const last = state.data?.last;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Master planning"
        description="Planned orders, action and delay messages of the current plan version, each explained back to its demand."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to={withPlan("/planning/runs/compare", plan)}>
              Compare versions
            </Link>
            <Link className={buttonClassName("primary")} to={withPlan("/planning/runs/new", plan)}>
              Run plan
            </Link>
          </>
        }
      />
      <PlanPicker />
      <LoadAlert what={`plan ${plan}`} error={state.error} />
      <StatGroup label="Plan summary">
        <StatTile label="Planned purchase orders" value={tile(summary.byType.purchase)} hint={capped} />
        <StatTile label="Planned production orders" value={tile(summary.byType.production)} hint={capped} />
        <StatTile label="Planned transfer orders" value={tile(summary.byType.transfer)} hint={capped} />
        <StatTile label="Delayed orders" value={tile(summary.delayed)} tone="danger" />
        <StatTile label="Messages" value={tile(summary.messages)} tone="warning" />
      </StatGroup>
      <Card
        title="Last run"
        actions={
          <Link className={buttonClassName("ghost", "sm")} to={withPlan("/planning/runs", plan)}>
            Run history
          </Link>
        }
      >
        {state.loading ? (
          <div aria-busy="true">
            <Skeleton width="3/4" />
            <span className="visually-hidden">Loading…</span>
          </div>
        ) : last ? (
          <Details
            entries={[
              ["Version", last.number],
              ["Status", <StatusPill key="s" status={last.status} />],
              ["As of", last.asOf],
              ["Items planned", last.itemsPlanned],
              ["Planned orders", last.plannedOrders],
              ["Approved orders firmed", last.approvedFirmed ?? 0],
              ["Messages", last.messages],
              ["Duration", `${last.durationMs} ms`],
              ["Plan hash", last.planHash.slice(0, 16), true],
            ]}
          />
        ) : state.error ? null : (
          <EmptyState
            compact
            icon="grid"
            title={`Plan ${plan} has not run yet`}
            description="Run the plan to net demand against supply and propose planned orders."
            action={
              <Link className={buttonClassName("secondary")} to={withPlan("/planning/runs/new", plan)}>
                Run plan
              </Link>
            }
          />
        )}
      </Card>
    </div>
  );
}

/* ----------------------------------------------------------- Planned orders */

/**
 * Planned orders of the current version, filterable by status; a row opens its
 * explain tree, and selected open orders can be firmed (FR-22.10).
 */
export function PlannedOrdersPage() {
  const [plan] = usePlanCode();
  const [params, setParams] = useSearchParams();
  const rawStatus = params.get("status") ?? "";
  const status = (ORDER_STATUSES as readonly string[]).includes(rawStatus) ? (rawStatus as PlannedOrder["status"]) : undefined;
  const navigate = useNavigate();
  const [reload, setReload] = useState(0);
  const [selected, setSelected] = useState<string[]>([]);
  // `?version=N` reads a version by number: a simulation, whose orders cannot be firmed (FR-22.15).
  const versionParam = Number(params.get("version"));
  const version = Number.isInteger(versionParam) && versionParam > 0 ? versionParam : undefined;
  const state = useLoad(
    (signal) => listPlannedOrders({ planCode: plan, status, version, limit: PAGE_LIMIT }, { signal }),
    [plan, status, version, reload],
  );
  const open = (state.data ?? []).filter(isFirmable);
  // Only orders still listed and open stay selected when the list changes.
  const picked = selected.filter((id) => open.some((o) => o.id === id));
  // The last box ticked anchors a Shift range (4.22.6 interaction rules).
  const anchor = useRef<string | null>(null);
  const shiftHeld = useShiftHeld();
  const pick = (order: PlannedOrder, on: boolean, shift: boolean) => {
    const ids = shift && anchor.current ? rangeIds(state.data ?? [], anchor.current, order.id) : [];
    const change = ids.length > 0 ? ids : [order.id];
    setSelected((cur) => {
      const rest = cur.filter((x) => !change.includes(x));
      return on ? [...rest, ...change] : rest;
    });
    anchor.current = order.id;
  };
  const columns: DataTableColumn<PlannedOrder>[] = [
    { key: "number", header: "Number", mono: true },
    { key: "itemId", header: "Item", mono: true },
    { key: "orderType", header: "Type", render: (o) => <Badge tone="neutral">{orderTypeLabel[o.orderType]}</Badge> },
    { key: "qty", header: "Quantity", align: "end" },
    { key: "orderDate", header: "Order date" },
    { key: "deliveryDate", header: "Delivery date" },
    { key: "futuresDate", header: "Expected", render: (o) => o.futuresDate ?? dash },
    {
      key: "delayDays",
      header: "Delay",
      align: "end",
      render: (o) => (o.delayDays > 0 ? <Badge tone="warning">{`${o.delayDays} days`}</Badge> : dash),
    },
    { key: "lotRule", header: "Lot rule" },
    { key: "status", header: "Status", render: (o) => <StatusPill status={o.status} /> },
    {
      key: "select",
      header: "Firm",
      render: (o) =>
        isFirmable(o) && !version ? (
          <Input
            type="checkbox"
            aria-label={`Select ${o.number} for firming`}
            checked={picked.includes(o.id)}
            onClick={(e) => e.stopPropagation()}
            onChange={(e) => {
              const native = e.nativeEvent as Partial<MouseEvent>;
              pick(o, e.target.checked, shiftHeld.current || native.shiftKey === true);
            }}
          />
        ) : (
          dash
        ),
    },
  ];
  const setStatus = (value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set("status", value);
    else next.delete("status");
    setParams(next);
  };
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Planned orders"
        description={
          version
            ? `Version ${version} of plan ${plan}, read only: planned orders of a simulation cannot be firmed or changed.`
            : `Current version of plan ${plan}. Open an order to see the demand it serves.`
        }
      />
      <PlanPicker>
        <div className={styles.toolbarField}>
          <FormField id="planning-status" label="Status">
            {(a11y) => (
              <Select {...a11y} value={status ?? ""} onChange={(e) => setStatus(e.target.value)}>
                <option value="">All statuses</option>
                {ORDER_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {sentence(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
      </PlanPicker>
      <LoadAlert what="planned orders" error={state.error} />
      {state.error || version ? null : (
        <FirmCard
          selected={picked}
          openCount={open.length}
          onSelectAllOpen={() => setSelected(open.map((o) => o.id))}
          onClear={() => setSelected([])}
          onFirmed={() => {
            setSelected([]);
            setReload((n) => n + 1);
          }}
        />
      )}
      {state.error ? null : (
        <DataTable
          label="Planned orders"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(o) => o.id}
          loading={state.loading}
          empty={{ title: "No planned orders", description: "Run the plan, or pick another plan or status." }}
          onRowOpen={(o) => navigate(withPlan(`/planning/planned-orders/${o.id}`, plan))}
        />
      )}
    </div>
  );
}

/* ------------------------------------------------------- Planned order view */

/** Explain-this-plan tree (FR-22.6): supply at the root, the demand it covers below. */
export function PegTree({ node }: { node: PegNode }) {
  return (
    <li>
      <div className={styles.node}>
        <Badge tone={node.refType === "planned-order" ? "info" : node.refType === "surplus" ? "neutral" : "success"}>
          {sentence(node.refType)}
        </Badge>
        <span className={styles.mono}>{node.ref}</span>
        <span className={styles.qty}>{node.qty}</span>
      </div>
      {node.children.length > 0 ? (
        <ul className={styles.treeChildren}>
          {node.children.map((c, i) => (
            <PegTree key={`${c.ref}-${i}`} node={c} />
          ))}
        </ul>
      ) : null}
    </li>
  );
}

function sumChildren(node: PegNode): string {
  const total = node.children.reduce((acc, c) => acc + Number(c.qty), 0);
  return Number.isFinite(total) ? String(Number(total.toFixed(6))) : dash;
}

function ExplainCard({ orderId }: { orderId: string }) {
  const state = useLoad((signal) => getPegging(orderId, 20, { signal }), [orderId]);
  return (
    <Card title="Explain this plan">
      <LoadAlert what="the pegging tree" error={state.error} />
      {state.loading ? (
        <div aria-busy="true">
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
          <span className="visually-hidden">Loading…</span>
        </div>
      ) : state.data ? (
        <>
          <p className={styles.summary}>
            {`Pegging explains ${sumChildren(state.data.tree)} of ${state.data.qty}, including any surplus.`}
          </p>
          <ul className={styles.tree} aria-label="Pegging tree">
            <PegTree node={state.data.tree} />
          </ul>
        </>
      ) : null}
    </Card>
  );
}

/** One planned order with its maintenance form and explain tree. */
export function PlannedOrderPage() {
  const { id = "" } = useParams();
  const [plan] = usePlanCode();
  const navigate = useNavigate();
  const [reload, setReload] = useState(0);
  // The order as the last write returned it; a reload goes back to the service.
  const [edited, setEdited] = useState<PlannedOrder | null>(null);
  const state = useLoad((signal) => getPlannedOrder(id, { signal }), [id, reload]);
  const back = (
    <Link className={buttonClassName("secondary")} to={withPlan("/planning/planned-orders", plan)}>
      Back to planned orders
    </Link>
  );
  if (state.error?.status === 404) {
    return (
      <Card>
        <EmptyState
          icon="grid"
          title="Planned order not found"
          description="It may belong to a version that has been replaced. Pick an order from the current plan."
          action={back}
        />
      </Card>
    );
  }
  const o = edited?.id === id ? edited : state.data;
  const reloadOrder = () => {
    setEdited(null);
    setReload((n) => n + 1);
  };
  return (
    <div className={styles.stack}>
      <PageHeader
        title={o ? `Planned order ${o.number}` : "Planned order"}
        meta={o ? <StatusPill status={o.status} /> : undefined}
        actions={
          <>
            {o ? (
              <Link className={buttonClassName("ghost")} to={`${withPlan("/planning/net-requirements", plan)}&item=${encodeURIComponent(o.itemId)}`}>
                Net requirements of the item
              </Link>
            ) : null}
            {back}
          </>
        }
      />
      <LoadAlert what="the planned order" error={state.error} />
      <Card title="Order">
        {state.loading ? (
          <div aria-busy="true">
            <Skeleton width="3/4" />
            <span className="visually-hidden">Loading…</span>
          </div>
        ) : o ? (
          <Details
            entries={[
              ["Number", o.number, true],
              ["Item", o.itemId, true],
              ["Type", orderTypeLabel[o.orderType]],
              ["Quantity", o.qty],
              ["Order date", o.orderDate],
              ["Delivery date", o.deliveryDate],
              ["Expected", o.futuresDate ?? dash],
              ["Delay", o.delayDays > 0 ? `${o.delayDays} days` : dash],
              ["Lot rule", o.lotRule],
              ...(o.orderType === "purchase" ? ([["Vendor", o.vendorId ?? dash, true]] as [string, ReactNode, boolean][]) : []),
              ...(o.quotaArrangementId
                ? ([
                    ["Quota arrangement", <Link to={`/planning/quota-arrangements/${o.quotaArrangementId}`}>{o.quotaArrangementId}</Link>, true],
                    ["Quota source", o.quotaSourceId ?? dash, true],
                  ] as [string, ReactNode, boolean][])
                : []),
              ...(o.firmedTargetType && o.firmedTargetId
                ? ([
                    ["Became", targetLabel[o.firmedTargetType]],
                    ["Order id", o.firmedTargetId, true],
                  ] as [string, ReactNode, boolean?][])
                : []),
            ]}
          />
        ) : null}
      </Card>
      {o && !state.loading ? (
        <MaintainOrderCard
          key={o.id}
          order={o}
          onChanged={setEdited}
          onDeleted={() => navigate(withPlan("/planning/planned-orders", plan))}
          onReload={reloadOrder}
        />
      ) : null}
      {o ? <ExplainCard orderId={o.id} /> : null}
    </div>
  );
}

/* ------------------------------------------------------------------ Messages */

/** Action and delay messages of the current version (4.22.14). */
export function MessagesPage() {
  const [plan] = usePlanCode();
  const [params, setParams] = useSearchParams();
  const raw = params.get("action") ?? "";
  const action = (MESSAGE_ACTIONS as readonly string[]).includes(raw) ? (raw as Message["action"]) : undefined;
  const state = useLoad((signal) => listMessages(plan, action, { signal }), [plan, action]);
  const columns: DataTableColumn<Message>[] = [
    { key: "action", header: "Action", render: (m) => <Badge tone={actionTone[m.action]}>{sentence(m.action)}</Badge> },
    { key: "itemId", header: "Item", mono: true },
    { key: "supplyRef", header: "Supply", mono: true, render: (m) => m.supplyRef || dash },
    { key: "demandRef", header: "Demand", mono: true, render: (m) => m.demandRef || dash },
    { key: "suggestedDate", header: "Suggested date", render: (m) => m.suggestedDate || dash },
    { key: "delayDays", header: "Delay (days)", align: "end", render: (m) => (m.delayDays > 0 ? m.delayDays : dash) },
    { key: "qty", header: "Quantity", align: "end" },
  ];
  const setAction = (value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set("action", value);
    else next.delete("action");
    setParams(next);
  };
  return (
    <div className={styles.stack}>
      <PageHeader title="Action messages" description={`Delays, early and late receipts, shortages and supply to cancel, decrease or increase in the current version of plan ${plan}.`} />
      <PlanPicker>
        <div className={styles.toolbarField}>
          <FormField id="planning-action" label="Action">
            {(a11y) => (
              <Select {...a11y} value={action ?? ""} onChange={(e) => setAction(e.target.value)}>
                <option value="">All actions</option>
                {MESSAGE_ACTIONS.map((a) => (
                  <option key={a} value={a}>
                    {sentence(a)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
      </PlanPicker>
      <LoadAlert what="messages" error={state.error} />
      {state.error ? null : (
        <DataTable
          label="Messages"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(m, i) => `${m.action}-${m.itemId}-${m.supplyRef ?? ""}-${m.demandRef ?? ""}-${i}`}
          loading={state.loading}
          empty={{ title: "No messages", description: "The current plan version needs no action." }}
        />
      )}
    </div>
  );
}

/* ------------------------------------------------------------------ Runs */

const versionColumns: DataTableColumn<PlanVersion>[] = [
  { key: "number", header: "Version", align: "end" },
  { key: "status", header: "Status", render: (v) => <StatusPill status={v.status} /> },
  {
    key: "kind",
    header: "Kind",
    render: (v) => (v.kind === "simulation" ? <Badge tone="info">{`Simulation ${v.scenarioCode ?? ""}`.trim()}</Badge> : "Operational"),
  },
  { key: "asOf", header: "As of" },
  { key: "itemsPlanned", header: "Items", align: "end" },
  { key: "plannedOrders", header: "Planned orders", align: "end" },
  { key: "approvedFirmed", header: "Approved firmed", align: "end", render: (v) => v.approvedFirmed ?? 0 },
  { key: "messages", header: "Messages", align: "end" },
  { key: "durationMs", header: "Duration (ms)", align: "end" },
  { key: "planHash", header: "Plan hash", mono: true, render: (v) => v.planHash.slice(0, 16) },
];

/** Plan run history (4.22.14): every version with counts and its hash. */
export function PlanRunsPage() {
  const [plan] = usePlanCode();
  const state = useLoad((signal) => listVersions(plan, 50, { signal }), [plan]);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Plan runs"
        description={`Versions of plan ${plan}, newest first. Identical input gives an identical plan hash.`}
        actions={
          <>
            <Link className={buttonClassName("secondary")} to={withPlan("/planning/runs/compare", plan)}>
              Compare versions
            </Link>
            <Link className={buttonClassName("primary")} to={withPlan("/planning/runs/new", plan)}>
              Run plan
            </Link>
          </>
        }
      />
      <PlanPicker />
      <LoadAlert what="plan runs" error={state.error} />
      {state.error ? null : (
        <DataTable
          label="Plan runs"
          columns={versionColumns}
          rows={state.data ?? []}
          getRowKey={(v) => v.id}
          loading={state.loading}
          empty={{ title: "No runs yet", description: "Run the plan to create its first version." }}
        />
      )}
    </div>
  );
}
