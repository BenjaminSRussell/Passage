import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { listOperations, listResources, OPERATION_STATUSES, type Operation, type OperationStatus } from "./api/client";
import { dash, formatDateTime, formatMinutes, sentence, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const statusTone: Record<OperationStatus, BadgeTone> = { open: "neutral", scheduled: "success", unplaced: "warning" };

interface View {
  rows: Operation[];
  codes: Record<string, string>;
}

/** Operations waiting for or holding a place in the schedule. */
export function OperationsPage() {
  const [status, setStatus] = useState<OperationStatus | "">("");
  const state = useLoad<View>(async (signal) => {
    const [rows, resources] = await Promise.all([listOperations({ status }, signal), listResources(signal)]);
    return { rows, codes: Object.fromEntries(resources.map((r) => [r.id, r.code])) };
  }, status);
  const codes = state.data?.codes ?? {};

  const columns: DataTableColumn<Operation>[] = [
    { key: "orderId", header: "Order", mono: true, render: (row) => shortId(row.orderId) },
    { key: "operNumber", header: "Operation", align: "end" },
    { key: "minutes", header: "Duration", align: "end", render: (row) => formatMinutes(row.minutes) },
    { key: "dueAt", header: "Due", render: (row) => (row.dueAt ? formatDateTime(row.dueAt) : dash) },
    { key: "priority", header: "Priority", align: "end" },
    { key: "resourceIds", header: "Candidates", mono: true, render: (row) => row.resourceIds.map((id) => codes[id] ?? shortId(id)).join(", ") },
    { key: "status", header: "Status", render: (row) => <Badge tone={statusTone[row.status] ?? "neutral"}>{sentence(row.status)}</Badge> },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Operations"
        description="Operations of production orders with their duration, due date and candidate resources in priority order."
        actions={
          <Link className={buttonClassName("primary")} to="/scheduling/operations/new">
            New operation
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="operations-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as OperationStatus | "")}>
              <option value="">All statuses</option>
              {OPERATION_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load operations" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Operations"
          columns={columns}
          rows={state.data?.rows ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No operations yet", description: "Add operations of production orders, then run the schedule." }}
        />
      )}
    </div>
  );
}
