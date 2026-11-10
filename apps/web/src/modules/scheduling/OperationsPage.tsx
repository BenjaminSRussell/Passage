import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { listNetworks, listOperations, listResources, OPERATION_STATUSES, type Operation, type OperationStatus } from "./api/client";
import { dash, formatDateTime, formatMinutes, sentence, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const statusTone: Record<OperationStatus, BadgeTone> = { open: "neutral", scheduled: "success", unplaced: "warning" };

interface View {
  rows: Operation[];
  codes: Record<string, string>;
  networks: Record<string, string>;
}

/** Attributes as name=value pairs, the way capacity block conditions read them. */
export function describeAttributes(attributes: Record<string, string> | undefined): string {
  const pairs = Object.entries(attributes ?? {}).sort(([a], [b]) => a.localeCompare(b));
  return pairs.length === 0 ? dash : pairs.map(([name, value]) => `${name}=${value}`).join(", ");
}

/** Operations waiting for or holding a place in the schedule. */
export function OperationsPage() {
  const [status, setStatus] = useState<OperationStatus | "">("");
  const state = useLoad<View>(async (signal) => {
    const [rows, resources, networks] = await Promise.all([listOperations({ status }, signal), listResources(signal), listNetworks(signal)]);
    return {
      rows,
      codes: Object.fromEntries(resources.map((r) => [r.id, r.code])),
      networks: Object.fromEntries(networks.map((n) => [n.id, n.code])),
    };
  }, status);
  const codes = state.data?.codes ?? {};
  const networks = state.data?.networks ?? {};

  const columns: DataTableColumn<Operation>[] = [
    { key: "orderId", header: "Order", mono: true, render: (row) => shortId(row.orderId) },
    { key: "operNumber", header: "Operation", align: "end", render: (row) => <Link to={`/scheduling/operations/${row.id}`} aria-label={`Operation ${row.operNumber} of order ${shortId(row.orderId)}`}>{row.operNumber}</Link> },
    { key: "minutes", header: "Duration", align: "end", render: (row) => formatMinutes(row.minutes) },
    { key: "dueAt", header: "Due", render: (row) => (row.dueAt ? formatDateTime(row.dueAt) : dash) },
    { key: "priority", header: "Priority", align: "end" },
    { key: "resourceIds", header: "Candidates", mono: true, render: (row) => row.resourceIds.map((id) => codes[id] ?? shortId(id)).join(", ") },
    { key: "secondaryResourceIds", header: "Secondary", mono: true, render: (row) => (row.secondaryResourceIds.length > 0 ? row.secondaryResourceIds.map((id) => codes[id] ?? shortId(id)).join(", ") : dash) },
    { key: "setupGroup", header: "Setup group", mono: true, render: (row) => row.setupGroup ?? dash },
    { key: "networkId", header: "Network", mono: true, render: (row) => (row.networkId ? (networks[row.networkId] ?? shortId(row.networkId)) : dash) },
    { key: "attributes", header: "Attributes", render: (row) => describeAttributes(row.attributes) },
    { key: "status", header: "Status", render: (row) => <Badge tone={statusTone[row.status] ?? "neutral"}>{sentence(row.status)}</Badge> },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Operations"
        description="Operations of production orders with their duration, due date, candidate resources in priority order, and the setup group, network and attributes the scheduler's constraints use."
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
          <ProblemMessage problem={state.error} />
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
