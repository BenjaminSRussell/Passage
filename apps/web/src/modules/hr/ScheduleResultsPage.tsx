import { useParams, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, EmptyState, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { scheduleResults, type GroupStat, type QuestionStat, type Stats } from "./api/questionnaires";
import styles from "./pages.module.css";
import { BackLink, CheckField, LoadFailure, problemText } from "./parts";
import { useLoad } from "./useLoad";

const questionColumns: DataTableColumn<QuestionStat>[] = [
  { key: "code", header: "Question", mono: true },
  { key: "type", header: "Type" },
  { key: "answered", header: "Answered", align: "end", mono: true },
  { key: "mean", header: "Mean", align: "end", mono: true, render: (row) => row.mean ?? "" },
  {
    key: "distribution",
    header: "Answers",
    render: (row) =>
      row.distribution
        ? Object.entries(row.distribution)
            .map(([answer, count]) => `${answer}: ${count}`)
            .join(", ")
        : "",
  },
];

const groupColumns: DataTableColumn<GroupStat>[] = [
  { key: "code", header: "Result group", mono: true },
  { key: "responses", header: "Responses", align: "end", mono: true },
  { key: "meanScore", header: "Mean score", align: "end", mono: true, render: (row) => row.meanScore ?? "" },
];

function StatsTables({ stats, label }: { stats: Stats; label: string }) {
  return (
    <div className={styles.stack}>
      <DataTable label={`${label} questions`} columns={questionColumns} rows={stats.questions} getRowKey={(row) => row.code} empty={{ title: "No answers yet" }} />
      {stats.resultGroups.length > 0 ? (
        <DataTable label={`${label} result groups`} columns={groupColumns} rows={stats.resultGroups} getRowKey={(row) => row.code} />
      ) : null}
    </div>
  );
}

/** A schedule's response rate and statistics. An anonymous questionnaire's results wait for the close and never show a group under five. */
export function ScheduleResultsPage() {
  const { id = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const byDepartment = params.get("by") === "department";
  const state = useLoad((signal) => scheduleResults(id, byDepartment, signal), `${id}|${byDepartment}`);
  const toggle = (
    <CheckField
      id="results-by-department"
      label="Break down by department"
      checked={byDepartment}
      onChange={(on) => setParams(on ? { by: "department" } : {}, { replace: true })}
    />
  );
  const back = <BackLink to="/hr/questionnaires">Back to questionnaires</BackLink>;
  const header = <PageHeader title="Questionnaire results" actions={back} />;
  const error = state.error;

  if (error?.code === "HCM-QST-NOT-CLOSED") {
    return (
      <div className={styles.stack}>
        {header}
        <Card>
          <EmptyState headingLevel={3} title="Results are not released yet" description={problemText(error)} />
        </Card>
      </div>
    );
  }
  if (error?.code === "HCM-QST-SCOPE") {
    return (
      <div className={styles.stack}>
        {header}
        <Alert tone="warning" title="You can't see these results" live="alert">
          {problemText(error)}
        </Alert>
      </div>
    );
  }
  if (error?.code === "HCM-QST-002") {
    return (
      <div className={styles.stack}>
        {header}
        <Alert
          tone="warning"
          title="This breakdown is hidden"
          live="alert"
          action={
            <Button size="sm" onClick={() => setParams({}, { replace: true })}>
              Show overall results
            </Button>
          }
        >
          {problemText(error)}
        </Alert>
      </div>
    );
  }
  if (error) {
    return (
      <div className={styles.stack}>
        {header}
        <LoadFailure error={error} reload={state.reload} what="schedule" backTo="/hr/questionnaires" />
      </div>
    );
  }
  const results = state.data;
  if (!results) {
    return (
      <div className={styles.stack} aria-busy="true">
        {header}
        <Alert tone="info" live="status">
          Loading the results
        </Alert>
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Questionnaire results" meta={results.anonymous ? <Badge tone="info">Anonymous</Badge> : undefined} actions={back} />
      <Card title="Response rate">
        <StatGroup label="Response rate">
          <StatTile label="Invited" value={String(results.invited)} />
          <StatTile label="Responded" value={String(results.responded)} />
          <StatTile label="Response rate" value={results.responseRate} />
        </StatGroup>
      </Card>
      {results.suppressed ? (
        <Alert tone="info" title="Results are hidden to protect anonymity" live="status">
          Fewer than 5 people have responded to this anonymous questionnaire, so no statistics are shown.
        </Alert>
      ) : (
        <>
          <div className={styles.toolbar}>{toggle}</div>
          <Card title="Overall">
            <StatsTables stats={results.overall} label="Overall" />
          </Card>
          {byDepartment
            ? (results.groups ?? []).map((group, i) => (
                <Card key={group.departmentId ?? `none-${i}`} title={group.departmentId ? `Department ${group.departmentId.slice(0, 8)}` : "No department"}>
                  <StatsTables stats={group.stats} label={group.departmentId ?? "No department"} />
                </Card>
              ))
            : null}
        </>
      )}
    </div>
  );
}
