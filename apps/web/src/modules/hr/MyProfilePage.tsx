import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import {
  Alert,
  Button,
  Card,
  EmptyState,
  PageHeader,
  Skeleton,
} from "../../shell/ui";
import { getMyProfile } from "./api/selfService";
import { dash, dayOf } from "./format";
import styles from "./pages.module.css";
import { ProfileChangesCard } from "./ProfileChangesCard";
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

/** The signed-in employee's own record (ESS). The service picks the worker from the sign-in, never from the URL. */
export function MyProfilePage() {
  const state = useLoad((signal) => getMyProfile(signal), "me");

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="My profile" />
        {state.error.code === "HCM-ESS-001" ? (
          <Card>
            <EmptyState
              headingLevel={3}
              title="No worker record is linked to your sign-in"
              description="Ask HR to link your sign-in to your worker record."
            />
          </Card>
        ) : (
          <Alert
            tone="danger"
            title="Could not load your profile"
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
        <PageHeader title="My profile" />
        <Card>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }
  const me = state.data;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="My profile"
        description={`Personnel number ${me.personnelNumber}`}
      />
      <Card title="About me">
        <dl className={styles.details}>
          <Entry term="Legal name">{me.legalName}</Entry>
          <Entry term="Preferred name">{me.preferredName ?? dash}</Entry>
          <Entry term="National ID" mono>
            {me.pii.nationalId}
          </Entry>
          <Entry term="Bank account" mono>
            {me.pii.bankAccount ?? dash}
          </Entry>
          <Entry term="Home address">{me.pii.homeAddress ?? dash}</Entry>
          <Entry term="Created" mono>
            {dayOf(me.createdAt)}
          </Entry>
        </dl>
      </Card>
      <Card title="My job today">
        <dl className={styles.details}>
          <Entry term="Position" mono>
            {me.asOf.positionNumber ?? "No primary position"}
          </Entry>
          <Entry term="FTE share" mono>
            {me.asOf.fteShare ?? dash}
          </Entry>
          <Entry term="Manager">
            {me.asOf.managerWorkerId ? "Assigned" : "Vacant or none"}
          </Entry>
          <Entry term="Employments">
            {me.asOf.employments.length === 0
              ? "None"
              : me.asOf.employments
                  .map((e) => `${e.employmentType} (${e.status})`)
                  .join(", ")}
          </Entry>
        </dl>
      </Card>
      <ProfileChangesCard onApplied={state.reload} />
      <p>
        <Link to="/hr/my/absence">My absence</Link>
      </p>
    </div>
  );
}
