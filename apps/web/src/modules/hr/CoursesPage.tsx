import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listCourses, type Course, type Problem } from "./api/client";
import { dash, validityLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<Course>[] = [
  { key: "code", header: "Code", mono: true, render: (row) => <Link to={`/hr/courses/${row.id}`}>{row.code}</Link> },
  { key: "name", header: "Course" },
  { key: "courseGroup", header: "Group", render: (row) => row.courseGroup ?? dash },
  {
    key: "skillOutcomes",
    header: "Skills on a pass",
    render: (row) => (row.skillOutcomes.length === 0 ? dash : row.skillOutcomes.map((o) => `${o.skillCode} ${o.rating}`).join(", ")),
  },
  {
    key: "certificationCode",
    header: "Certificate",
    render: (row) => (row.certificationCode ? `${row.certificationCode} (${validityLabel(row.validityMonths).toLowerCase()})` : dash),
  },
  {
    key: "requiresManagerApproval",
    header: "Approval",
    render: (row) => (row.requiresManagerApproval ? <Badge tone="warning">Manager approval</Badge> : <Badge tone="neutral">Open</Badge>),
  },
];

/** The course catalog (FR-42.14): what a pass awards, and whether registering needs a manager's approval. */
export function CoursesPage() {
  const state = useLoad((signal) => listCourses({}, signal), "courses");
  const [more, setMore] = useState<{ items: Course[]; cursor?: string } | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);

  const rows = [...(state.data?.items ?? []), ...(more?.items ?? [])];
  const cursor = more ? more.cursor : state.data?.nextCursor;

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const next = await listCourses({ cursor });
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
        title="Courses"
        description="Open a course to schedule sessions, register workers and record attendance. A pass raises the course's skills and issues its certificate."
        actions={
          <Link className={buttonClassName("primary")} to="/hr/courses/new">
            New course
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load courses" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Courses"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No courses yet", description: "Add a course such as forklift operation, with the skills and certificate a pass awards." }}
        />
      )}
      {moreError ? (
        <Alert tone="danger" title="Could not load more courses" live="alert">
          <ProblemMessage problem={moreError} />
        </Alert>
      ) : null}
      {!state.error && !state.loading && cursor ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more courses
          </Button>
        </div>
      ) : null}
    </div>
  );
}
