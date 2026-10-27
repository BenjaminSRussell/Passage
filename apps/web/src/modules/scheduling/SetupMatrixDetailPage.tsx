import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton } from "../../shell/ui";
import { getSetupMatrix, listResources, type SetupEntry, type SetupMatrix } from "./api/client";
import { dash, formatDateTime, formatMinutes, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

interface Detail {
  matrix: SetupMatrix;
  code: string;
}

/** One version of a resource's setup matrix with its changeovers. */
export function SetupMatrixDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad<Detail>(async (signal) => {
    const [matrix, resources] = await Promise.all([getSetupMatrix(id, signal), listResources(signal)]);
    return { matrix, code: resources.find((r) => r.id === matrix.resourceId)?.code ?? shortId(matrix.resourceId) };
  }, id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Setup matrix" />
        <Alert tone="danger" title="Could not load the setup matrix" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Setup matrix" />
        <span className="visually-hidden">Loading the setup matrix…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { matrix, code } = state.data;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${code} setup matrix`}
        description={`Version ${matrix.version}, active from ${matrix.activeFrom}. Costs in ${matrix.currency}. Created ${formatDateTime(matrix.createdAt)}.`}
        meta={<Badge tone="info">{`v${matrix.version}`}</Badge>}
        actions={
          <Link className={buttonClassName("secondary")} to="/scheduling/setup-matrices/new">
            New version
          </Link>
        }
      />
      <Card title="Changeovers">
        <DataTable
          label="Changeovers"
          columns={[
            { key: "fromGroup", header: "From group", mono: true },
            { key: "toGroup", header: "To group", mono: true },
            { key: "minutes", header: "Minutes", align: "end", render: (e: SetupEntry) => formatMinutes(e.minutes) },
            { key: "cost", header: `Cost (${matrix.currency})`, align: "end", mono: true },
            { key: "cleaningSetId", header: "Cleaning set", render: (e: SetupEntry) => e.cleaningSetId ?? dash },
          ]}
          rows={matrix.entries}
          getRowKey={(e) => `${e.fromGroup}->${e.toGroup}`}
          empty={{ title: "No changeovers", description: "This version lists no group pairs." }}
        />
      </Card>
    </div>
  );
}
