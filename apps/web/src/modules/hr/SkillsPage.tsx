import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listSkills, type Problem, type Skill } from "./api/client";
import { dayOf } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<Skill>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Skill" },
  { key: "scaleMax", header: "Rating scale", mono: true, render: (row) => `1 to ${row.scaleMax}` },
  { key: "createdAt", header: "Added", mono: true, render: (row) => dayOf(row.createdAt) },
];

/** The skill catalog (FR-42.10): what workers are rated on, each with its rating scale. */
export function SkillsPage() {
  const state = useLoad((signal) => listSkills({}, signal), "skills");
  const [more, setMore] = useState<{ items: Skill[]; cursor?: string } | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);

  const rows = [...(state.data?.items ?? []), ...(more?.items ?? [])];
  const cursor = more ? more.cursor : state.data?.nextCursor;

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const next = await listSkills({ cursor });
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
        title="Skills"
        description="Workers are rated on these skills and positions require them at a minimum rating. Rate a worker or record a certificate on the worker's page."
        actions={
          <div className={styles.rowActions}>
            <Link className={buttonClassName("secondary")} to="/hr/skills/search">
              Find workers by skill
            </Link>
            <Link className={buttonClassName("secondary")} to="/hr/certifications/expiring">
              Expiring certifications
            </Link>
            <Link className={buttonClassName("primary")} to="/hr/skills/new">
              New skill
            </Link>
          </div>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load skills" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Skills"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No skills yet", description: "Add skills such as welding or PLC programming, each with a rating scale." }}
        />
      )}
      {moreError ? (
        <Alert tone="danger" title="Could not load more skills" live="alert">
          <ProblemMessage problem={moreError} />
        </Alert>
      ) : null}
      {!state.error && !state.loading && cursor ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more skills
          </Button>
        </div>
      ) : null}
    </div>
  );
}
