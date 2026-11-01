import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { fetchLateOrders, type LateOrder } from "./api/client";
import { dash, formatDateTime, formatMinutes, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Smallest lateness choices, in minutes. */
export const LATE_THRESHOLDS: { label: string; minutes: number }[] = [
  { label: "Any lateness", minutes: 1 },
  { label: "1 hour or more", minutes: 60 },
  { label: "4 hours or more", minutes: 240 },
  { label: "1 day or more", minutes: 1440 },
  { label: "1 week or more", minutes: 10080 },
];

/** Orders planned past their due date, and orders with an operation the scheduler could not place (PRD 4.23.14 Late orders). */
export function LateOrdersPage() {
  const [minutes, setMinutes] = useState(1);
  const state = useLoad((signal) => fetchLateOrders(minutes, signal), String(minutes));

  const columns: DataTableColumn<LateOrder>[] = [
    { key: "orderId", header: "Order", mono: true, render: (row) => shortId(row.orderId) },
    { key: "dueAt", header: "Due", render: (row) => (row.dueAt ? formatDateTime(row.dueAt) : dash) },
    { key: "plannedEnd", header: "Planned end", render: (row) => (row.plannedEnd ? formatDateTime(row.plannedEnd) : dash) },
    { key: "lateMinutes", header: "Late by", align: "end", render: (row) => (row.lateMinutes > 0 ? formatMinutes(row.lateMinutes) : dash) },
    { key: "lateOperations", header: "Late operations", align: "end", render: (row) => `${row.lateOperations} of ${row.operations}` },
    { key: "dependentOrders", header: "Pegged orders", align: "end", render: (row) => (row.dependentOrders > 0 ? String(row.dependentOrders) : dash) },
    {
      key: "status",
      header: "Status",
      render: (row) => (
        <>
          {row.lateMinutes > 0 && <Badge tone="warning">Late</Badge>} {row.unplacedOperations > 0 && <Badge tone="danger">{`Unplaced ${row.unplacedOperations}`}</Badge>}{" "}
          {row.firmed && <Badge tone="info">Firmed</Badge>}
        </>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Late orders"
        description="Orders with a job that ends after its due date, most late first, followed by orders with an operation that could not be placed. Lateness is that of the plan as it stands; pegged orders are the orders that consume this one and are delayed with it."
        actions={
          <Link className={buttonClassName("secondary")} to="/scheduling">
            Open the Gantt
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="late-threshold" label="Late by">
          {(control) => (
            <Select {...control} value={String(minutes)} onChange={(event) => setMinutes(Number(event.target.value))}>
              {LATE_THRESHOLDS.map((t) => (
                <option key={t.minutes} value={t.minutes}>
                  {t.label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load late orders" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <>
          {state.data?.truncated && (
            <Alert tone="warning" title="Showing the first 200 orders">
              Raise the lateness threshold to narrow the list.
            </Alert>
          )}
          <DataTable
            label="Late orders"
            columns={columns}
            rows={state.data?.items ?? []}
            getRowKey={(row) => row.orderId}
            loading={state.loading}
            empty={{ title: "No late orders", description: "Every scheduled order ends by its due date and every operation is placed." }}
          />
        </>
      )}
    </div>
  );
}
