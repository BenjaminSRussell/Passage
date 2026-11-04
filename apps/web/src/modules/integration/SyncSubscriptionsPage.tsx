import { Link, useNavigate } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, StatusPill, type DataTableColumn } from "../../shell/ui";
import { DUTY_HUB_WRITE, listSubscriptions, type Subscription } from "./api/hubClient";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Subscription>[] = [
  { key: "name", header: "Name" },
  { key: "kind", header: "Kind", render: (row) => (row.kind === "rest-push" ? <Badge tone="info">REST push</Badge> : <Badge tone="neutral">Webhook</Badge>) },
  { key: "url", header: "Endpoint", mono: true },
  { key: "patterns", header: "Event types", render: (row) => row.patterns.join(", ") },
  { key: "filter", header: "Filter", mono: true, render: (row) => row.filter || "—" },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "createdAt", header: "Created", render: (row) => formatDateTime(row.createdAt) },
];

/** Outbound sync: subscriptions that push Passage events to a webhook or into the customer's legacy ERP. */
export function SyncSubscriptionsPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_HUB_WRITE);
  const state = useLoad((signal) => listSubscriptions(undefined, signal), "sync-subscriptions");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Outbound sync"
        description="Each subscription sends the events it matches to one endpoint. A REST push subscription renders the event through an outbound mapping and writes it into the existing ERP."
        actions={
          canWrite ? (
            <>
              <Link className={buttonClassName("secondary")} to="/integration/sync/new-webhook">
                New webhook
              </Link>
              <Link className={buttonClassName("primary")} to="/integration/sync/new">
                New REST push
              </Link>
            </>
          ) : undefined
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load subscriptions" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Outbound sync subscriptions"
          columns={columns}
          rows={state.data?.items ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          onRowOpen={(row) => navigate(`/integration/sync/${row.id}`)}
          empty={{ title: "No outbound sync yet", description: "Create a webhook to send events to a receiver, or a REST push subscription to write confirmed orders and other events into the ERP." }}
        />
      )}
    </div>
  );
}
