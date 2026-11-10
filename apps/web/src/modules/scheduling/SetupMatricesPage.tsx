import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listResources, listSetupMatrices, type Resource, type SetupMatrix } from "./api/client";
import { formatDateTime, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

interface View {
  rows: SetupMatrix[];
  resources: Resource[];
}

/** Changeover matrices per resource, newest version first (FR-23.15). */
export function SetupMatricesPage() {
  const navigate = useNavigate();
  const [resourceId, setResourceId] = useState("");
  const state = useLoad<View>(async (signal) => {
    const [rows, resources] = await Promise.all([listSetupMatrices(resourceId, signal), listResources(signal)]);
    return { rows, resources };
  }, resourceId);
  const codes: Record<string, string> = Object.fromEntries((state.data?.resources ?? []).map((r) => [r.id, `${r.name} (${r.code})`]));

  const columns: DataTableColumn<SetupMatrix>[] = [
    { key: "resourceId", header: "Resource", render: (m) => codes[m.resourceId] ?? shortId(m.resourceId) },
    { key: "version", header: "Version", align: "end" },
    { key: "activeFrom", header: "Active from" },
    { key: "currency", header: "Currency", mono: true },
    { key: "entries", header: "Changeovers", align: "end", render: (m) => String(m.entries.length) },
    { key: "createdAt", header: "Created", render: (m) => formatDateTime(m.createdAt) },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Setup matrices"
        description="Changeover minutes and cost between setup groups on a resource. The version in force on a campaign's planned start prices its sequence."
        actions={
          <Link className={buttonClassName("primary")} to="/scheduling/setup-matrices/new">
            New matrix version
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="matrices-resource" label="Resource">
          {(control) => (
            <Select {...control} value={resourceId} onChange={(event) => setResourceId(event.target.value)}>
              <option value="">All resources</option>
              {(state.data?.resources ?? []).map((r) => (
                <option key={r.id} value={r.id}>
                  {r.code}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load setup matrices" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Setup matrices"
          columns={columns}
          rows={state.data?.rows ?? []}
          getRowKey={(m) => m.id}
          loading={state.loading}
          empty={{ title: "No setup matrices yet", description: "Without a matrix, campaigns use each order's route setup time and warn with SCHED-0012." }}
          onRowOpen={(m) => navigate(`/scheduling/setup-matrices/${m.id}`)}
        />
      )}
    </div>
  );
}
