import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { ENVIRONMENTS, listPartners, type Environment, type Partner } from "./api/client";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Partner>[] = [
  { key: "name", header: "Partner" },
  { key: "isa", header: "ISA identity", mono: true, render: (row) => `${row.isaQualifier}/${row.isaId}` },
  { key: "environment", header: "Environment" },
  { key: "ackSlaMinutes", header: "997 expected within", align: "end", render: (row) => `${row.ackSlaMinutes} min` },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "createdAt", header: "Created", render: (row) => formatDateTime(row.createdAt) },
];

/** Trading partner profiles: who may send X12 to this tenant, and in which environment (FR-35.1). */
export function PartnersPage() {
  const navigate = useNavigate();
  const [environment, setEnvironment] = useState<Environment | "">("");
  const state = useLoad((signal) => listPartners({ environment }, signal), `partners:${environment}`);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Trading partners"
        description="Each partner is identified by its ISA sender qualifier and id. Inbound interchanges from unknown senders are refused."
        actions={
          <Link className={buttonClassName("primary")} to="/integration/partners/new">
            New partner
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="partners-environment" label="Environment">
          {(control) => (
            <Select {...control} value={environment} onChange={(event) => setEnvironment(event.target.value as Environment | "")}>
              <option value="">All environments</option>
              {ENVIRONMENTS.map((env) => (
                <option key={env} value={env}>
                  {env}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load partners" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Trading partners"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          onRowOpen={(row) => navigate(`/integration/partners/${row.id}`)}
          empty={{ title: "No trading partners yet", description: "Create a partner before it sends its first interchange." }}
        />
      )}
    </div>
  );
}
