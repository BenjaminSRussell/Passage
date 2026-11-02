import { useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Alert, Button, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import { DELIVERY_STATUSES, listDeliveries, type Delivery, type DeliveryStatus } from "./api/hubClient";
import { dash, formatDateTime, UUID } from "./format";
import styles from "./pages.module.css";
import { DeliveryPill, formatUnix } from "./syncFormat";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Delivery>[] = [
  { key: "eventType", header: "Event", mono: true },
  { key: "aggregateId", header: "Subject", mono: true },
  { key: "status", header: "Status", render: (row) => <DeliveryPill status={row.status} /> },
  { key: "attempts", header: "Attempts", align: "end" },
  { key: "lastStatus", header: "Last HTTP status", align: "end", render: (row) => (row.lastStatus === null ? dash : String(row.lastStatus)) },
  { key: "legacyKey", header: "ERP key", mono: true, render: (row) => row.legacyKey ?? dash },
  { key: "nextRetryAt", header: "Next try", render: (row) => (row.status === "pending" ? formatUnix(row.nextRetryAt) : dash) },
  { key: "createdAt", header: "Received", render: (row) => formatDateTime(row.createdAt) },
];

/** Delivery monitor: every event sent to a subscription, newest first, with status, attempts and the ERP's key. */
export function SyncDeliveriesPage() {
  const [params] = useSearchParams();
  const rawSub = params.get("subscriptionId") ?? "";
  const subscriptionId = UUID.test(rawSub) ? rawSub : "";
  const [status, setStatus] = useState<DeliveryStatus | "">("");
  const [more, setMore] = useState<Delivery[]>([]);
  const [cursor, setCursor] = useState<string | null | undefined>(undefined);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const key = `sync-deliveries:${status}:${subscriptionId}`;
  const state = useLoad(
    (signal) => {
      setMore([]);
      setCursor(undefined);
      return listDeliveries({ status, subscriptionId }, signal);
    },
    key,
  );
  const next = cursor === undefined ? (state.data?.nextCursor ?? null) : cursor;

  async function loadMore() {
    if (next === null) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listDeliveries({ status, subscriptionId, cursor: next });
      setMore((rows) => [...rows, ...page.items]);
      setCursor(page.nextCursor);
    } catch (failure) {
      setMoreError(describeProblem(failure));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Sync deliveries" description={subscriptionId ? "Deliveries of one subscription." : "Every event sent by outbound sync, newest first."} />
      <div className={styles.toolbar}>
        <FormField id="sync-deliveries-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as DeliveryStatus | "")}>
              <option value="">All</option>
              {DELIVERY_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {s === "dead_letter" ? "Dead letter" : s.charAt(0).toUpperCase() + s.slice(1)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button onClick={state.reload}>Refresh</Button>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load deliveries" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <>
          <DataTable
            label="Sync deliveries"
            columns={columns}
            rows={[...(state.data?.items ?? []), ...more]}
            getRowKey={(row) => row.id}
            loading={state.loading}
            empty={{ title: "No deliveries yet", description: "Deliveries appear when an event matches a subscription." }}
          />
          {moreError ? (
            <Alert tone="danger" title="Could not load more" live="alert">
              {moreError.message} ({moreError.code})
            </Alert>
          ) : null}
          {next !== null && !state.loading ? (
            <div>
              <Button onClick={loadMore} loading={loadingMore}>
                Load more
              </Button>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
