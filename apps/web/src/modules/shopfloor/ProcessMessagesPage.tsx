import { Link, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listProcessMessages, type ProcessCategory, type ProcessDestination, type ProcessMessage, type ProcessStatus } from "./api/client";
import { dash, formatDateTime } from "./format";
import styles from "./pages.module.css";
import {
  CATEGORIES,
  CATEGORY_LABELS,
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
  { key: "categoryCode", header: "Category", render: (row) => CATEGORY_LABELS[row.categoryCode] ?? row.categoryCode },
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

/** Process message monitor (FR-25.17, CO54 equivalent): messages with their per-destination delivery state. */
export function ProcessMessagesPage() {
  const [params, setParams] = useSearchParams();
  const status = (params.get("status") ?? "") as ProcessStatus | "";
  const category = (params.get("category") ?? "") as ProcessCategory | "";
  const destination = (params.get("destination") ?? "") as ProcessDestination | "";
  const state = useLoad(
    (signal) => listProcessMessages({ status, category, destination }, signal),
    `process|${status}|${category}|${destination}`,
  );

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
              {CATEGORIES.map((c) => (
                <option key={c} value={c}>
                  {CATEGORY_LABELS[c]}
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
        <DataTable
          label="Process messages"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={
            filtered
              ? { title: "No messages match these filters", description: "Clear a filter to see more messages." }
              : { title: "No process messages yet", description: "Kiosk PI sheets and the edge connector send them as orders run; you can also record one by hand." }
          }
        />
      )}
    </div>
  );
}
