import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, type DataTableColumn } from "../../shell/ui";
import { listScheduleLines, type Order, type ScheduleLine } from "./api/client";
import { sentence } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const scheduleColumns: DataTableColumn<ScheduleLine>[] = [
  { key: "lineNo", header: "Line", align: "end" },
  { key: "seq", header: "Delivery", align: "end" },
  { key: "quantity", header: "Quantity", align: "end" },
  { key: "deliveryDate", header: "Delivery date", mono: true, render: (s) => s.deliveryDate ?? "Not promised" },
  { key: "status", header: "Status", render: (s) => <Badge tone={s.status === "CONFIRMED" ? "success" : s.status === "BACKORDER" ? "warning" : "danger"}>{sentence(s.status)}</Badge> },
  { key: "source", header: "Source", render: scheduleSource },
];

/** Where a schedule line's date came from: the ATP bucket a promise drew on, or the line's confirmed date. */
export function scheduleSource(s: ScheduleLine): string {
  if (s.source === "LINE") return "Line date";
  return s.bucketDate ? `ATP bucket ${s.bucketDate}` : "ATP, no supply";
}

/** Schedule lines by line and delivery (FR-27.6). */
export function ScheduleTable({ lines, loading = false }: { lines: ScheduleLine[]; loading?: boolean }) {
  return <DataTable label="Schedule lines" columns={scheduleColumns} rows={lines} getRowKey={(s) => s.id} loading={loading} empty={{ title: "No schedule lines" }} />;
}

/**
 * The schedule lines of a confirmed order with a link to promise a line against
 * ATP. Reloads when the order's version changes.
 */
export function OrderSchedule({ order }: { order: Order }) {
  const state = useLoad((signal) => listScheduleLines(order.id, signal), `${order.id}:${order.version}`);
  const lines = state.data ?? [];
  return (
    <Card
      title="Schedule lines"
      actions={
        order.orderType === "JOURNAL" ? undefined : (
          <Link className={buttonClassName("secondary", "sm")} to={`/sales/orders/${order.id}/promise`}>
            Promise against ATP
          </Link>
        )
      }
    >
      {state.error ? (
        <Alert tone="danger" title="Could not load the schedule lines" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : !state.loading && lines.length === 0 ? (
        <EmptyState compact headingLevel={3} title="No schedule lines" description="Confirming the order creates one schedule line per line." />
      ) : (
        <div className={styles.stack}>
          <ScheduleTable lines={lines} loading={state.loading && lines.length === 0} />
        </div>
      )}
    </Card>
  );
}
