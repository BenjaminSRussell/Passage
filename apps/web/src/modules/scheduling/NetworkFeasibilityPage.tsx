import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { fetchNetworkFeasibility, listNetworks, type NetworkFinding } from "./api/client";
import { dash, dayStart, formatDateTime, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/**
 * Operations the scheduler could not place because no resource transition of their network was allowed (SCHED-0009),
 * and placed operations that wait longer than the network maximum (SCHED-0010), read from the schedule runs (PRD 4.23.14 Network feasibility).
 */
export function NetworkFeasibilityPage() {
  const [networkId, setNetworkId] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const networks = useLoad((signal) => listNetworks(signal), "networks");
  // "Runs to" is a day, inclusive: runs made before the start of the next day.
  const rangeInverted = from !== "" && to !== "" && to < from;
  const key = `${networkId}|${from}|${to}`;
  const state = useLoad(
    (signal) =>
      rangeInverted
        ? Promise.resolve({ items: [], truncated: false, infeasible: 0, infeasibleOrders: 0, waitViolations: 0 })
        : fetchNetworkFeasibility({ networkId: networkId || undefined, from: from ? dayStart(from) : undefined, to: to ? dayStart(to, 1) : undefined }, signal),
    key,
  );

  const columns: DataTableColumn<NetworkFinding>[] = [
    {
      key: "flag",
      header: "Finding",
      render: (row) => (row.flag === "NETWORK_INFEASIBLE" ? <Badge tone="danger">Network infeasible</Badge> : <Badge tone="warning">Wait over maximum</Badge>),
    },
    { key: "code", header: "Code", mono: true, render: (row) => row.code },
    { key: "orderId", header: "Order", mono: true, render: (row) => (row.orderId ? shortId(row.orderId) : dash) },
    {
      key: "operation",
      header: "Operation",
      mono: true,
      render: (row) =>
        row.orderId ? <Link to={`/scheduling/operations/${row.operationId}`}>{`Operation ${row.operNumber ?? ""}`.trim()}</Link> : "Deleted",
    },
    { key: "network", header: "Network", mono: true, render: (row) => row.networkCode ?? dash },
    {
      key: "run",
      header: "Run",
      render: (row) => (
        <Link to={`/scheduling/runs/${row.runId}`} aria-label={`Run ${shortId(row.runId)}`}>
          {formatDateTime(row.runAt)}
        </Link>
      ),
    },
    { key: "detail", header: "Reason", render: (row) => row.detail },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Network feasibility"
        description="Operations a schedule run left unplaced because their resource network allows no transition within the delay limit, and placed operations that wait longer than the network maximum. Findings are the messages of the runs, newest run first; the order and network are those of the operation today."
        actions={
          <Link className={buttonClassName("secondary")} to="/scheduling/networks">
            Resource networks
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="nf-network" label="Network">
          {(control) => (
            <Select {...control} value={networkId} onChange={(event) => setNetworkId(event.target.value)}>
              <option value="">All networks</option>
              {(networks.data ?? []).map((n) => (
                <option key={n.id} value={n.id}>{`${n.code} ${n.name}`}</option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="nf-from" label="Runs from">
          {(control) => <Input {...control} type="date" value={from} onChange={(event) => setFrom(event.target.value)} />}
        </FormField>
        <FormField id="nf-to" label="Runs to">
          {(control) => <Input {...control} type="date" value={to} onChange={(event) => setTo(event.target.value)} />}
        </FormField>
      </div>
      {rangeInverted && (
        <Alert tone="warning" title="The range is empty">
          The last day is before the first day, so no run can match.
        </Alert>
      )}
      {state.error ? (
        <Alert tone="danger" title="Could not load network findings" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <>
          {state.data && (
            <p aria-label="Totals">
              <Badge tone="danger">{`${state.data.infeasibleOrders} ${state.data.infeasibleOrders === 1 ? "order" : "orders"} network infeasible (${state.data.infeasible} ${state.data.infeasible === 1 ? "operation" : "operations"})`}</Badge>{" "}
              <Badge tone="warning">{`${state.data.waitViolations} wait over maximum`}</Badge>
            </p>
          )}
          {state.data?.truncated && (
            <Alert tone="warning" title="Showing the first 200 findings">
              Pick a network or a shorter range to narrow the list.
            </Alert>
          )}
          <DataTable
            label="Network findings"
            columns={columns}
            rows={state.data?.items ?? []}
            getRowKey={(row) => `${row.runId}|${row.code}|${row.operationId}`}
            loading={state.loading}
            empty={{ title: "No network findings", description: "No run in this range left an operation network infeasible or waiting longer than its network allows." }}
          />
        </>
      )}
    </div>
  );
}
