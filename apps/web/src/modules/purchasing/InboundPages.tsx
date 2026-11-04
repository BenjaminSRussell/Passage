import { useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Alert, Button, DataTable, FormField, PageHeader, Select, buttonClassName, type DataTableColumn } from "../../shell/ui";
import { getInboundChange, listInboundChanges, type InboundChange, type InboundSource, type InboundStatus } from "./api/client";
import { Details, LoadError, ShortId, StatusBadge, dash, statusLabel, useResource, uuidPattern } from "./parts";
import styles from "./pages.module.css";

const SOURCES: InboundSource[] = ["PLANNING_MESSAGE", "DROPSHIP_CLOSED"];
const STATUSES: InboundStatus[] = ["APPLIED", "REFUSED", "IGNORED"];

/** What asked for the change, in words. */
export function sourceLabel(source: InboundSource): string {
  return source === "PLANNING_MESSAGE" ? "Planning message" : "Drop-ship closed";
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

const columns: DataTableColumn<InboundChange>[] = [
  { key: "createdAt", header: "Received", mono: true, render: (c) => c.createdAt.slice(0, 16).replace("T", " ") },
  { key: "source", header: "From", render: (c) => sourceLabel(c.source) },
  { key: "action", header: "Action", render: (c) => actionLabel(c.action) },
  { key: "target", header: "Purchase order", render: (c) => <Target c={c} /> },
  { key: "effect", header: "Effect", render: (c) => effectOf(c) || dash },
  { key: "status", header: "Status", render: (c) => <StatusBadge status={c.status} /> },
  { key: "reason", header: "Reason", render: (c) => (c.reason ? `${c.reasonCode}: ${c.reason}` : dash) },
];

/**
 * Changes from other services: the planning messages a planner applied to a purchase order and the closed
 * drop-ship remainders, as the purchasing consumer handled them. A refused change is the buyer's to settle
 * by hand on the order; it is listed here with the rule that stopped it.
 */
export function InboundChangesPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const purchaseOrderId = params.get("purchaseOrderId") ?? "";
  const [source, setSource] = useState<InboundSource | "">("");
  const [status, setStatus] = useState<InboundStatus | "">(params.get("status") === "REFUSED" ? "REFUSED" : "");
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<InboundChange[]>([]);
  const validOrder = purchaseOrderId === "" || uuidPattern.test(purchaseOrderId);
  const page = useResource(
    async (signal) => {
      if (!validOrder) return { items: [] as InboundChange[] };
      const result = await listInboundChanges({ source, status, purchaseOrderId }, { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [source, status, purchaseOrderId, cursor, validOrder],
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Changes from other services"
        description="Planning messages a planner applied to a purchase order, and drop-ship remainders that fulfillment closed. Each moves the order through its change versions; a refused change is left for you to settle on the order."
      />
      <div className={styles.toolbar}>
        <div className={styles.filter}>
          <FormField id="inb-source" label="From">
            {(control) => (
              <Select
                {...control}
                value={source}
                onChange={(e) => {
                  setCursor(undefined);
                  setSource(e.target.value as InboundSource | "");
                }}
              >
                <option value="">All sources</option>
                {SOURCES.map((s) => (
                  <option key={s} value={s}>
                    {sourceLabel(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
        <div className={styles.filter}>
          <FormField id="inb-status" label="Status">
            {(control) => (
              <Select
                {...control}
                value={status}
                onChange={(e) => {
                  setCursor(undefined);
                  setStatus(e.target.value as InboundStatus | "");
                }}
              >
                <option value="">All statuses</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {statusLabel(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
        {purchaseOrderId !== "" ? (
          <div className={styles.filter}>
            <Button
              onClick={() => {
                setCursor(undefined);
                setParams({});
              }}
            >
              Show all orders
            </Button>
          </div>
        ) : null}
      </div>
      {!validOrder ? <Alert tone="danger" live="alert" title="The purchase order id in the address is not a UUID." /> : null}
      {page.error ? <LoadError what="the changes" error={page.error} onRetry={page.reload} /> : null}
      <DataTable
        label="Changes from other services"
        columns={columns}
        rows={page.error || !validOrder ? [] : rows}
        getRowKey={(c) => c.id}
        loading={page.loading && rows.length === 0}
        empty={{
          title: "No changes from other services",
          description: "When planning applies a message to a purchase order or fulfillment closes a drop-ship, the change is listed here.",
        }}
        onRowOpen={(c) => navigate(`/purchasing/inbound-changes/${c.id}`)}
      />
      {page.data?.nextCursor ? (
        <div className={styles.actions}>
          <Button loading={page.loading} onClick={() => setCursor(page.data?.nextCursor)}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

/** One change: what was asked, by whom, what the order did about it and, when it did nothing, why. */
export function InboundChangeDetailPage() {
  const { id = "" } = useParams();
  const resource = useResource(async (signal) => (await getInboundChange(id, signal)).body, [id]);
  const c = resource.data;
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
      {c ? (
        <>
          {c.status === "REFUSED" ? (
            <Alert tone="danger" title={`Not applied (${c.reasonCode})`}>
              {c.reason} Change the line by hand on the purchase order if it still has to move.
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
              { label: "Product", value: <ShortId id={c.productId} />, mono: true },
              { label: "Effect", value: effectOf(c) },
              { label: "Requested quantity", value: c.requestedQty ?? "", mono: true },
              { label: "Requested date", value: c.requestedDate ?? "", mono: true },
              { label: "Order version", value: c.versionNo ? String(c.versionNo) : "", mono: true },
              { label: "Applied by", value: c.requestedBy ?? "" },
              { label: "Note", value: c.note ?? "" },
              { label: "Source id", value: c.sourceKey, mono: true },
              { label: "Event id", value: c.eventId, mono: true },
              { label: "Received", value: c.createdAt, mono: true },
            ]}
          />
        </>
      ) : null}
    </div>
  );
}
