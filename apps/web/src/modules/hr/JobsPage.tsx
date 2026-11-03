import { Link, useParams } from "react-router-dom";
import { buttonClassName, Card, PageHeader, type DataTableColumn } from "../../shell/ui";
import { getJob, listJobs, type Job } from "./api/personnel";
import { dash } from "./format";
import styles from "./pages.module.css";
import { BackLink, Entry, Facts, ListPage, Loaded, Money } from "./parts";
import { JobGapsCard } from "./JobGapsCard";
import { SkillProfileCard } from "./SkillProfileCard";
import { useLoad } from "./useLoad";

export function PayRange({ job }: { job: Job }) {
  if (!job.payMin || !job.payMax || !job.payCurrency) return <>{dash}</>;
  return (
    <>
      <Money amount={job.payMin} currency={job.payCurrency} /> to <Money amount={job.payMax} currency={job.payCurrency} />
    </>
  );
}

const columns: DataTableColumn<Job>[] = [
  { key: "code", header: "Code", mono: true, render: (row) => <Link to={`/hr/jobs/${row.id}`}>{row.code}</Link> },
  { key: "title", header: "Title" },
  { key: "jobType", header: "Type", mono: true },
  { key: "jobFunction", header: "Function", render: (row) => row.jobFunction ?? dash },
  { key: "pay", header: "Pay range", render: (row) => <PayRange job={row} /> },
];

/** The job catalog by code. Positions refer to a job by its code. */
export function JobsPage() {
  return (
    <ListPage<Job>
      title="Jobs"
      description="Job titles with an optional pay range. Positions are filled from a job."
      label="Jobs"
      noun="jobs"
      load={(cursor, signal) => listJobs(cursor, signal)}
      loadKey="jobs"
      columns={columns}
      getRowKey={(row) => row.id}
      empty={{ title: "No jobs yet", description: "Add the first job to describe what positions do." }}
      actions={
        <Link className={buttonClassName("primary")} to="/hr/jobs/new">
          New job
        </Link>
      }
    />
  );
}

export function JobDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getJob(id, signal), id);
  return (
    <Loaded state={state} title="Job" what="job" backTo="/hr/jobs">
      {(job) => (
        <div className={styles.stack}>
          <PageHeader title={`Job ${job.code}`} description={job.title} actions={<BackLink to="/hr/jobs">Back to jobs</BackLink>} />
          <Card title="Job">
            <Facts>
              <Entry term="Code" mono>
                {job.code}
              </Entry>
              <Entry term="Title">{job.title}</Entry>
              <Entry term="Type" mono>
                {job.jobType}
              </Entry>
              <Entry term="Function">{job.jobFunction ?? dash}</Entry>
              <Entry term="Pay range">
                <PayRange job={job} />
              </Entry>
            </Facts>
          </Card>
          <SkillProfileCard jobId={job.id} />
          <JobGapsCard jobId={job.id} />
        </div>
      )}
    </Loaded>
  );
}
