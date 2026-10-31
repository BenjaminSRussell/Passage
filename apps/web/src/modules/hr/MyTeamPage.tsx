import { useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  DataTable,
  PageHeader,
  type DataTableColumn,
} from "../../shell/ui";
import { describeProblem, type Problem, type Worker } from "./api/client";
import { listMyTeam } from "./api/selfService";
import { dash, workerTypeLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Worker>[] = [
  { key: "personnelNumber", header: "Personnel number", mono: true },
  { key: "legalName", header: "Legal name" },
  {
    key: "preferredName",
    header: "Preferred name",
    render: (row) => row.preferredName ?? dash,
  },
  {
    key: "workerType",
    header: "Type",
    render: (row) => (
      <Badge tone="neutral">
        {workerTypeLabel[row.workerType] ?? row.workerType}
      </Badge>
    ),
  },
];

/** The signed-in manager's direct reports (MSS). The service decides who is on the team. */
export function MyTeamPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listMyTeam(undefined, signal), "team");
  const [more, setMore] = useState<{ items: Worker[]; cursor?: string } | null>(
    null,
  );
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);

  const rows = [...(state.data?.items ?? []), ...(more?.items ?? [])];
  const cursor = more ? more.cursor : state.data?.nextCursor;

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const next = await listMyTeam(cursor);
      setMore({
        items: [...(more?.items ?? []), ...next.items],
        cursor: next.nextCursor,
      });
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="My team"
        description="The people who report to you today. Personal data is not shown here."
      />
      {state.error ? (
        <Alert
          tone="danger"
          title="Could not load your team"
          live="alert"
          action={
            <Button size="sm" onClick={state.reload}>
              Try again
            </Button>
          }
        >
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="My team"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{
            title: "No direct reports",
            description:
              "People appear here when their position reports to yours.",
          }}
          onRowOpen={(row) => navigate(`/hr/my/team/${row.id}`)}
        />
      )}
      {moreError ? (
        <Alert
          tone="danger"
          title="Could not load more of your team"
          live="alert"
        >
          {moreError.message} ({moreError.code})
        </Alert>
      ) : null}
      {!state.error && !state.loading && cursor ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more people
          </Button>
        </div>
      ) : null}
    </div>
  );
}
