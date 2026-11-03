import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  cancelOrderRemainder,
  describeProblem,
  getOrder,
  listOrderProgress,
  newKey,
  PROGRESS_KINDS,
  recordOrderProgress,
  type OrderProgress,
  type OrderProgressFilter,
  type OrderType,
  type Order,
  type Problem,
  type ProgressKind,
  type Versioned,
} from "./api/client";
import { dash, DECIMAL, formatDateTime, sentence, shortId, subtractDecimal, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/**
 * Reporting a delivery and reporting an invoice each need their own duty. The
 * service enforces them (they are new duties; the authz seed is handed off
 * under #313), so the nav item stays on sales.order.read and only the action
 * is gated here.
 */
export const DUTY_RECORD_DELIVERY = "sales.order.record-delivery";
export const DUTY_RECORD_INVOICE = "sales.order.record-invoice";

/** Closing the remainder of a partly shipped order is an order change (SalesOrderMaintain in the PRD duty catalog). */
export const DUTY_CANCEL_REMAINDER = "sales.order.write";
const REASON_MAX = 500;

const kindDuty: Record<ProgressKind, string> = { DELIVERED: DUTY_RECORD_DELIVERY, INVOICED: DUTY_RECORD_INVOICE };

/**
 * The kinds of report an order of this type and status takes (mirrors the
 * service): none on a JOURNAL or RETURNED order, deliveries until it has all
 * shipped, invoices (not on a SUBSCRIPTION order, which billing invoices per
 * period) until it is invoiced.
 */
export function reportKinds(orderType: OrderType, status: string): ProgressKind[] {
  if (orderType === "JOURNAL" || orderType === "RETURNED") return [];
  const kinds: ProgressKind[] = [];
  if (status === "CONFIRMED" || status === "PARTIALLY_SHIPPED") kinds.push("DELIVERED");
  if (orderType !== "SUBSCRIPTION" && (status === "CONFIRMED" || status === "PARTIALLY_SHIPPED" || status === "SHIPPED")) kinds.push("INVOICED");
  return kinds;
}

/** The report kinds the signed-in user holds the duty for. */
function useRecordableKinds(): ProgressKind[] {
  const duties = usePrincipalDuties();
  return PROGRESS_KINDS.filter((k) => holdsDuty(duties, kindDuty[k]));
}

const kindTone = { DELIVERED: "info", INVOICED: "success" } as const;

export function ProgressKindBadge({ kind }: { kind: ProgressKind }) {
  return <Badge tone={kindTone[kind]}>{sentence(kind)}</Badge>;
}

function progressColumns(withOrder: boolean): DataTableColumn<OrderProgress>[] {
  const cols: DataTableColumn<OrderProgress>[] = [
    { key: "recordedAt", header: "Recorded", render: (p) => `${formatDateTime(p.recordedAt)} by ${p.recordedBy}` },
    { key: "kind", header: "Report", render: (p) => <ProgressKindBadge kind={p.kind} /> },
    { key: "sourceRef", header: "Reference", mono: true, render: (p) => p.sourceRef },
    {
      key: "lines",
      header: "Lines",
      render: (p) => p.lines.map((l) => `${l.lineNo}: ${l.quantity}`).join(", "),
    },
    { key: "orderStatus", header: "Order then", render: (p) => <StatusPill status={p.orderStatus} /> },
  ];
  if (withOrder) cols.splice(1, 0, { key: "orderNumber", header: "Order", render: (p) => <Link to={`/sales/orders/${p.orderId}`}>{p.orderNumber}</Link> });
  return cols;
}

/** The deliveries and invoices recorded on one order, on its detail page (BR-27.5). */
export function OrderProgressCard({ orderId, version, status, orderType }: { orderId: string; version: number; status: string; orderType: OrderType }) {
  const held = useRecordableKinds();
  const canRecord = reportKinds(orderType, status).some((k) => held.includes(k));
  const state = useLoad((signal) => listOrderProgress({ orderId }, signal), `${orderId}:${version}`);
  const rows = state.data ?? [];
  return (
    <Card
      title="Deliveries and invoices"
      actions={
        canRecord ? (
          <Link className={buttonClassName("secondary")} to={`/sales/orders/${orderId}/progress/new`}>
            Record delivery or invoice
          </Link>
        ) : undefined
      }
    >
      {state.error ? (
        <Alert tone="danger" title="Could not load the deliveries and invoices" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : !state.loading && rows.length === 0 ? (
        <EmptyState compact headingLevel={3} title="Nothing delivered or invoiced yet" description="Packing slips and invoices that cover this order appear here and move its lines." />
      ) : (
        <DataTable label="Deliveries and invoices of the order" columns={progressColumns(false)} rows={rows} getRowKey={(p) => p.id} loading={state.loading && rows.length === 0} />
      )}
    </Card>
  );
}

/** Deliveries and invoices across orders, newest first, filtered by order or kind. */
export function OrderProgressListPage() {
  const [draft, setDraft] = useState({ legalEntityId: "", orderId: "", kind: "" as ProgressKind | "" });
  const [filter, setFilter] = useState<OrderProgressFilter>({});
  const invalid: Record<string, string> = {};
  if (draft.legalEntityId.trim() && !UUID.test(draft.legalEntityId.trim())) invalid.legalEntityId = "Enter a legal entity UUID.";
  if (draft.orderId.trim() && !UUID.test(draft.orderId.trim())) invalid.orderId = "Enter an order UUID.";
  const state = useLoad((signal) => listOrderProgress(filter, signal), JSON.stringify(filter));
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Deliveries and invoices"
        description="What fulfillment and billing reported against sales orders: each packing slip or invoice with the quantities it covered. Each report moves the order lines and the order status."
      />
      <form
        className={styles.toolbar}
        aria-label="Filter deliveries and invoices"
        onSubmit={(e) => {
          e.preventDefault();
          if (Object.keys(invalid).length > 0) return;
          setFilter({ legalEntityId: draft.legalEntityId.trim() || undefined, orderId: draft.orderId.trim() || undefined, kind: draft.kind || undefined });
        }}
      >
        <FormField id="op-le" label="Legal entity" error={invalid.legalEntityId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })} invalid={Boolean(invalid.legalEntityId)} />}
        </FormField>
        <FormField id="op-order" label="Order" error={invalid.orderId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.orderId} onChange={(e) => setDraft({ ...draft, orderId: e.target.value })} invalid={Boolean(invalid.orderId)} />}
        </FormField>
        <FormField id="op-kind" label="Report">
          {(control) => (
            <Select {...control} value={draft.kind} onChange={(e) => setDraft({ ...draft, kind: e.target.value as ProgressKind | "" })}>
              <option value="">Deliveries and invoices</option>
              {PROGRESS_KINDS.map((k) => (
                <option key={k} value={k}>
                  {sentence(k)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load the deliveries and invoices" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Deliveries and invoices"
          columns={progressColumns(true)}
          rows={state.data ?? []}
          getRowKey={(p) => p.id}
          loading={state.loading}
          empty={{ title: "Nothing recorded", description: "A delivery or invoice reported against an order appears here." }}
        />
      )}
    </div>
  );
}

const refusalTitle: Record<string, string> = {
  "SLS-0058": "The order cannot take this report",
  "SLS-0059": "The quantity is too high",
  "SLS-0060": "This reference was already recorded",
};

/** Records a delivery or an invoice on one order: pick the report, name its source and enter the quantity covered on each line. */
export function NewOrderProgressPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const held = useRecordableKinds();
  const state = useLoad((signal) => getOrder(id, signal), id);
  const [chosenKind, setKind] = useState<ProgressKind | null>(null);
  const [sourceRef, setSourceRef] = useState("");
  const [quantities, setQuantities] = useState<Record<number, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  const back = (
    <Link className={buttonClassName("ghost")} to={`/sales/orders/${id}`}>
      Back to the order
    </Link>
  );
  if (state.error && !state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Record delivery or invoice" actions={back} />
        <Alert tone="danger" title="Could not load the order" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Record delivery or invoice" />
        <span className="visually-hidden">Loading the order…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const order = state.data.value;
  const kinds = reportKinds(order.orderType, order.status).filter((k) => held.includes(k));
  const kind: ProgressKind = chosenKind && kinds.includes(chosenKind) ? chosenKind : (kinds[0] ?? "DELIVERED");
  const canRecord = kinds.length > 0;
  const lines = order.lines.filter((l) => l.lineStatus !== "CANCELLED");
  const open = (l: (typeof lines)[number]) => (kind === "DELIVERED" ? subtractDecimal(l.quantity, l.deliveredQty ?? "0") : subtractDecimal(l.deliveredQty ?? "0", l.invoicedQty ?? "0"));

  function touch() {
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (sourceRef.trim() === "" || sourceRef.trim().length > 100) found.sourceRef = "Enter the packing slip or invoice number (up to 100 characters).";
    const chosen: { lineNo: number; quantity: string }[] = [];
    for (const l of lines) {
      const q = (quantities[l.lineNo] ?? "").trim();
      if (q === "") continue;
      if (!DECIMAL.test(q) || Number(q) <= 0) found[`line-${l.lineNo}`] = "Enter a positive quantity with up to 6 places.";
      else if (subtractDecimal(open(l), q).startsWith("-")) found[`line-${l.lineNo}`] = `At most ${open(l)} can still be ${kind === "DELIVERED" ? "delivered" : "invoiced"}.`
      else chosen.push({ lineNo: l.lineNo, quantity: q });
    }
    if (chosen.length === 0 && !Object.keys(found).some((k) => k.startsWith("line-"))) found.lines = "Enter a quantity on at least one line.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await recordOrderProgress(id, { kind, sourceRef: sourceRef.trim(), lines: chosen }, key.current);
      navigate(`/sales/orders/${id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) {
        // The service names a line by its place in the request (lines[i]); show it on that line.
        const mapped: Record<string, string> = {};
        for (const [field, message] of Object.entries(problem.fields)) {
          const at = /^lines\[(\d+)\]/.exec(field);
          const line = at ? chosen[Number(at[1])] : undefined;
          mapped[line ? `line-${line.lineNo}` : field] = message;
        }
        setErrors(mapped);
      }
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const columns: DataTableColumn<(typeof lines)[number]>[] = [
    { key: "lineNo", header: "Line", align: "end" },
    { key: "productId", header: "Product", mono: true, render: (l) => shortId(l.productId) },
    { key: "quantity", header: "Ordered", align: "end", render: (l) => `${l.quantity} ${l.unit}` },
    { key: "deliveredQty", header: "Delivered", align: "end", render: (l) => l.deliveredQty ?? "0" },
    { key: "invoicedQty", header: "Invoiced", align: "end", render: (l) => l.invoicedQty ?? "0" },
    { key: "open", header: kind === "DELIVERED" ? "Still to deliver" : "Still to invoice", align: "end", render: (l) => open(l) },
    {
      key: "report",
      header: "This report",
      render: (l) => (
        <FormField id={`progress-line-${l.lineNo}`} label={`Quantity on line ${l.lineNo}`} error={errors[`line-${l.lineNo}`]}>
          {(control) => (
            <Input
              {...control}
              inputMode="decimal"
              className={styles.mono}
              value={quantities[l.lineNo] ?? ""}
              onChange={(e) => {
                setQuantities({ ...quantities, [l.lineNo]: e.target.value });
                touch();
              }}
              invalid={Boolean(errors[`line-${l.lineNo}`])}
            />
          )}
        </FormField>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Record delivery or invoice on ${order.orderNumber}`}
        description="Enter what a packing slip delivered or an invoice billed. Quantities are what this report covers, not running totals; leave a line empty when the report does not cover it."
        meta={<StatusPill status={order.status} />}
        actions={back}
      />
      {failure ? (
        <Alert tone="danger" title={refusalTitle[failure.code] ?? "The report could not be recorded"} live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {!canRecord ? (
        <Alert tone="warning" title="You cannot record this">
          {reportKinds(order.orderType, order.status).length === 0
            ? `A ${sentence(order.orderType)} order that is ${sentence(order.status)} takes no delivery or invoice.`
            : `Recording a delivery needs the ${DUTY_RECORD_DELIVERY} duty and recording an invoice the ${DUTY_RECORD_INVOICE} duty.`}
        </Alert>
      ) : null}
      <Card>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label="Record delivery or invoice">
          <FormField id="progress-kind" label="Report">
            {(control) => (
              <Select
                {...control}
                value={kind}
                onChange={(e) => {
                  setKind(e.target.value as ProgressKind);
                  touch();
                }}
              >
                {(kinds.length > 0 ? kinds : PROGRESS_KINDS).map((k) => (
                  <option key={k} value={k}>
                    {k === "DELIVERED" ? "Delivery (packing slip)" : "Invoice"}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="progress-source" label="Reference" hint="The packing slip, shipment or invoice number. The same reference is applied once." error={errors.sourceRef}>
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={sourceRef}
                onChange={(e) => {
                  setSourceRef(e.target.value);
                  touch();
                }}
                invalid={Boolean(errors.sourceRef)}
              />
            )}
          </FormField>
          {errors.lines ? (
            <Alert tone="danger" title="Nothing to record" live="alert">
              {errors.lines}
            </Alert>
          ) : null}
          <DataTable label="Order lines" columns={columns} rows={lines} getRowKey={(l) => l.id} empty={{ title: "No lines", description: dash }} />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy} disabled={!canRecord}>
              Record {kind === "DELIVERED" ? "delivery" : "invoice"}
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}

/**
 * Closes the undelivered remainder of a partly shipped order (BR-27.5): what
 * shipped stays, the rest of every line is cancelled and the order becomes
 * shipped. Holds are left for their owners to release. Shown only to a user
 * who holds the order write duty.
 */
export function CancelRemainderCard({ order, etag, onDone }: { order: Order; etag: string; onDone: (res: Versioned<Order>) => void }) {
  const duties = usePrincipalDuties();
  const [reason, setReason] = useState("");
  const [sure, setSure] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  if (order.status !== "PARTIALLY_SHIPPED" || !holdsDuty(duties, DUTY_CANCEL_REMAINDER)) return null;
  const open = order.lines.filter((l) => l.lineStatus !== "CANCELLED" && Number(subtractDecimal(l.quantity, l.deliveredQty ?? "0")) > 0);
  const held = order.holds.some((h) => h.status === "ACTIVE");

  async function submit() {
    setFailure(null);
    if (reason.length > REASON_MAX) {
      setError(`Keep the reason to ${REASON_MAX} characters.`);
      return;
    }
    setError(undefined);
    setBusy(true);
    try {
      const res = await cancelOrderRemainder(order.id, etag, reason.trim());
      setSure(false);
      onDone(res);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Cancel the remainder">
      <div className={styles.form}>
        <p className={styles.note}>
          What was delivered stays on the order. {open.length > 0 ? `Line ${open.map((l) => l.lineNo).join(", ")} still ${open.length === 1 ? "has" : "have"} quantity to deliver; ` : ""}closing the remainder cancels it, lowers the order total and the credit exposure to what was delivered, returns allocation quota and agreement quantity, and ends the order as shipped.
          {held ? " Its active holds stay until they are released." : ""}
        </p>
        {failure ? (
          <Alert tone="danger" title="The remainder was not cancelled" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        <FormField id="remainder-reason" label="Reason" optional error={error}>
          {(control) => <Textarea {...control} rows={2} maxLength={REASON_MAX} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(error)} />}
        </FormField>
        <div className={styles.formActions}>
          <span className={styles.check}>
            <Input id="remainder-sure" type="checkbox" checked={sure} onChange={(e) => setSure(e.target.checked)} />
            <label htmlFor="remainder-sure">Cancel everything not yet delivered on this order</label>
          </span>
        </div>
        <div className={styles.formActions}>
          <Button variant="danger" loading={busy} disabled={!sure} onClick={submit}>
            Cancel remainder
          </Button>
        </div>
      </div>
    </Card>
  );
}
