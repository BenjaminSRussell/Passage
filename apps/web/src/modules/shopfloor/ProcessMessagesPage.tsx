import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  listProcessMessages,
  type ProcessCategory,
  type ProcessDestination,
  type ProcessMessage,
  type ProcessMessagePage,
  type ProcessStatus,
  type Problem,
} from "./api/client";
import { dash, formatDateTime } from "./format";
import styles from "./pages.module.css";
import {
  categoryLabel,
  useCategoryOptions,
  DESTINATIONS,
  DESTINATION_LABELS,
  DeliveryStatus,
  MessageStatus,
  STATUSES,
  statusLabel,
} from "./processLabels";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<ProcessMessage>[] = [
  { key: "receivedAt", header: "Received", render: (row) => formatDateTime(row.receivedAt) },
  { key: "categoryCode", header: "Category", render: (row) => categoryLabel(row.categoryCode) },
  { key: "orderId", header: "Order", mono: true },
  { key: "phasePath", header: "Phase", mono: true, render: (row) => row.phasePath || dash },
  { key: "source", header: "Source", render: (row) => `${row.source} ${row.sourceMessageId}` },
  {
    key: "status",
    header: "Status",
    render: (row) => (
      <span className={styles.flags}>
        <MessageStatus status={row.status} />
        {row.outOfLimit ? <Badge tone="warning">Out of limit</Badge> : null}
      </span>
    ),
  },
  {
    key: "deliveries",
    header: "Destinations",
    render: (row) => (
      <span className={styles.flags}>
        {row.deliveries.map((d) => (
          <span key={d.destination} className={styles.flags}>
            {d.destination}
            <DeliveryStatus status={d.status} />
          </span>
        ))}
      </span>
    ),
  },
  {
    key: "open",
    header: "Details",
    render: (row) => <Link to={`/shopfloor/process-messages/${row.id}`}>Open message from {row.sourceMessageId}</Link>,
  },
];

/** Pages loaded after the first one, tied to the first page they continue. */
interface MorePages {
  base: ProcessMessagePage;
  items: ProcessMessage[];
  cursor: string | null;
  error: Problem | null;
}

/**
 * Process message monitor (FR-25.17, CO54 equivalent): messages with their per-destination
 * delivery state, newest first, one page at a time (keyset cursor).
 */
export function ProcessMessagesPage() {
  const [params, setParams] = useSearchParams();
  const categories = useCategoryOptions(false);
  const status = (params.get("status") ?? "") as ProcessStatus | "";
  const category = (params.get("category") ?? "") as ProcessCategory | "";
  const destination = (params.get("destination") ?? "") as ProcessDestination | "";
  const state = useLoad(
    (signal) => listProcessMessages({ status, category, destination }, signal),
    `process|${status}|${category}|${destination}`,
  );
  const [more, setMore] = useState<MorePages | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  // Further pages belong to the first page they continue; a new filter or a reload starts over.
  const extra = more && state.data && more.base === state.data ? more : null;
  const rows = state.data ? [...state.data.items, ...(extra?.items ?? [])] : [];
  const cursor = extra ? extra.cursor : (state.data?.nextCursor ?? null);

  async function loadMore() {
    const base = state.data;
    if (!base || !cursor) return;
    setLoadingMore(true);
    try {
      const page = await listProcessMessages({ status, category, destination, cursor });
      setMore((prev) => ({
        base,
        items: [...(prev && prev.base === base ? prev.items : []), ...page.items],
        cursor: page.nextCursor,
        error: null,
      }));
    } catch (failed) {
      const error = describeProblem(failed);
      setMore((prev) => (prev && prev.base === base ? { ...prev, error } : { base, items: [], cursor, error }));
    } finally {
      setLoadingMore(false);
    }
  }

  function setFilter(name: string, value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set(name, value);
    else next.delete(name);
    setParams(next);
  }

  const filtered = status !== "" || category !== "" || destination !== "";

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Process messages"
        description="Messages from PI sheets, process control and manual entry, routed by category to the batch record, the production journal, quality, Kafka and webhooks. A failed destination is retried after 1, 5, 15 and 60 minutes, then waits in error for a reprocess."
        actions={
          <Link className={buttonClassName("primary")} to="/shopfloor/process-messages/new">
            Record message
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="process-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setFilter("status", event.target.value)}>
              <option value="">All statuses</option>
              {STATUSES.map((s) => (
                <option key={s} value={s}>
                  {statusLabel(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="process-category" label="Category">
          {(control) => (
            <Select {...control} value={category} onChange={(event) => setFilter("category", event.target.value)}>
              <option value="">All categories</option>
              {categories.map((c) => (
                <option key={c.code} value={c.code}>
                  {c.label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="process-destination" label="Destination">
          {(control) => (
            <Select {...control} value={destination} onChange={(event) => setFilter("destination", event.target.value)}>
              <option value="">All destinations</option>
              {DESTINATIONS.map((d) => (
                <option key={d} value={d}>
                  {DESTINATION_LABELS[d]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load process messages" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <>
          <DataTable
            label="Process messages"
            columns={columns}
            rows={rows}
            getRowKey={(row) => row.id}
            loading={state.loading}
            empty={
              filtered
                ? { title: "No messages match these filters", description: "Clear a filter to see more messages." }
                : { title: "No process messages yet", description: "Kiosk PI sheets and the edge connector send them as orders run; you can also record one by hand." }
            }
          />
          {extra?.error ? (
            <Alert tone="danger" title="Could not load more messages" live="alert">
              {extra.error.message} ({extra.error.code})
            </Alert>
          ) : null}
          {!state.loading && cursor ? (
            <div className={styles.actions}>
              <Button loading={loadingMore} onClick={() => void loadMore()}>
                Load more messages
              </Button>
              <span className={styles.note}>{`Showing the newest ${rows.length} ${rows.length === 1 ? "message" : "messages"}.`}</span>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
