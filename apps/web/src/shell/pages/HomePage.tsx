import { Link } from "react-router-dom";
import { copy } from "../copy";
import { DemoTour } from "../../demo/DemoTour";
import { useRecentPages } from "../recent";
import { useTenantSettings } from "../tenant/TenantSettings";
import { Alert, Badge, Button, Card, CardGrid, EmptyState, Icon, PageHeader } from "../ui";
import { MyWorkSummary } from "./MyWorkSummary";
import { ALL_FEATURES_PATH, HOME_WORKSPACE_LIMIT, TOP_TASK_COUNT } from "../workspaces/model";
import { useWorkspaceNav } from "../workspaces/WorkspaceNav";
import styles from "./pages.module.css";

const SETUP_PATH = "/setup";

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

function Recent() {
  const { pages } = useRecentPages();
  return (
    <Card title={copy.home.recentTitle} headingLevel={2}>
      {pages.length === 0 ? (
        <EmptyState compact title={copy.home.recentTitle} description={copy.home.recentEmpty} />
      ) : (
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
      )}
    </Card>
  );
}

/**
 * The home page is the role home. For the person's current workspace it shows what is waiting for them, a few
 * numbers about it, shortcuts to the top tasks and the pages they used last. Their other workspaces are one
 * click away but add no links, so the page stays short however many workspaces the person holds.
 */
export function HomePage() {
  const { held, active, setActive } = useWorkspaceNav();
  const ordered = (active ? [active, ...held.filter((w) => w.id !== active.id)] : held).slice(0, HOME_WORKSPACE_LIMIT);
  return (
    <>
      <PageHeader title={copy.home.pageTitle} description={copy.home.description} />
      {import.meta.env.MODE === "demo" ? <DemoTour /> : null}
      <SetupBanner />
      <MyWorkSummary />
      {ordered.length === 0 ? (
        <Card>
          <EmptyState icon="grid" title={copy.home.emptyTitle} description={copy.home.emptyDescription} />
        </Card>
      ) : (
        <CardGrid as="ul" label={copy.home.pageTitle}>
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
      )}
      <Recent />
      <p>
        <Link to={ALL_FEATURES_PATH}>{copy.home.seeAll}</Link>
      </p>
    </>
  );
}
