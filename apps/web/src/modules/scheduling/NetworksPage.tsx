import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listNetworks, type Network } from "./api/client";
import { dash } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<Network>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "activeFrom", header: "Active from" },
  { key: "activeTo", header: "Active to", render: (n) => n.activeTo ?? dash },
  { key: "edges", header: "Transitions", align: "end", render: (n) => String(n.edges.length) },
];

/** Resource networks: which resource may feed which, and how long product may wait (FR-23.13). */
export function NetworksPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listNetworks(signal), "networks");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Resource networks"
        description="Allowed transitions between resources, such as a mixing vessel feeding only certain fillers, with transfer time and maximum wait."
        actions={
          <Link className={buttonClassName("primary")} to="/scheduling/networks/new">
            New network
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load resource networks" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Resource networks"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(n) => n.id}
          loading={state.loading}
          empty={{ title: "No resource networks yet", description: "Without a network any candidate resource may follow any other." }}
          onRowOpen={(n) => navigate(`/scheduling/networks/${n.id}`)}
        />
      )}
    </div>
  );
}
