import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listDowntimeReasons, updateDowntimeReason, type DowntimeReason, type Problem } from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** The downtime reason code master: the codes a supervisor chooses when coding a downtime (FR-25.11). */
export function DowntimeReasonsPage() {
  const state = useLoad((signal) => listDowntimeReasons({}, signal), "downtime-reasons");
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);

  async function toggle(reason: DowntimeReason) {
    setBusy(reason.id);
    setFailure(null);
    try {
      await updateDowntimeReason(reason, { active: !reason.active });
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
      // A stale version (SF-0143) is fixed by reading the list again.
      state.reload();
    } finally {
      setBusy(null);
    }
  }

  const columns: DataTableColumn<DowntimeReason>[] = [
    { key: "code", header: "Code", mono: true },
    { key: "name", header: "Name" },
    { key: "category", header: "Category", render: (row) => (row.category === "planned" ? <Badge tone="neutral">Planned</Badge> : <Badge tone="warning">Unplanned</Badge>) },
    { key: "active", header: "Status", render: (row) => (row.active ? <Badge tone="success">Active</Badge> : <Badge tone="neutral">Inactive</Badge>) },
    {
      key: "action",
      header: "Action",
      render: (row) => (
        <Button size="sm" loading={busy === row.id} aria-label={`${row.active ? "Deactivate" : "Activate"} downtime reason ${row.code}`} onClick={() => void toggle(row)}>
          {row.active ? "Deactivate" : "Activate"}
        </Button>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Downtime reasons"
        description="Once a reason is defined, coding a downtime takes one of the active codes and the downtime takes the code's category. Until then a downtime is coded with free text."
        actions={
          <Link className={buttonClassName("primary")} to="/shopfloor/downtime-reasons/new">
            New downtime reason
          </Link>
        }
      />
      {failure ? (
        <Alert tone="danger" title="Could not change the reason" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load downtime reasons" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Downtime reasons"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No downtime reasons yet", description: "Create one, such as JAM or CHANGEOVER, so stops are coded the same way everywhere." }}
        />
      )}
    </div>
  );
}
