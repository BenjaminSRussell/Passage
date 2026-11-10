import { useState, type ReactNode } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Skeleton,
  StatusPill,
  type DataTableColumn,
} from "../../shell/ui";
import { AbsenceCard } from "./AbsenceCard";
import { BenefitsCard } from "./BenefitsCard";
import { CertificationsCard } from "./CertificationsCard";
import { getWorker, listEmployments, workerAsOf, type Employment, type WorkerDetail } from "./api/client";
import { EmploymentForm } from "./EmploymentForm";
import { assignmentKindLabel, dash, DATE, dayOf, employmentTypeLabel, lastDay, todayIso, workerTypeLabel } from "./format";
import styles from "./pages.module.css";
import { PiiCard } from "./PiiCard";
import { SkillsCard } from "./SkillsCard";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const employmentColumns: DataTableColumn<Employment>[] = [
  { key: "legalEntityId", header: "Legal entity", mono: true },
  { key: "employmentType", header: "Type", render: (row) => employmentTypeLabel[row.employmentType] ?? row.employmentType },
  { key: "assignmentKind", header: "Kind", render: (row) => <Badge tone="neutral">{assignmentKindLabel[row.assignmentKind] ?? row.assignmentKind}</Badge> },
  { key: "hireDate", header: "Hire date", mono: true },
  { key: "validTo", header: "Last day", mono: true, render: (row) => row.terminationDate ?? lastDay(row.validTo) },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status.toLowerCase()} /> },
];

interface Detail {
  worker: WorkerDetail;
  employments: Employment[];
}

function Entry({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/** Employments, primary position and manager of the worker on one date (FR-42.4, BR-42.1). */
function AsOfCard({ workerId }: { workerId: string }) {
  const [params, setParams] = useSearchParams();
  const date = params.get("date") ?? todayIso();
  const valid = DATE.test(date);
  const state = useLoad((signal) => workerAsOf(workerId, date, signal), `${workerId}|${date}`);

  return (
    <Card title="As of a date">
      <div className={styles.stack}>
        <div className={styles.toolbar}>
          <FormField id="as-of-date" label="As of" hint="Ranges include their start day and end before their end day.">
            {(control) => (
              <Input {...control} type="date" value={date} onChange={(event) => event.target.value && setParams({ date: event.target.value }, { replace: true })} />
            )}
          </FormField>
        </div>
        {!valid ? (
          <Alert tone="warning">Enter the date as YYYY-MM-DD.</Alert>
        ) : state.error ? (
          <Alert tone="danger" title="Could not load the as-of view" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            <ProblemMessage problem={state.error} />
          </Alert>
        ) : state.loading || !state.data ? (
          <div aria-busy="true">
            <Skeleton width="3/4" />
            <Skeleton width="1/2" />
          </div>
        ) : (
          <dl className={styles.details} aria-label={`Worker as of ${state.data.date}`}>
            <Entry term="Primary position" mono>
              {state.data.positionId ? <Link to={`/hr/positions/${state.data.positionId}`}>{state.data.positionNumber}</Link> : "No primary position"}
            </Entry>
            <Entry term="FTE share" mono>
              {state.data.fteShare ?? dash}
            </Entry>
            <Entry term="Manager">
              {state.data.managerWorkerId ? <Link to={`/hr/workers/${state.data.managerWorkerId}`}>Open manager</Link> : "Vacant or none"}
            </Entry>
            <Entry term="Employments on this date">
              {state.data.employments.length === 0
                ? "None"
                : state.data.employments.map((e) => `${assignmentKindLabel[e.assignmentKind] ?? e.assignmentKind} at ${e.legalEntityId.slice(0, 8)}`).join(", ")}
            </Entry>
          </dl>
        )}
      </div>
    </Card>
  );
}

/** One worker: identity, masked personal data, employments, the as-of view and absence. */
export function WorkerDetailPage() {
  const { id = "" } = useParams();
  const [adding, setAdding] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const state = useLoad<Detail>(async (signal) => {
    const [worker, employments] = await Promise.all([getWorker(id, signal), listEmployments(id, signal)]);
    return { worker, employments };
  }, id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Worker" />
        {state.error.code === "HCM-WRK-NOT-FOUND" ? (
          <Card>
            <EmptyState
              headingLevel={3}
              title="We couldn't find this worker"
              description="It may belong to another tenant, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/hr/workers">
                  Back to workers
                </Link>
              }
            />
          </Card>
        ) : (
          <Alert tone="danger" title="Could not load the worker" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            <ProblemMessage problem={state.error} />
          </Alert>
        )}
      </div>
    );
  }
  if (state.loading && !state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Worker" />
        <Card>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }
  const { worker, employments } = state.data as Detail;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={worker.preferredName ? `${worker.preferredName} (${worker.legalName})` : worker.legalName}
        description={`Personnel number ${worker.personnelNumber}`}
        meta={<Badge tone="neutral">{workerTypeLabel[worker.workerType] ?? worker.workerType}</Badge>}
        actions={
          <Link className={buttonClassName("ghost")} to="/hr/workers">
            Back to workers
          </Link>
        }
      />
      <Card title="Worker">
        <dl className={styles.details}>
          <Entry term="Personnel number" mono>
            {worker.personnelNumber}
          </Entry>
          <Entry term="Legal name">{worker.legalName}</Entry>
          <Entry term="Preferred name">{worker.preferredName ?? dash}</Entry>
          <Entry term="Party id" mono>
            {worker.partyPersonId}
          </Entry>
          <Entry term="Sign-in user" mono>
            {worker.userId ?? dash}
          </Entry>
          <Entry term="Created" mono>
            {dayOf(worker.createdAt)}
          </Entry>
        </dl>
      </Card>
      <PiiCard workerId={worker.id} masked={worker.pii} />
      <Card
        title="Employments"
        actions={
          adding ? null : (
            <Button size="sm" onClick={() => setAdding(true)}>
              Start employment
            </Button>
          )
        }
      >
        <div className={styles.stack}>
          <Alert tone="success" live="status">
            {notice}
          </Alert>
          {adding ? (
            <EmploymentForm
              workerId={worker.id}
              onCancel={() => setAdding(false)}
              onCreated={(e) => {
                setAdding(false);
                setNotice(`Employment from ${e.hireDate} started.`);
                state.reload();
              }}
            />
          ) : null}
          <DataTable
            label="Employments"
            columns={employmentColumns}
            rows={employments}
            getRowKey={(row) => row.id}
            empty={{ title: "No employments yet", description: "Start an employment so the worker can hold a position." }}
          />
        </div>
      </Card>
      <AsOfCard workerId={worker.id} />
      <AbsenceCard workerId={worker.id} />
      <BenefitsCard workerId={worker.id} />
      <SkillsCard workerId={worker.id} />
      <p>
        <Link to={`/hr/workers/${worker.id}/performance`}>Goals and reviews</Link>
      </p>
      <CertificationsCard workerId={worker.id} />
    </div>
  );
}
