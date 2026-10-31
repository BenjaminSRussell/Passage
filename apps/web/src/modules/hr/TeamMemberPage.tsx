import type { ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  EmptyState,
  PageHeader,
  Skeleton,
} from "../../shell/ui";
import { getTeamMember } from "./api/selfService";
import { dash } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

function Entry({
  term,
  children,
  mono,
}: {
  term: string;
  children: ReactNode;
  mono?: boolean;
}) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>
        {children}
      </dd>
    </div>
  );
}

const backToTeam = (
  <Link className={buttonClassName("ghost")} to="/hr/my/team">
    Back to my team
  </Link>
);

/** One direct report (MSS). Anyone who is not on the caller's team is a 404. */
export function TeamMemberPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getTeamMember(id, signal), `member|${id}`);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Team member" actions={backToTeam} />
        {state.error.code === "HCM-WRK-NOT-FOUND" ? (
          <Card>
            <EmptyState
              headingLevel={3}
              title="This person isn't on your team"
              description="You can only open people who report to you."
            />
          </Card>
        ) : (
          <Alert
            tone="danger"
            title="Could not load this person"
            live="alert"
            action={
              <Button size="sm" onClick={state.reload}>
                Try again
              </Button>
            }
          >
            {state.error.message} ({state.error.code})
          </Alert>
        )}
      </div>
    );
  }
  if (state.loading || !state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Team member" actions={backToTeam} />
        <Card>
          <Skeleton width="3/4" />
        </Card>
      </div>
    );
  }
  const member = state.data;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={
          member.preferredName
            ? `${member.preferredName} (${member.legalName})`
            : member.legalName
        }
        description={`Personnel number ${member.personnelNumber}`}
        actions={backToTeam}
      />
      <Card title="Job today">
        <dl className={styles.details}>
          <Entry term="Position" mono>
            {member.asOf.positionNumber ?? "No primary position"}
          </Entry>
          <Entry term="FTE share" mono>
            {member.asOf.fteShare ?? dash}
          </Entry>
          <Entry term="Employments">
            {member.asOf.employments.length === 0
              ? "None"
              : member.asOf.employments
                  .map((e) => `${e.employmentType} (${e.status})`)
                  .join(", ")}
          </Entry>
        </dl>
      </Card>
      <p>
        <Link to="/hr/absence/requests">Absence requests for my team</Link>
      </p>
    </div>
  );
}
