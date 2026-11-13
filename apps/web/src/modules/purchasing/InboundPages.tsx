import { useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  Alert,
  Button,
  Card,
  FormField,
  Input,
  PageHeader,
  buttonClassName,
  ListPage,
  type ListColumn,
  type ListData,
  type ListFilterDef,
  type ListQuery,
} from "../../shell/ui";
import {
  describeProblem,
  getInboundChange,
  listInboundChanges,
  settleInboundChange,
  type InboundChange,
  type InboundResolution,
  type InboundSource,
  type InboundStatus,
} from "./api/client";
import {
  Details,
  LoadError,
  StatusBadge,
  dash,
  statusLabel,
  useResource,
  uuidPattern,
  type Failure,
  Named,
  useProcurementPages,
} from "./parts";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const SOURCES: InboundSource[] = ["PLANNING_MESSAGE", "DROPSHIP_CLOSED", "DROPSHIP_CANCELLED"];
const STATUSES: InboundStatus[] = ["APPLIED", "REFUSED", "IGNORED"];
type ResolutionFilter = InboundResolution | "NONE" | "";

/** What asked for the change, in words. */
export function sourceLabel(source: InboundSource): string {
  switch (source) {
    case "PLANNING_MESSAGE":
      return "Planning message";
    case "DROPSHIP_CLOSED":
      return "Drop-ship closed";
    default:
      return "Drop-ship cancelled";
  }
}

/** Where a change stands for the buyer: a refused one waits until it is acknowledged or retried. */
export function settledLabel(c: InboundChange): string {
  if (c.resolution === "ACKNOWLEDGED") return "Acknowledged";
  if (c.resolution === "RETRIED") return "Retried";
  return c.status === "REFUSED" ? "Waiting for a buyer" : dash;
}

/** The action of the request, in words: close-remainder reads "Close remainder". */
export function actionLabel(action: string): string {
  const spaced = action.replace(/-/g, " ");
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

/** The effect on the line, e.g. "Quantity 100 → 96" or "Due 2027-03-10 → 2027-03-17"; empty when nothing moved. */
export function effectOf(c: InboundChange): string {
  const parts: string[] = [];
  if (c.qtyBefore !== undefined && c.qtyAfter !== undefined) {
    parts.push(c.qtyAfter === "0" ? `Line cancelled (was ${c.qtyBefore})` : c.qtyBefore !== c.qtyAfter ? `Quantity ${c.qtyBefore} → ${c.qtyAfter}` : "");
  }
  if (c.dateBefore !== undefined && c.dateAfter !== undefined && c.dateBefore !== c.dateAfter) {
    parts.push(`Due ${c.dateBefore} → ${c.dateAfter}`);
  }
  return parts.filter(Boolean).join(", ");
}

/** The line a change is about: PO number and line number, linked to the order. */
function Target({ c }: { c: InboundChange }) {
  if (!c.purchaseOrderId) return <>{dash}</>;
  return (
    <Link className={styles.link} to={`/purchasing/purchase-orders/${c.purchaseOrderId}`}>
      {c.poNumber || c.purchaseOrderId.slice(0, 8)}
      {c.lineNo ? ` line ${c.lineNo}` : ""}
    </Link>
  );
}

const columns: ListColumn<InboundChange>[] = [
  { key: "createdAt", header: "Received", mono: true, render: (c) => c.createdAt.slice(0, 16).replace("T", " ") },
  { key: "source", header: "From", render: (c) => sourceLabel(c.source) },
  { key: "action", header: "Action", render: (c) => actionLabel(c.action) },
  { key: "target", header: "Purchase order", render: (c) => <Target c={c} /> },
  { key: "effect", header: "Effect", render: (c) => effectOf(c) || dash },
  { key: "status", header: "Status", render: (c) => <StatusBadge status={c.status} /> },
  { key: "reason", header: "Reason", render: (c) => (c.reason ? `${c.reasonCode}: ${c.reason}` : dash) },
  { key: "settled", header: "Settled", render: (c) => settledLabel(c) },
];

const filters: ListFilterDef[] = [
  { kind: "select", key: "source", label: "From", anyLabel: "All sources", options: SOURCES.map((s) => ({ value: s, label: sourceLabel(s) })) },
  { kind: "select", key: "status", label: "Status", anyLabel: "All statuses", options: STATUSES.map((s) => ({ value: s, label: statusLabel(s) })) },
  {
    kind: "select",
    key: "resolution",
    label: "Settled",
    anyLabel: "Any",
    options: [
      { value: "NONE", label: "Waiting for a buyer" },
      { value: "ACKNOWLEDGED", label: "Acknowledged" },
      { value: "RETRIED", label: "Retried" },
    ],
  },
];

/**
 * Changes from other services: the planning messages a planner applied to a purchase order and the closed or
 * cancelled drop-ships, as the purchasing consumer handled them. A refused change is the buyer's to settle: it
 * is listed here with the rule that stopped it, and its page retries or acknowledges it.
 */
export function InboundChangesPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const purchaseOrderId = params.get("purchaseOrderId") ?? "";
  const validOrder = purchaseOrderId === "" || uuidPattern.test(purchaseOrderId);
  const [initialFilters] = useState(() => ({
    status: params.get("status") === "REFUSED" ? "REFUSED" : "",
    resolution: params.get("resolution") === "NONE" ? "NONE" : "",
  }));
  const useChangeRows = (query: ListQuery): ListData<InboundChange> => {
    const source = (query.filters.source ?? "") as InboundSource | "";
    const status = (query.filters.status ?? "") as InboundStatus | "";
    const resolution = (query.filters.resolution ?? "") as ResolutionFilter;
    return useProcurementPages(`${source}|${status}|${resolution}|${purchaseOrderId}`, async (cursor, signal) =>
      validOrder ? listInboundChanges({ source, status, resolution, purchaseOrderId }, { cursor, signal }) : { items: [] },
    );
  };
  return (
    <ListPage
      id="purchasing-inbound-changes"
      title="Changes from other services"
      description="Planning messages a planner applied to a purchase order, and drop-ships that fulfillment closed or cancelled. Each moves the order through its change versions; a refused change waits for you to retry or acknowledge it."
      actions={purchaseOrderId !== "" ? <Button onClick={() => setParams({})}>Show all orders</Button> : undefined}
      notice={!validOrder ? <Alert tone="danger" live="alert" title="The purchase order id in the address is not a UUID." /> : undefined}
      noun="changes"
      singular="change"
      columns={columns}
      filters={filters}
      initialFilters={initialFilters}
      useRows={useChangeRows}
      getRowKey={(c) => c.id}
      onRowOpen={(c) => navigate(`/purchasing/inbound-changes/${c.id}`)}
      empty={{
        title: "No changes from other services",
        description: "When planning applies a message to a purchase order or fulfillment closes or cancels a drop-ship, the change is listed here.",
      }}
    />
  );
}

/** One change: what was asked, by whom, what the order did about it and, when it did nothing, why. */
export function InboundChangeDetailPage() {
  const { id = "" } = useParams();
  const resource = useResource(async (signal) => (await getInboundChange(id, signal)).body, [id]);
  const [settled, setSettled] = useState<InboundChange | null>(null);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState<"acknowledge" | "retry" | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);
  // Set when a retry ran but a purchasing rule refused the change again: the page would otherwise look unchanged.
  const [refusedAgain, setRefusedAgain] = useState(false);
  const c = settled && settled.id === id ? settled : resource.data;

  async function settle(action: "acknowledge" | "retry") {
    if (!c) return;
    setBusy(action);
    setFailure(null);
    setRefusedAgain(false);
    try {
      const { body } = await settleInboundChange(c.id, action, note.trim());
      setSettled(body);
      setRefusedAgain(action === "retry" && body.status === "REFUSED" && !body.resolution);
      setNote("");
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const waiting = c?.status === "REFUSED" && !c.resolution;
  const stale = failure?.code === "PRC-INB-008";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={c ? `${sourceLabel(c.source)}: ${actionLabel(c.action).toLowerCase()}` : "Change from another service"}
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/purchasing/inbound-changes">
              All changes
            </Link>
            {c?.purchaseOrderId ? (
              <Link className={buttonClassName("primary")} to={`/purchasing/purchase-orders/${c.purchaseOrderId}`}>
                Open purchase order
              </Link>
            ) : null}
          </>
        }
      />
      {resource.error ? <LoadError what="the change" error={resource.error} onRetry={resource.reload} /> : null}
      {resource.loading && !c ? <p role="status">Loading the change…</p> : null}
      <Alert tone="warning" live="status">
        {refusedAgain && c ? `The retry ran (${c.attempts ?? 0} runs in all) and was refused again; the reason below is the new one.` : null}
      </Alert>
      {c ? (
        <>
          {waiting ? (
            <Alert tone="danger" title={`Not applied (${c.reasonCode})`}>
              {c.reason} Fix the cause on the purchase order and retry, or change the line by hand and acknowledge.
            </Alert>
          ) : null}
          {c.status === "REFUSED" && c.resolution === "ACKNOWLEDGED" ? (
            <Alert tone="info" title="Acknowledged">
              Not applied ({c.reasonCode}); {c.resolvedBy || "a buyer"} dealt with it by hand. {c.reason}
            </Alert>
          ) : null}
          {c.resolution === "RETRIED" ? (
            <Alert tone="success" title="Retried">
              The request ran again ({c.attempts} runs in all) and is now {statusLabel(c.status).toLowerCase()}.
            </Alert>
          ) : null}
          {c.status === "IGNORED" ? (
            <Alert tone="info" title="Nothing to change">
              {c.reason}
            </Alert>
          ) : null}
          {c.status === "APPLIED" && c.reapprovalRequired ? (
            <Alert tone="warning" title="Waiting for re-approval">
              The change is version {c.versionNo} of the purchase order and takes effect when it is approved and confirmed.
            </Alert>
          ) : null}
          <Details
            items={[
              { label: "Status", value: <StatusBadge status={c.status} /> },
              { label: "From", value: sourceLabel(c.source) },
              { label: "Action", value: actionLabel(c.action) },
              { label: "Purchase order", value: <Target c={c} /> },
              { label: "Product", value: <Named entity="product" id={c.productId} />, mono: true },
              { label: "Effect", value: effectOf(c) },
              { label: "Requested quantity", value: c.requestedQty ?? "", mono: true },
              { label: "Requested date", value: c.requestedDate ?? "", mono: true },
              { label: "Order version", value: c.versionNo ? String(c.versionNo) : "", mono: true },
              { label: "Applied by", value: c.requestedBy ?? "" },
              { label: "Note", value: c.note ?? "" },
              { label: "Settled", value: settledLabel(c) },
              { label: "Runs of the request", value: c.attempts ? String(c.attempts) : "", mono: true },
              { label: "Settled by", value: c.resolvedBy ?? "" },
              { label: "Settled at", value: c.resolvedAt ?? "", mono: true },
              { label: "Settlement note", value: c.resolutionNote ?? "" },
              { label: "Source key", value: c.sourceKey, mono: true },
              { label: "Event key", value: c.eventId, mono: true },
              { label: "Received", value: c.createdAt, mono: true },
            ]}
          />
          <Alert
            tone="danger"
            live="alert"
            action={
              stale ? (
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() => {
                    setSettled(null);
                    setFailure(null);
                    resource.reload();
                  }}
                >
                  Reload change
                </Button>
              ) : undefined
            }
          >
            {failure ? <ProblemMessage problem={failure} /> : null}
          </Alert>
          {waiting ? (
            <Card title="Settle this change">
              <FormField id="inb-note" label="Note" optional>
                {(control) => <Input {...control} value={note} maxLength={1000} onChange={(e) => setNote(e.target.value)} />}
              </FormField>
              <div className={styles.actions}>
                <Button variant="primary" loading={busy === "retry"} disabled={busy !== null} onClick={() => settle("retry")}>
                  Retry the change
                </Button>
                <Button variant="secondary" loading={busy === "acknowledge"} disabled={busy !== null} onClick={() => settle("acknowledge")}>
                  Acknowledge
                </Button>
              </div>
            </Card>
          ) : null}
        </>
      ) : null}
    </div>
  );
}
