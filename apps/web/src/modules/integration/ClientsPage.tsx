import { Link, useNavigate } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, DataTable, PageHeader, StatusPill, type DataTableColumn } from "../../shell/ui";
import { DUTY_HUB_CLIENT_WRITE, listClients, type ApiClient } from "./api/hubClient";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<ApiClient>[] = [
  { key: "name", header: "Name" },
  { key: "clientId", header: "Client id", mono: true },
  { key: "scopes", header: "Scopes", render: (row) => row.scopes.join(", ") },
  { key: "quotaPerMin", header: "Requests per minute", align: "end", render: (row) => `${row.quotaPerMin} (burst ${row.burst})` },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "createdAt", header: "Registered", render: (row) => formatDateTime(row.createdAt) },
];

/** API clients: external systems that call the public APIs, with their scopes and request quota (FR-35.8). */
export function ClientsPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_HUB_CLIENT_WRITE);
  const state = useLoad((signal) => listClients(undefined, signal), "hub-clients");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="API clients"
        description="Each client is a Keycloak client that calls the public APIs. Its scopes are the duties it may use; its quota is a token bucket that answers 429 when empty."
        actions={
          canWrite ? (
            <Link className={buttonClassName("primary")} to="/integration/clients/new">
              Register client
            </Link>
          ) : undefined
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load API clients" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="API clients"
          columns={columns}
          rows={state.data?.items ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          onRowOpen={(row) => navigate(`/integration/clients/${row.id}`)}
          empty={{ title: "No API clients yet", description: "Register a client to let an external system call the public APIs within a quota." }}
        />
      )}
    </div>
  );
}
