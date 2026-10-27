import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton } from "../../shell/ui";
import { getNetwork, listResources, type Network, type NetworkEdge } from "./api/client";
import { dash, formatMinutes, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

interface Detail {
  network: Network;
  codes: Record<string, string>;
}

/** One resource network with its allowed transitions. */
export function NetworkDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad<Detail>(async (signal) => {
    const [network, resources] = await Promise.all([getNetwork(id, signal), listResources(signal)]);
    return { network, codes: Object.fromEntries(resources.map((r) => [r.id, r.code])) };
  }, id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Resource network" />
        <Alert tone="danger" title="Could not load the network" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Resource network" />
        <span className="visually-hidden">Loading the network…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { network, codes } = state.data;
  const code = (rid?: string) => (rid ? (codes[rid] ?? shortId(rid)) : dash);

  return (
    <div className={styles.stack}>
      <PageHeader
        title={network.code}
        description={`${network.name}. Active from ${network.activeFrom}${network.activeTo ? ` to ${network.activeTo}` : ""}.`}
        actions={
          <Link className={buttonClassName("ghost")} to="/scheduling/networks">
            All networks
          </Link>
        }
      />
      <Card title="Allowed transitions">
        <DataTable
          label="Allowed transitions"
          columns={[
            { key: "from", header: "From", mono: true, render: (e: NetworkEdge) => code(e.fromResourceId) },
            { key: "to", header: "To", mono: true, render: (e: NetworkEdge) => code(e.toResourceId) },
            { key: "transfer", header: "Transfer", align: "end", render: (e: NetworkEdge) => formatMinutes(e.transferMinutes) },
            { key: "wait", header: "Maximum wait", align: "end", render: (e: NetworkEdge) => (e.maxWaitMinutes === null ? "No limit" : formatMinutes(e.maxWaitMinutes)) },
            { key: "secondary", header: "Transfer resource", mono: true, render: (e: NetworkEdge) => code(e.secondaryResourceId) },
          ]}
          rows={network.edges}
          getRowKey={(e) => `${e.fromResourceId}>${e.toResourceId}`}
          empty={{ title: "No transitions", description: "This network allows no resource changes." }}
        />
      </Card>
    </div>
  );
}
