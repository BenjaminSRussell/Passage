import { useState, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { getPosition, type PositionAssignment } from "./api/client";
import { AssignmentForm } from "./AssignmentForm";
import { dayOf, lastDay } from "./format";
import styles from "./pages.module.css";
import { SkillProfileCard } from "./SkillProfileCard";
import { useLoad } from "./useLoad";

const assignmentColumns: DataTableColumn<PositionAssignment>[] = [
  { key: "workerId", header: "Worker", mono: true, render: (row) => <Link to={`/hr/workers/${row.workerId}`}>{row.workerId.slice(0, 8)}</Link> },
  { key: "fteShare", header: "FTE share", align: "end" },
  { key: "isPrimary", header: "Primary", render: (row) => (row.isPrimary ? <Badge tone="info">Primary</Badge> : "No") },
  { key: "validFrom", header: "From", mono: true, render: (row) => dayOf(row.validFrom) },
  { key: "validTo", header: "Last day", mono: true, render: (row) => lastDay(row.validTo) },
];

function Entry({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/** One position with its incumbents over time and a form to assign a worker. */
export function PositionDetailPage() {
  const { id = "" } = useParams();
  const [adding, setAdding] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const state = useLoad((signal) => getPosition(id, signal), id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Position" />
        {state.error.code === "HCM-POS-NOT-FOUND" ? (
          <Card>
            <EmptyState
              headingLevel={3}
              title="We couldn't find this position"
              description="It may belong to another tenant or legal entity, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/hr/positions">
                  Back to positions
                </Link>
              }
            />
          </Card>
        ) : (
          <Alert tone="danger" title="Could not load the position" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        )}
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Position" />
        <Card>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }
  const position = state.data;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Position ${position.positionNumber}`}
        description={`Job ${position.jobCode}, FTE ${position.fte}`}
        meta={position.allowOverfill ? <Badge tone="warning">Overfill allowed</Badge> : undefined}
        actions={
          <Link className={buttonClassName("ghost")} to="/hr/positions">
            Back to positions
          </Link>
        }
      />
      <Card title="Position">
        <dl className={styles.details}>
          <Entry term="Job code" mono>
            {position.jobCode}
          </Entry>
          <Entry term="FTE" mono>
            {position.fte}
          </Entry>
          <Entry term="Reports to">
            {position.reportsToPositionId ? <Link to={`/hr/positions/${position.reportsToPositionId}`}>Open reports-to position</Link> : "Top of the hierarchy"}
          </Entry>
          <Entry term="Department" mono>
            {position.departmentId}
          </Entry>
          <Entry term="Legal entity" mono>
            {position.legalEntityId}
          </Entry>
          <Entry term="Valid" mono>
            {`${dayOf(position.validFrom)} to ${lastDay(position.validTo)}`}
          </Entry>
        </dl>
      </Card>
      <Card
        title="Assignments"
        actions={
          adding ? null : (
            <Button size="sm" onClick={() => setAdding(true)}>
              Assign worker
            </Button>
          )
        }
      >
        <div className={styles.stack}>
          <Alert tone="success" live="status">
            {notice}
          </Alert>
          {adding ? (
            <AssignmentForm
              position={position}
              onCancel={() => setAdding(false)}
              onCreated={(a) => {
                setAdding(false);
                setNotice(`Worker assigned from ${dayOf(a.validFrom)}.`);
                state.reload();
              }}
            />
          ) : null}
          <DataTable
            label="Assignments"
            columns={assignmentColumns}
            rows={position.assignments}
            getRowKey={(row) => row.id}
            empty={{ title: "Vacant", description: "No worker has held this position yet." }}
          />
        </div>
      </Card>
      <SkillProfileCard positionId={position.id} />
    </div>
  );
}
