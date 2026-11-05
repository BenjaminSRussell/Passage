import { Alert, Button, Card, EmptyState, PageHeader, Skeleton } from "../../shell/ui";
import { AbsenceCard } from "./AbsenceCard";
import { getMyProfile } from "./api/selfService";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** ESS: the signed-in employee's balances and requests, and the leave request form. The worker comes from the sign-in. */
export function MyAbsencePage() {
  const state = useLoad((signal) => getMyProfile(signal), "me");

  return (
    <div className={styles.stack}>
      <PageHeader title="My absence" description="Your balances this plan year, your requests, and a new request." />
      {state.error ? (
        state.error.code === "HCM-ESS-001" ? (
          <Card>
            <EmptyState headingLevel={3} title="No worker record is linked to your sign-in" description="Ask HR to link your sign-in to your worker record." />
          </Card>
        ) : (
          <Alert tone="danger" title="Could not load your record" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        )
      ) : !state.data ? (
        <Card>
          <div aria-busy="true">
            <Skeleton width="3/4" />
          </div>
        </Card>
      ) : (
        <AbsenceCard workerId={state.data.id} />
      )}
    </div>
  );
}
