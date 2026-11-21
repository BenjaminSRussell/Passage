import { useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { AiSuggestion } from "../../modules/copilot/AiSuggestion";
import { decideAgentApproval } from "../../modules/copilot/api/proposals";
import { toolActionLabel } from "../../modules/copilot/domain";
import { agentLabel } from "../../modules/copilot/suggestionModel";
import { copy } from "../copy";
import { ProblemMessage } from "../errors";
import { decideInboxItem, MY_WORK_PATH, type InboxDecision } from "../inbox/inbox";
import pageStyles from "../pages/pages.module.css";
import { usePrincipalDuties } from "../principal";
import { Alert, Badge, Button, Card, EmptyState, Skeleton } from "../ui";
import { FEED_VISIBLE_LIMIT, inlineDecisions, isSodRefusal, type FeedItem } from "./feedSources";
import { useAttentionFeed } from "./useAttentionFeed";

type RowState = { status: "pending" | "failed" | "blocked"; problem?: unknown };

interface RowProps {
  item: FeedItem;
  state: RowState | undefined;
  decisions: InboxDecision[];
  onDecide(item: FeedItem, decision: InboxDecision): void;
}

function Row({ item, state, decisions, onDecide }: RowProps) {
  const busy = state?.status === "pending";
  const blocked = state?.status === "blocked";
  const body: ReactNode = (
    <>
      <span>
        {item.kind}: {item.number}. {item.reason}.{" "}
        {item.overdue ? <Badge tone="danger">{copy.attention.overdue}</Badge> : null}
        {item.expiring ? <Badge tone="warning">{copy.attention.expiringSoon}</Badge> : null}{" "}
        {item.dueDate
          ? item.category === "approval"
            ? copy.attention.expiresOn(item.dueDate.slice(0, 10))
            : copy.attention.dueOn(item.dueDate.slice(0, 10))
          : copy.attention.noDueDate}
      </span>
      <span>
        {decisions.map((decision) => (
          <Button
            key={decision}
            size="sm"
            variant={decision === "approve" ? "primary" : "secondary"}
            disabled={busy || blocked}
            aria-label={copy.attention.decisionLabel(decision, item.kind, item.number)}
            onClick={() => onDecide(item, decision)}
          >
            {decision === "approve" ? copy.attention.approve : copy.attention.reject}
          </Button>
        ))}
        <Link to={item.link} className={pageStyles.seeAll} aria-label={copy.attention.reviewLabel(item.kind, item.number)}>
          {copy.attention.review}
        </Link>
      </span>
      {blocked ? <p>{copy.attention.sodReason}</p> : null}
      {state?.status === "failed" ? (
        <ProblemMessage problem={state.problem} lead={`${copy.attention.decisionFailed(item.kind, item.number)} `} fallback={copy.myWork.errorTitle} />
      ) : null}
    </>
  );
  const { origin } = item;
  return (
    <li className={pageStyles.link}>
      {origin?.type === "approval" ? (
        <AiSuggestion
          host="feed-item"
          suggestion={{
            actionId: origin.actionId,
            agentLabel: agentLabel(origin.agentId),
            rationale: item.reason,
            confidencePercent: undefined,
            tools: [toolActionLabel(origin.tool)],
            records: [],
          }}
        >
          {body}
        </AiSuggestion>
      ) : (
        body
      )}
    </li>
  );
}

export function AttentionFeed() {
  const feed = useAttentionFeed();
  const duties = usePrincipalDuties();
  const [rowStates, setRowStates] = useState<Record<string, RowState>>({});
  const [decided, setDecided] = useState<ReadonlySet<string>>(new Set());
  const [announcement, setAnnouncement] = useState("");
  if (!feed.enabled) return null;
  const waiting = feed.loading && feed.items.length === 0;
  const shown = feed.items.filter((item) => !decided.has(item.key));
  const visible = shown.slice(0, FEED_VISIBLE_LIMIT);
  const retry = <Button onClick={feed.reload}>{copy.attention.retry}</Button>;

  const decide = async (item: FeedItem, decision: InboxDecision) => {
    const { origin } = item;
    if (origin === undefined) return;
    setRowStates((prev) => ({ ...prev, [item.key]: { status: "pending" } }));
    try {
      if (origin.type === "inbox") await decideInboxItem(origin.item, decision);
      else await decideAgentApproval(origin.runId, origin.actionId, decision);
    } catch (problem) {
      setRowStates((prev) => ({ ...prev, [item.key]: { status: isSodRefusal(problem) ? "blocked" : "failed", problem } }));
      return;
    }
    setRowStates(({ [item.key]: _done, ...rest }) => rest);
    setDecided((prev) => new Set(prev).add(item.key));
    setAnnouncement(copy.attention.decided(decision, item.kind, item.number));
    feed.reload();
  };

  return (
    <Card title={copy.attention.title} headingLevel={2}>
      <p role="status">{waiting || shown.length === 0 ? "" : copy.attention.summary(shown.length, feed.hasMore)}</p>
      <p role="status" className="visually-hidden">
        {announcement}
      </p>
      {feed.needsEntity ? (
        <Alert tone="danger" live="alert" action={retry}>
          {copy.myWork.needEntity}
        </Alert>
      ) : feed.soleInboxProblem ? (
        <Alert tone="danger" live="alert" action={retry}>
          <ProblemMessage problem={feed.soleInboxProblem} fallback={copy.myWork.errorTitle} />
        </Alert>
      ) : feed.unavailable.length > 0 ? (
        <Alert tone="warning" live="status" action={retry}>
          {copy.attention.partial(feed.unavailable.join(", "))}
        </Alert>
      ) : null}
      {waiting ? (
        <div aria-busy="true">
          <span className="visually-hidden">{copy.attention.loading}</span>
          <Skeleton width="3/4" />
        </div>
      ) : shown.length === 0 ? (
        feed.unavailable.length === 0 && !feed.needsEntity ? (
          <EmptyState compact title={copy.attention.emptyTitle} description={copy.attention.emptyDescription} />
        ) : null
      ) : (
        <>
          <ul className={pageStyles.links} aria-label={copy.attention.title}>
            {visible.map((item) => (
              <Row key={item.key} item={item} state={rowStates[item.key]} decisions={inlineDecisions(item, duties)} onDecide={decide} />
            ))}
          </ul>
          {shown.length > FEED_VISIBLE_LIMIT || feed.hasMore ? (
            <p>
              <Link to={MY_WORK_PATH} className={pageStyles.seeAll}>
                {copy.attention.seeAll}
              </Link>
            </p>
          ) : null}
        </>
      )}
    </Card>
  );
}
