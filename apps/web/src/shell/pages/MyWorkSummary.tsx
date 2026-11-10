import { Link } from "react-router-dom";
import { copy } from "../copy";
import { ProblemMessage } from "../errors";
import { MY_WORK_PATH, sourceList } from "../inbox/inbox";
import { countInbox, useSharedInbox } from "../inbox/InboxProvider";
import { Alert, Button, Card, EmptyState, Skeleton, StatGroup, StatTile } from "../ui";
import styles from "./pages.module.css";

export { MY_WORK_PATH };
export const MY_WORK_PREVIEW_COUNT = 5;

function Summary() {
  const inbox = useSharedInbox();
  if (inbox === null) return null;
  const preview = inbox.items.slice(0, MY_WORK_PREVIEW_COUNT);
  const counts = countInbox(inbox.items, inbox.hasMore || inbox.truncated.length > 0);

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
          <EmptyState compact title={copy.myWork.emptyTitle} description={`${copy.myWork.emptyDescription} ${copy.myWork.getStarted}`} />
        ) : null
      ) : (
        <>
          <p role="status">{copy.myWork.summary(inbox.items.length, counts.more)}</p>
          <StatGroup label={copy.myWork.tilesLabel}>
            <StatTile label={copy.myWork.tileWaiting} value={`${counts.total}${counts.more ? "+" : ""}`} />
            <StatTile label={copy.myWork.tileApprovals} value={counts.approvals} />
            <StatTile label={copy.myWork.tileOverdue} value={counts.overdue} tone="danger" />
            <StatTile label={copy.myWork.tileOldest} value={counts.oldestDays} />
          </StatGroup>
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
  if (useSharedInbox() === null) return null;
  return (
    <Card title={copy.myWork.title} headingLevel={2}>
      <Summary />
    </Card>
  );
}
