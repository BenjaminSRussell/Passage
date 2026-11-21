import { useCallback, useEffect, useId, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { useAiAvailability } from "../../shell/assistant/AvailabilityProvider";
import { claimDailyBriefing, localDay } from "../../shell/assistant/briefingDay";
import { AGENTS_INVOKE_DUTY } from "../../shell/assistant/router";
import { copy } from "../../shell/copy";
import { holdsDuty } from "../../shell/dutyGate";
import { formatDateTime } from "../../shell/format";
import { usePrincipalDuties, usePrincipalIdentity } from "../../shell/principal";
import { Alert, Badge, Button, Card, EmptyState, Skeleton } from "../../shell/ui";
import { isAiUnavailable } from "./api/client";
import { briefingItemRoute, generateBriefing, listBriefings, markBriefingRead, type Briefing, type BriefingItem, type BriefingList } from "./api/briefings";
import styles from "./BriefingCard.module.css";
import { GENERATED_BADGE } from "./domain";
import { agentLabel } from "./suggestionModel";
import { useLoad } from "./useLoad";

const POLL_MS = 2000;
const POLL_LIMIT = 15;
const SECONDS_PER_MINUTE = 60;

type Phase = { kind: "idle" } | { kind: "preparing" } | { kind: "limited"; minutes: number };

const wait = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));
const generatedAtOf = (b: Briefing) => b.completedAt ?? b.createdAt;
const counts = (b: Briefing) => [b.changes.length, b.risks.length, b.recommendedActions.length] as const;

interface SectionProps {
  title: string;
  items: BriefingItem[];
  defaultOpen: boolean;
}

function Item({ item }: { item: BriefingItem }) {
  const route = briefingItemRoute(item);
  return (
    <li>
      <span className={styles.source}>{copy.briefing.sourceLabel(agentLabel(item.agentId))}</span> {route === undefined ? item.text : <Link to={route}>{item.text}</Link>}
    </li>
  );
}

function Section({ title, items, defaultOpen }: SectionProps) {
  const [open, setOpen] = useState(defaultOpen);
  const panelId = useId();
  return (
    <div>
      <h3 className={styles.sectionHeading}>
        <Button variant="ghost" size="sm" aria-expanded={open} aria-controls={panelId} onClick={() => setOpen(!open)}>
          {copy.briefing.section(title, items.length)}
        </Button>
      </h3>
      <div id={panelId} hidden={!open}>
        {items.length === 0 ? (
          <p>{copy.briefing.nothing}</p>
        ) : (
          <ul className={styles.items} aria-label={title}>
            {items.map((item, i) => (
              <Item key={`${item.factId}-${i}`} item={item} />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function GeneratedMeta({ briefing }: { briefing: Briefing }) {
  const generatedAt = generatedAtOf(briefing);
  return (
    <p className={styles.meta}>
      <span>{copy.briefing.generatedAt}</span>
      <time dateTime={generatedAt}>{formatDateTime(generatedAt)}</time>
      {briefing.narrative ? <Badge tone="info">{GENERATED_BADGE}</Badge> : null}
    </p>
  );
}

function BriefingBody({ briefing }: { briefing: Briefing }) {
  return (
    <div className={styles.body}>
      <GeneratedMeta briefing={briefing} />
      {briefing.narrative ? null : <p>{copy.briefing.deterministicNote}</p>}
      <Section title={copy.briefing.changes} items={briefing.changes} defaultOpen={false} />
      <Section title={copy.briefing.risks} items={briefing.risks} defaultOpen={false} />
      <Section title={copy.briefing.recommendedActions} items={briefing.recommendedActions} defaultOpen />
    </div>
  );
}

function ReadBriefing({ briefing }: { briefing: Briefing }) {
  const [open, setOpen] = useState(false);
  const bodyId = useId();
  return (
    <div className={styles.body}>
      <p className={styles.meta}>{copy.briefing.readSummary(...counts(briefing))}</p>
      <div>
        <Button variant="ghost" size="sm" aria-expanded={open} aria-controls={bodyId} onClick={() => setOpen(!open)}>
          {open ? copy.briefing.hideRead : copy.briefing.showRead}
        </Button>
      </div>
      <div id={bodyId} hidden={!open}>
        {open ? <BriefingBody briefing={briefing} /> : null}
      </div>
    </div>
  );
}

function PastBriefings({ briefings }: { briefings: Briefing[] }) {
  const [open, setOpen] = useState(false);
  const listId = useId();
  if (briefings.length === 0) return null;
  return (
    <div>
      <h3 className={styles.sectionHeading}>
        <Button variant="ghost" size="sm" aria-expanded={open} aria-controls={listId} onClick={() => setOpen(!open)}>
          {copy.briefing.past(briefings.length)}
        </Button>
      </h3>
      <ul id={listId} hidden={!open} className={styles.items} aria-label={copy.briefing.past(briefings.length)}>
        {briefings.map((b) => (
          <li key={b.id}>
            <time dateTime={generatedAtOf(b)}>{formatDateTime(generatedAtOf(b))}</time> {b.narrative ? <Badge tone="info">{GENERATED_BADGE}</Badge> : null} {copy.briefing.pastSummary(...counts(b))}
          </li>
        ))}
      </ul>
    </div>
  );
}

interface GenerateControlProps {
  label: string;
  canGenerate: boolean;
  phase: Phase;
  onGenerate: () => void;
}

function GenerateControl({ label, canGenerate, phase, onGenerate }: GenerateControlProps) {
  return (
    <div className={styles.body}>
      <div>
        <Button onClick={onGenerate} disabled={!canGenerate || phase.kind !== "idle"}>
          {label}
        </Button>
      </div>
      {canGenerate ? null : <p>{copy.briefing.generateUnavailable}</p>}
      {phase.kind === "limited" ? <p>{copy.briefing.limited(phase.minutes)}</p> : null}
    </div>
  );
}

export function BriefingCard() {
  const duties = usePrincipalDuties();
  const identity = usePrincipalIdentity();
  const availability = useAiAvailability().state;
  const state = useLoad(listBriefings, "briefings");
  const [polled, setPolled] = useState<BriefingList | null>(null);
  const [phase, setPhase] = useState<Phase>({ kind: "idle" });
  const [readIds, setReadIds] = useState<ReadonlySet<string>>(new Set());
  const [notice, setNotice] = useState<string | null>(null);
  const alive = useRef(true);
  const canGenerate = holdsDuty(duties, AGENTS_INVOKE_DUTY) && availability.status === "enabled";
  const list = polled ?? state.data;

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  const generate = useCallback(async (automatic: boolean) => {
    setNotice(null);
    setPhase({ kind: "preparing" });
    const outcome = await generateBriefing();
    if (!alive.current) return;
    if (outcome.kind === "limited") {
      setPhase({ kind: "limited", minutes: Math.ceil(outcome.retryAfterSeconds / SECONDS_PER_MINUTE) });
      return;
    }
    if (outcome.kind === "failed" || (outcome.kind === "busy" && automatic)) {
      setPhase({ kind: "idle" });
      if (outcome.kind === "failed" && !automatic) setNotice(copy.briefing.generateFailed);
      return;
    }
    for (let attempt = 0; attempt < POLL_LIMIT; attempt += 1) {
      await wait(POLL_MS);
      if (!alive.current) return;
      try {
        const fresh = await listBriefings();
        if (!alive.current) return;
        setPolled(fresh);
        if (!fresh.items.some((b) => b.status === "RUNNING")) break;
      } catch {
        break;
      }
    }
    if (alive.current) setPhase({ kind: "idle" });
  }, []);

  useEffect(() => {
    if (state.loading || state.error !== null || state.data === null || availability.status === "checking") return;
    const now = new Date();
    const latest = state.data.items[0];
    if (latest !== undefined && localDay(new Date(latest.createdAt)) === localDay(now)) return;
    if (!claimDailyBriefing(identity, now)) return;
    if (canGenerate) void generate(true);
  }, [state.loading, state.error, state.data, availability.status, canGenerate, identity, generate]);

  async function markRead(id: string) {
    setNotice(null);
    try {
      await markBriefingRead(id);
      if (alive.current) setReadIds(new Set([...readIds, id]));
    } catch {
      if (alive.current) setNotice(copy.briefing.markReadFailed);
    }
  }

  const ready = list === null ? [] : list.items.filter((b) => b.status === "READY");
  const current = ready[0];
  const preparing = phase.kind === "preparing";
  const generateNow = () => void generate(false);

  return (
    <Card title={copy.briefing.title} headingLevel={2}>
      <p role="status">{preparing ? copy.briefing.preparing : ""}</p>
      {list === null && state.loading ? (
        <div aria-busy="true">
          <span className="visually-hidden">{copy.briefing.loading}</span>
          <Skeleton height="tile" />
        </div>
      ) : list === null && isAiUnavailable(state.failure) ? (
        <p>{copy.briefing.unavailable}</p>
      ) : list === null ? (
        <Alert tone="danger" live="alert" title={copy.briefing.errorTitle} action={<Button onClick={state.reload}>{copy.briefing.retry}</Button>}>
          {copy.briefing.errorBody}
        </Alert>
      ) : (
        <div className={styles.body}>
          {notice === null ? null : (
            <Alert tone="warning" live="status">
              {notice}
            </Alert>
          )}
          {current === undefined ? (
            preparing ? null : (
              <EmptyState compact title={copy.briefing.emptyTitle} description={copy.briefing.emptyDescription} action={<GenerateControl label={copy.briefing.generate} canGenerate={canGenerate} phase={phase} onGenerate={generateNow} />} />
            )
          ) : (
            <>
              {current.readAt !== null || readIds.has(current.id) ? (
                <ReadBriefing briefing={current} />
              ) : (
                <>
                  <BriefingBody briefing={current} />
                  <div>
                    <Button onClick={() => void markRead(current.id)}>{copy.briefing.markRead}</Button>
                  </div>
                </>
              )}
              <GenerateControl label={copy.briefing.generateAgain} canGenerate={canGenerate} phase={phase} onGenerate={generateNow} />
              <PastBriefings briefings={ready.slice(1)} />
            </>
          )}
        </div>
      )}
    </Card>
  );
}
