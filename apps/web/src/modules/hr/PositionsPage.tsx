import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listPositions, type Position, type Problem } from "./api/client";
import { dayOf, lastDay } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** Columns for the positions list; the reports-to number comes from the loaded rows. */
function columns(byId: Map<string, Position>): DataTableColumn<Position>[] {
  return [
    { key: "positionNumber", header: "Position", mono: true },
    { key: "jobCode", header: "Job", mono: true },
    { key: "fte", header: "FTE", align: "end" },
    {
      key: "reportsToPositionId",
      header: "Reports to",
      mono: true,
      render: (row) => (row.reportsToPositionId ? byId.get(row.reportsToPositionId)?.positionNumber ?? row.reportsToPositionId.slice(0, 8) : "Top"),
    },
    { key: "validFrom", header: "From", mono: true, render: (row) => dayOf(row.validFrom) },
    { key: "validTo", header: "Last day", mono: true, render: (row) => lastDay(row.validTo) },
    { key: "allowOverfill", header: "Overfill", render: (row) => (row.allowOverfill ? <Badge tone="warning">Allowed</Badge> : "No") },
  ];
}

/** Budgeted positions with their FTE and reports-to line (FR-42.3). */
export function PositionsPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listPositions({}, signal), "positions");
  const [more, setMore] = useState<{ items: Position[]; cursor?: string } | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const rows = [...(state.data?.items ?? []), ...(more?.items ?? [])];
  const cursor = more ? more.cursor : state.data?.nextCursor;
  const byId = new Map(rows.map((p) => [p.id, p]));

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const next = await listPositions({ cursor });
      setMore({ items: [...(more?.items ?? []), ...next.items], cursor: next.nextCursor });
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Positions"
        description="Seats in the organisation. A position has an FTE from 0.05 to 2.00 and may report to another position, which is how managers are found on any date."
        actions={
          <Link className={buttonClassName("primary")} to="/hr/positions/new">
            New position
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load positions" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Positions"
          columns={columns(byId)}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No positions yet", description: "Create the top position first, then the positions that report to it." }}
          onRowOpen={(row) => navigate(`/hr/positions/${row.id}`)}
        />
      )}
      {moreError ? (
        <Alert tone="danger" title="Could not load more positions" live="alert">
          <ProblemMessage problem={moreError} />
        </Alert>
      ) : null}
      {!state.error && !state.loading && cursor ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more positions
          </Button>
        </div>
      ) : null}
    </div>
  );
}
