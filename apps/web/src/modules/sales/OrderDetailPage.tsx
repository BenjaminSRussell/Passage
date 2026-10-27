import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  applyHold,
  CHANGEABLE_STATUSES,
  confirmOrder,
  describeProblem,
  getOrder,
  releaseHold,
  type CreditRule,
  type Hold,
  type ListingOverride,
  type ManualHoldType,
  MANUAL_HOLD_TYPES,
  type Order,
  type OrderLine,
  type Problem,
  type Versioned,
} from "./api/client";
import { dash, formatDateTime, money, sentence } from "./format";
import { OrderHistory } from "./OrderHistory";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const ruleTone = { PASS: "success", WARN: "warning", FAIL: "danger" } as const;

const ruleNames: Record<string, string> = {
  "CR-1": "Exposure against the credit limit",
  "CR-2": "Oldest open item",
  "CR-3": "Dunning level",
  "CR-4": "Mandatory credit check and review date",
  "CR-5": "Overdue share of open AR",
  "CR-6": "Order size for a new customer",
};

interface Outcome {
  tone: "success" | "warning";
  text: string;
}

/** One order: header, lines, holds and the last credit check, with confirm and release. */
export function OrderDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getOrder(id, signal), id);
  const [latest, setLatest] = useState<Versioned<Order> | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const [holdType, setHoldType] = useState<ManualHoldType>("MANUAL");
  const [reasonCode, setReasonCode] = useState("");
  const [reasonError, setReasonError] = useState<string | undefined>();
  const current = latest && latest.value.id === id ? latest : state.data;

  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Sales order" />
        <Alert tone="danger" title="Could not load the order" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Sales order" />
        <span className="visually-hidden">Loading the order…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: order, etag } = current;

  async function run(action: string, call: () => Promise<Versioned<Order & { confirmed: boolean }>>, describe: (o: Order & { confirmed: boolean }) => Outcome) {
    setBusy(action);
    setFailure(null);
    setOutcome(null);
    try {
      const res = await call();
      setLatest(res);
      setOutcome(describe(res.value));
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const confirm = () =>
    run("confirm", () => confirmOrder(order.id, etag), (o) =>
      o.confirmed
        ? { tone: "success", text: `Order ${o.orderNumber} is confirmed.` }
        : { tone: "warning", text: "The credit check failed, so a credit hold was placed. A credit manager must release it." },
    );
  const release = (hold: Hold) =>
    run(`release-${hold.id}`, () => releaseHold(order.id, hold.id, etag), (o) =>
      o.confirmed
        ? { tone: "success", text: `${sentence(hold.holdType)} hold released and order ${o.orderNumber} confirmed.` }
        : { tone: "success", text: `${sentence(hold.holdType)} hold released.` },
    );

  async function placeHold(event: FormEvent) {
    event.preventDefault();
    const reason = reasonCode.trim();
    if (reason.length < 1 || reason.length > 80) {
      setReasonError("Enter a reason code of up to 80 characters.");
      return;
    }
    setReasonError(undefined);
    await run(
      "hold",
      async () => {
        const res = await applyHold(order.id, etag, holdType, reason);
        return { ...res, value: { ...res.value, confirmed: false } };
      },
      () => {
        setReasonCode("");
        return { tone: "success", text: `${sentence(holdType)} hold applied.` };
      },
    );
  }

  const confirmable = order.status === "DRAFT" || order.status === "OPEN";
  const blocked = order.holds.some((h) => h.status === "ACTIVE" && (h.holdType === "CREDIT" || h.holdType === "FRAUD"));
  const stale = failure?.code === "SLS-0006";

  const facts: [string, ReactNode][] = [
    ["Order", order.orderNumber],
    ["Type", sentence(order.orderType)],
    ["Customer", order.customerId],
    ["Invoice account", order.invoiceAccountId],
    ["Legal entity", order.legalEntityId],
    ["Requested ship date", order.requestedShipDate],
    ["Channel", order.externalRef ? `${order.channel} · ${order.externalRef}` : order.channel],
    ["Customer group", order.customerGroup ?? dash],
    ["Net total", money(order.netTotal, order.currency)],
    ["Version", String(order.version)],
    ["Created", formatDateTime(order.createdAt)],
  ];

  const lineColumns: DataTableColumn<OrderLine>[] = [
    { key: "lineNo", header: "Line", align: "end" },
    { key: "productId", header: "Product", mono: true },
    { key: "quantity", header: "Quantity", align: "end", render: (l) => `${l.quantity} ${l.unit}` },
    { key: "unitPrice", header: "Unit price", align: "end" },
    { key: "discPct", header: "Discount %", align: "end" },
    { key: "lineNet", header: "Net", align: "end" },
    { key: "requestedShipDate", header: "Requested", mono: true },
    { key: "confirmedShipDate", header: "Confirmed", mono: true, render: (l) => l.confirmedShipDate ?? dash },
    { key: "lineStatus", header: "Status", render: (l) => <StatusPill status={l.lineStatus} /> },
  ];

  const holdColumns: DataTableColumn<Hold>[] = [
    { key: "holdType", header: "Hold", render: (h) => sentence(h.holdType) },
    { key: "reasonCode", header: "Reason", mono: true },
    { key: "status", header: "Status", render: (h) => <StatusPill status={h.status} /> },
    { key: "orderAmount", header: "Amount held", align: "end", render: (h) => money(h.orderAmount, order.currency) },
    { key: "appliedAt", header: "Applied", render: (h) => `${formatDateTime(h.appliedAt)} by ${h.appliedBy}` },
    { key: "releasedUntil", header: "Release valid until", render: (h) => (h.releasedUntil ? formatDateTime(h.releasedUntil) : dash) },
    {
      key: "actions",
      header: "Action",
      render: (h) =>
        h.status === "ACTIVE" ? (
          <Button size="sm" loading={busy === `release-${h.id}`} disabled={busy !== null && busy !== `release-${h.id}`} onClick={() => release(h)} aria-label={`Release ${sentence(h.holdType)} hold ${h.reasonCode}`}>
            Release
          </Button>
        ) : (
          dash
        ),
    },
  ];

  const overrideColumns: DataTableColumn<ListingOverride>[] = [
    { key: "lineNo", header: "Line", align: "end" },
    { key: "productId", header: "Product", mono: true },
    { key: "decision", header: "Listing decision", render: (o) => sentence(o.decision) },
    { key: "reason", header: "Reason" },
    { key: "actor", header: "Accepted by" },
  ];

  const ruleColumns: DataTableColumn<CreditRule>[] = [
    { key: "id", header: "Rule", mono: true },
    { key: "name", header: "Checks", render: (r) => ruleNames[r.id] ?? r.id },
    { key: "result", header: "Result", render: (r) => <Badge tone={ruleTone[r.result]}>{sentence(r.result)}</Badge> },
    { key: "value", header: "Value", align: "end", render: (r) => r.value || dash },
    { key: "threshold", header: "Threshold", align: "end", render: (r) => r.threshold || dash },
    { key: "note", header: "Note", render: (r) => r.note ?? dash },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Sales order ${order.orderNumber}`}
        meta={<StatusPill status={order.status} />}
        actions={
          <div className={styles.actions}>
            {confirmable ? (
              <Button variant="primary" loading={busy === "confirm"} disabled={blocked || (busy !== null && busy !== "confirm")} onClick={confirm}>
                Confirm order
              </Button>
            ) : null}
            {CHANGEABLE_STATUSES.includes(order.status) ? (
              <Link className={buttonClassName("secondary")} to={`/sales/orders/${order.id}/change`}>
                Change or cancel
              </Link>
            ) : null}
            <Link className={buttonClassName("ghost")} to="/sales/orders">
              Back to orders
            </Link>
          </div>
        }
      />
      <Alert tone={outcome?.tone ?? "success"} live="status">
        {outcome?.text}
      </Alert>
      {failure ? (
        <Alert
          tone="danger"
          title={stale ? "Someone else changed this order" : "The action failed"}
          live="alert"
          action={
            stale ? (
              <Button
                size="sm"
                onClick={() => {
                  setLatest(null);
                  setFailure(null);
                  state.reload();
                }}
              >
                Reload order
              </Button>
            ) : undefined
          }
        >
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {confirmable && blocked ? <p className={styles.note}>An active credit or fraud hold blocks confirmation until it is released.</p> : null}
      <Card>
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={term === "Order" || term.endsWith("account") || term === "Customer" || term === "Legal entity" ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <DataTable label="Order lines" columns={lineColumns} rows={order.lines} getRowKey={(l) => l.id} empty={{ title: "No lines" }} />
      <Card title="Holds">
        {order.holds.length === 0 ? (
          <EmptyState compact headingLevel={3} title="No holds" description="Holds placed by the credit check or by a sales manager appear here." />
        ) : (
          <DataTable label="Holds" columns={holdColumns} rows={order.holds} getRowKey={(h) => h.id} />
        )}
        {CHANGEABLE_STATUSES.includes(order.status) ? (
          <form className={styles.toolbar} onSubmit={placeHold} noValidate aria-label="Apply a hold">
            <FormField id="hold-type" label="Hold type" hint="Fraud blocks confirmation; manual and price review block warehouse release.">
              {(control) => (
                <Select {...control} value={holdType} onChange={(e) => setHoldType(e.target.value as ManualHoldType)}>
                  {MANUAL_HOLD_TYPES.map((t) => (
                    <option key={t} value={t}>
                      {sentence(t)}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="hold-reason" label="Reason code" error={reasonError}>
              {(control) => <Input {...control} value={reasonCode} onChange={(e) => setReasonCode(e.target.value)} invalid={Boolean(reasonError)} />}
            </FormField>
            <Button type="submit" loading={busy === "hold"} disabled={busy !== null && busy !== "hold"}>
              Apply hold
            </Button>
          </form>
        ) : null}
      </Card>
      {order.listingOverrides && order.listingOverrides.length > 0 ? (
        <Card title="Listing overrides">
          <DataTable label="Listing overrides" columns={overrideColumns} rows={order.listingOverrides} getRowKey={(o) => String(o.lineNo)} />
        </Card>
      ) : null}
      <Card title="Last credit check">
        {order.creditCheck ? (
          <div className={styles.stack}>
            <p className={styles.muted}>
              Outcome: <Badge tone={ruleTone[order.creditCheck.outcome]}>{sentence(order.creditCheck.outcome)}</Badge>
            </p>
            <DataTable label="Credit rules" columns={ruleColumns} rows={order.creditCheck.rules} getRowKey={(r) => r.id} />
          </div>
        ) : (
          <EmptyState compact headingLevel={3} title="Not checked yet" description="The credit check runs when the order is confirmed." />
        )}
      </Card>
      <OrderHistory orderId={order.id} version={order.version} />
    </div>
  );
}
