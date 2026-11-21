import { lazy, Suspense, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useOutletContext } from "react-router-dom";
import { postRoute } from "../../modules/copilot/api/client";
import { AskAnswer } from "../../modules/copilot/AskAnswer";
import { BriefingCard } from "../../modules/copilot/BriefingCard";
import { useCopilotConversation } from "../../modules/copilot/useCopilotConversation";
import { AssistantNotice, useAiAvailability } from "../assistant/AvailabilityProvider";
import { AGENTS_INVOKE_DUTY, createPromptRouter } from "../assistant/router";
import { handOffFeatureFilter } from "../assistant/session";
import { copy } from "../copy";
import { holdsDuty } from "../dutyGate";
import { AssistantPrompt } from "../home/AssistantPrompt";
import { AttentionFeed } from "../home/AttentionFeed";
import { ASSISTANT_DUTY, COPILOT_PATH } from "../home/assistantRouter";
import { PinnedRegion, useLookedUpLabels } from "../pins/PinnedRegion";
import { usePins } from "../pins/PinsProvider";
import { pinKey } from "../pins/pinStore";
import { pinHref, RECORD_LOOKUP } from "../pins/pinTargets";
import { readActivity, resetPersonalization, type Activity } from "../personal/activityStore";
import { orderWorkspaces, recentRecords, suggestedPrompts } from "../personal/homeOrder";
import { plannedRouteFor } from "../plannedRoutes";
import homeStyles from "../home/home.module.css";
import { usePrincipalDuties, usePrincipalIdentity } from "../principal";
import { useRecentPages } from "../recent";
import { RECORD_TYPE_LABEL } from "../search/records";
import { isPathEnabled } from "../tenant/featureRules";
import { useTenantSettings } from "../tenant/TenantSettings";
import { Alert, Badge, Button, Card, CardGrid, ConfirmDialog, EmptyState, Icon, PageHeader } from "../ui";
import { WorkspaceKpis } from "./WorkspaceKpis";
import { ALL_FEATURES_PATH, HOME_WORKSPACE_LIMIT, TOP_TASK_COUNT } from "../workspaces/model";
import { useWorkspaceNav } from "../workspaces/WorkspaceNav";
import styles from "./pages.module.css";

const SETUP_PATH = "/setup";
const BRIEFING_READ_DUTY = "agents.run.read";

// Loaded only in the demo build, like the banner in main.tsx: a static import would put the demo company and its tour
// in every production bundle even though nothing renders them there (UXS-01).
const DemoTour = import.meta.env.MODE === "demo" ? lazy(() => import("../../demo/DemoTour").then((m) => ({ default: m.DemoTour }))) : null;

function SetupBanner() {
  const { setup } = useTenantSettings();
  if (setup === undefined || setup.complete) return null;
  return (
    <Alert
      tone="info"
      title={copy.home.setupTitle}
      action={
        <Link to={SETUP_PATH} className={styles.link}>
          {copy.home.setupLink}
        </Link>
      }
    >
      {copy.home.setupBody(setup.finished, setup.total)}
    </Alert>
  );
}

function Recent({ activity, onReset }: { activity: readonly Activity[]; onReset(): void }) {
  const { pages } = useRecentPages();
  const [confirming, setConfirming] = useState(false);
  const { canOpen } = usePins();
  const records = useMemo(() => recentRecords(activity, canOpen), [activity, canOpen]);
  const looked = useLookedUpLabels(records, canOpen);
  const shown = records.flatMap((record) => {
    const label = RECORD_LOOKUP[record.recordType] === undefined ? RECORD_TYPE_LABEL[record.recordType] : looked.get(pinKey(record));
    return typeof label === "string" ? [{ record, label }] : [];
  });
  return (
    <Card
      title={copy.home.recentTitle}
      headingLevel={2}
      actions={
        <Button size="sm" onClick={() => setConfirming(true)}>
          {copy.home.resetPersonalization}
        </Button>
      }
    >
      {confirming ? (
        <ConfirmDialog
          title={copy.home.resetTitle}
          description={copy.home.resetDescription}
          confirmLabel={copy.home.resetConfirm}
          cancelLabel={copy.home.resetCancel}
          onCancel={() => setConfirming(false)}
          onConfirm={() => {
            setConfirming(false);
            onReset();
          }}
        />
      ) : null}
      {pages.length === 0 && shown.length === 0 ? (
        <EmptyState compact title={copy.home.recentTitle} description={copy.home.recentEmpty} />
      ) : (
        <>
          {pages.length > 0 ? (
            <ul className={styles.links} aria-label={copy.home.recentTitle}>
              {pages.map((p) => (
                <li key={p.to}>
                  <Link to={p.to} className={styles.link}>
                    <span>{p.label}</span>
                    <Icon name="chevronRight" />
                  </Link>
                </li>
              ))}
            </ul>
          ) : null}
          {shown.length > 0 ? (
            <ul className={styles.links} aria-label={copy.home.recentRecords}>
              {shown.map(({ record, label }) => (
                <li key={pinKey(record)}>
                  <Link to={pinHref(record)} className={styles.link}>
                    <span>{label}</span>
                    <Icon name="chevronRight" />
                  </Link>
                </li>
              ))}
            </ul>
          ) : null}
        </>
      )}
    </Card>
  );
}

/**
 * The home page is the role home. For the person's current workspace it shows what is waiting for them, a few
 * numbers that matter to the role, shortcuts to the top tasks and the pages they used last. Their other workspaces are one
 * click away but add no links, so the page stays short however many workspaces the person holds.
 */
export function HomePage() {
  const { held, active, setActive } = useWorkspaceNav();
  const navigate = useNavigate();
  const announce = useOutletContext<((message: string) => void) | undefined>();
  const { features } = useTenantSettings();
  const duties = usePrincipalDuties();
  const identity = usePrincipalIdentity();
  const now = useMemo(() => new Date(), []);
  const { clear: clearRecent } = useRecentPages();
  const [resets, setResets] = useState(0);
  const activity = useMemo(() => readActivity(identity), [identity, resets]);
  const onReset = () => {
    resetPersonalization(identity);
    clearRecent();
    setResets((n) => n + 1);
    announce?.(copy.home.resetDone);
  };
  const availability = useAiAvailability().state;
  const availabilityRef = useRef(availability);
  availabilityRef.current = availability;
  const conversation = useCopilotConversation();
  const conversationRef = useRef(conversation);
  conversationRef.current = conversation;
  // TODO DEPOT-281, DEPOT-282, DEPOT-297: register the navigate and act handlers
  const router = useMemo(
    () =>
      createPromptRouter({
        handlers: {
          ask: (text) => conversationRef.current.send(text, "ask"),
        },
        getAvailability: () => availabilityRef.current,
        hasDuty: (duty) => holdsDuty(duties, duty),
        postRoute,
        searchFallback: (text) => {
          announce?.(copy.home.assistantSearchNote);
          handOffFeatureFilter(text);
          navigate(ALL_FEATURES_PATH);
        },
      }),
    [announce, duties, navigate],
  );
  const assistant =
    availability.status === "enabled" && holdsDuty(duties, ASSISTANT_DUTY) && isPathEnabled(features, COPILOT_PATH) && plannedRouteFor(COPILOT_PATH) === undefined;
  const rest = orderWorkspaces(held.filter((w) => w.id !== active?.id), duties, activity, now);
  const ordered = (active ? [active, ...rest] : rest).slice(0, HOME_WORKSPACE_LIMIT);
  const cards =
    ordered.length === 0 ? (
      <Card>
        <EmptyState icon="grid" title={copy.home.emptyTitle} description={copy.home.emptyDescription} />
      </Card>
    ) : (
      <CardGrid as="ul" label={assistant ? copy.home.browseTitle : copy.home.pageTitle}>
        {ordered.map((w) => (
          <li key={w.id} className={styles.cell}>
            <Card>
              <div className={styles.cardHead}>
                <h3 className={styles.cardTitle}>{w.label}</h3>
                {w.id === active?.id ? <Badge tone="neutral">{copy.home.current}</Badge> : null}
              </div>
              <p className={styles.cardDescription}>{w.description}</p>
              {w.id === active?.id ? (
                <ul className={styles.links} aria-label={`${w.label}: ${copy.home.topTasks}`}>
                  {w.entries.slice(0, TOP_TASK_COUNT).map((n) => (
                    <li key={n.to}>
                      <Link to={n.to} className={styles.link}>
                        <span>{n.label}</span>
                        <Icon name="chevronRight" />
                      </Link>
                    </li>
                  ))}
                </ul>
              ) : (
                <Button size="sm" onClick={() => setActive(w.id)} aria-label={copy.home.switchTo(w.label)}>
                  {copy.home.switchTo(w.label)}
                </Button>
              )}
            </Card>
          </li>
        ))}
      </CardGrid>
    );
  const seeAll = (
    <p>
      <Link to={ALL_FEATURES_PATH}>{copy.home.seeAll}</Link>
    </p>
  );
  const briefing = holdsDuty(duties, BRIEFING_READ_DUTY) ? <BriefingCard /> : null;
  const demoTour = DemoTour ? (
    <Suspense fallback={null}>
      <DemoTour />
    </Suspense>
  ) : null;
  if (!assistant) {
    return (
      <>
        <PageHeader title={copy.home.pageTitle} description={copy.home.description} />
        <AssistantNotice />
        {briefing}
        {demoTour}
        <SetupBanner />
        <AttentionFeed />
        <PinnedRegion />
        <WorkspaceKpis workspace={active} />
        {cards}
        <Recent activity={activity} onReset={onReset} />
        {seeAll}
      </>
    );
  }
  return (
    <>
      <PageHeader title={copy.home.pageTitle} description={copy.home.description} />
      <AssistantNotice />
      <AssistantPrompt onSubmit={router.submit} canAct={holdsDuty(duties, AGENTS_INVOKE_DUTY)} suggestions={suggestedPrompts(duties, activity, now)} />
      <AskAnswer conversation={conversation} />
      {briefing}
      {demoTour}
      <SetupBanner />
      <AttentionFeed />
      <PinnedRegion />
      <WorkspaceKpis workspace={active} />
      <section aria-labelledby="home-browse-title" className={homeStyles.browse}>
        <h2 id="home-browse-title" className={homeStyles.browseTitle}>
          {copy.home.browseTitle}
        </h2>
        {cards}
        {seeAll}
      </section>
      <Recent activity={activity} onReset={onReset} />
    </>
  );
}
