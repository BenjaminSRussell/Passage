import { Link } from "react-router-dom";
import { copy } from "../copy";
import { holdsDuty } from "../dutyGate";
import { ProblemMessage } from "../errors";
import { INBOX_DUTY, sourceList } from "../inbox/inbox";
import { useInbox } from "../inbox/useInbox";
import { useWorkContext } from "../lookup/workContextState";
import { usePrincipalDuties } from "../principal";
import { Alert, Button, Card, EmptyState, Skeleton } from "../ui";
import styles from "./pages.module.css";

export const MY_WORK_PATH = "/workflow/inbox";
export const MY_WORK_PREVIEW_COUNT = 5;

function Summary() {
  const { legalEntityId } = useWorkContext();
  const inbox = useInbox(legalEntityId, true);
  const preview = inbox.items.slice(0, MY_WORK_PREVIEW_COUNT);

  if (inbox.status === "loading") {
    return (
      <div aria-busy="true">
        <span className="visually-hidden">Loading your work…</span>
        <Skeleton width="3/4" />
      </div>
    );
  }
  if (inbox.status === "error") {
    return (
      <Alert tone="danger" live="alert" action={<Button onClick={inbox.reload}>{copy.myWork.retry}</Button>}>
        {inbox.needsEntity ? copy.myWork.needEntity : <ProblemMessage problem={inbox.problem} fallback={copy.myWork.errorTitle} />}
      </Alert>
    );
  }
  return (
    <>
      {inbox.unavailable.length > 0 ? (
        <Alert tone="warning" live="status" action={<Button onClick={inbox.reload}>{copy.myWork.retry}</Button>}>
          {copy.myWork.partial(sourceList(inbox.unavailable))}
        </Alert>
      ) : null}
      {inbox.items.length === 0 ? (
        inbox.unavailable.length === 0 ? (
          <EmptyState compact title={copy.myWork.emptyTitle} description={copy.myWork.emptyDescription} />
        ) : null
      ) : (
        <>
          <p role="status">{copy.myWork.summary(inbox.items.length, inbox.hasMore)}</p>
          <ul className={styles.links} aria-label={copy.myWork.title}>
            {preview.map((item) => (
              <li key={`${item.source}:${item.id}`}>
                <Link to={item.link} className={styles.link}>
                  <span>
                    {item.kind}: {item.number}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
          <p>
            <Link to={MY_WORK_PATH}>{copy.myWork.seeAll}</Link>
          </p>
        </>
      )}
    </>
  );
}

export function MyWorkSummary() {
  if (!holdsDuty(usePrincipalDuties(), INBOX_DUTY)) return null;
  return (
    <Card title={copy.myWork.title} headingLevel={2}>
      <Summary />
    </Card>
  );
}
