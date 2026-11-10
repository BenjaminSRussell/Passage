import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, type DataTableColumn } from "../../shell/ui";
import { jobSkillGaps, type JobWorkerGap } from "./api/client";
import { DATE, todayIso } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<JobWorkerGap>[] = [
  { key: "personnelNumber", header: "Personnel number", mono: true, render: (row) => <Link to={`/hr/workers/${row.workerId}`}>{row.personnelNumber}</Link> },
  { key: "legalName", header: "Name" },
  {
    key: "gaps",
    header: "Short on",
    render: (row) =>
      row.met ? (
        <Badge tone="success">Meets every requirement</Badge>
      ) : (
        row.gaps.map((g) => `${g.skill} ${g.actual} of ${g.required}`).join(", ")
      ),
  },
];

/** Every worker who holds the job on a date, with the skills they are short on (FR-42.18). */
export function JobGapsCard({ jobId }: { jobId: string }) {
  const [asOf, setAsOf] = useState(todayIso());
  const valid = DATE.test(asOf);
  const state = useLoad((signal) => (valid ? jobSkillGaps(jobId, asOf, signal) : Promise.resolve(null)), `job-gaps|${jobId}|${valid ? asOf : ""}`);
  const short = state.data?.items.filter((i) => !i.met).length ?? 0;

  return (
    <Card title="Skill gap report">
      <div className={styles.stack}>
        <FormField id="gaps-asOf" label="As of" hint="Workers whose primary position carries this job on that day." error={valid ? undefined : "Enter a date."}>
          {(control) => <Input {...control} type="date" value={asOf} invalid={!valid} onChange={(event) => setAsOf(event.target.value)} />}
        </FormField>
        {state.error ? (
          <Alert tone="danger" title="Could not load the gap report" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            <ProblemMessage problem={state.error} />
          </Alert>
        ) : valid ? (
          <>
            {state.data && !state.loading ? (
              <Badge tone={short === 0 ? "success" : "danger"}>
                {state.data.items.length === 0 ? "Nobody holds this job on that day" : short === 0 ? "Everyone meets the profile" : `${short} of ${state.data.items.length} workers short`}
              </Badge>
            ) : null}
            {state.data?.truncated ? <p className={styles.note}>More workers hold this job than the report lists; only the first are shown.</p> : null}
            <DataTable
              label="Job skill gaps"
              columns={columns}
              rows={state.data?.items ?? []}
              getRowKey={(row) => row.workerId}
              loading={state.loading && !state.data}
              empty={{ title: "No workers", description: "Workers appear when their primary position carries this job's code." }}
            />
          </>
        ) : null}
      </div>
    </Card>
  );
}
