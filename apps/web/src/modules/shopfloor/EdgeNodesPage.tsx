import { useState } from "react";
import { Link } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { DUTY_EDGE_CONFIGURE, listEdgeNodes, type EdgeNode, type EdgeRing, type EdgeStatusValue } from "./api/client";
import { dash, formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const EDGE_STATUS: Record<EdgeStatusValue, { label: string; tone: BadgeTone }> = {
  pending: { label: "Waiting for first heartbeat", tone: "neutral" },
  online: { label: "Online", tone: "success" },
  offline: { label: "Offline", tone: "danger" },
  inactive: { label: "Switched off", tone: "neutral" },
};

export const RING_LABELS: Record<EdgeRing, string> = { canary: "Canary", ring1: "Ring 1", all: "All" };

export function EdgeStatusBadge({ status }: { status: EdgeStatusValue }) {
  const known = EDGE_STATUS[status];
  return <Badge tone={known?.tone ?? "neutral"}>{known?.label ?? status}</Badge>;
}

/** The configuration version a connector runs against the newest published one. */
export function configState(node: Pick<EdgeNode, "appliedConfigVersion" | "latestConfigVersion">): string {
  const { appliedConfigVersion: applied, latestConfigVersion: latest } = node;
  if (latest === null) return "No configuration published";
  if (applied === latest) return `Version ${latest} applied`;
  return applied === null ? `Version ${latest} not picked up yet` : `Running ${applied}, ${latest} published`;
}

const columns: DataTableColumn<EdgeNode>[] = [
  { key: "name", header: "Edge node", render: (row) => <Link to={`/shopfloor/edge-nodes/${row.id}`}>{row.name}</Link> },
  { key: "platform", header: "Platform" },
  { key: "ring", header: "Ring", render: (row) => RING_LABELS[row.ring] },
  { key: "status", header: "Status", render: (row) => <EdgeStatusBadge status={row.status} /> },
  { key: "connectorVersion", header: "Connector", mono: true, render: (row) => row.connectorVersion ?? dash },
  { key: "lastHeartbeatAt", header: "Last heartbeat", mono: true, render: (row) => formatDateTime(row.lastHeartbeatAt) },
  { key: "bufferFillPct", header: "Buffer", align: "end", render: (row) => (row.bufferFillPct === null ? dash : `${row.bufferFillPct}%`) },
  { key: "config", header: "Configuration", render: (row) => configState(row) },
];

/** The edge fleet: every connector with its derived status, ring and configuration state (FR-25.19). */
export function EdgeNodesPage() {
  const canConfigure = holdsDuty(usePrincipalDuties(), DUTY_EDGE_CONFIGURE);
  const [ring, setRing] = useState<EdgeRing | "">("");
  const [status, setStatus] = useState<EdgeStatusValue | "">("");
  const state = useLoad((signal) => listEdgeNodes({ ring: ring || undefined, status: status || undefined }, signal), `edge-nodes:${ring}:${status}`);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Edge fleet"
        description="Connectors report every 30 seconds; a node with no heartbeat for 90 seconds shows offline."
        actions={
          canConfigure ? (
            <Link className={buttonClassName("primary")} to="/shopfloor/edge-nodes/new">
              Register edge node
            </Link>
          ) : undefined
        }
      />
      <div className={styles.toolbar}>
        <FormField id="edge-ring" label="Ring">
          {(control) => (
            <Select {...control} value={ring} onChange={(event) => setRing(event.target.value as EdgeRing | "")}>
              <option value="">All rings</option>
              <option value="canary">Canary</option>
              <option value="ring1">Ring 1</option>
              <option value="all">All</option>
            </Select>
          )}
        </FormField>
        <FormField id="edge-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as EdgeStatusValue | "")}>
              <option value="">Any status</option>
              <option value="online">Online</option>
              <option value="offline">Offline</option>
              <option value="pending">Waiting for first heartbeat</option>
              <option value="inactive">Switched off</option>
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load edge nodes" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Edge nodes"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No edge nodes", description: "Register a connector, publish its configuration, and it appears here once it reports in." }}
        />
      )}
    </div>
  );
}
