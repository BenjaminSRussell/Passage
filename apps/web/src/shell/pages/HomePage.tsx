import { Link } from "react-router-dom";
import { copy } from "../copy";
import { Card, CardGrid, EmptyState, Icon, PageHeader } from "../ui";
import { ALL_FEATURES_PATH, TOP_TASK_COUNT } from "../workspaces/model";
import { useWorkspaceNav } from "../workspaces/WorkspaceNav";
import styles from "./pages.module.css";

export function HomePage() {
  const { held, active } = useWorkspaceNav();
  const ordered = active ? [active, ...held.filter((w) => w.id !== active.id)] : held;
  return (
    <>
      <PageHeader title={copy.home.pageTitle} description={copy.home.description} />
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
                </div>
                <p className={styles.cardDescription}>{w.description}</p>
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
              </Card>
            </li>
          ))}
        </CardGrid>
      )}
      <p>
        <Link to={ALL_FEATURES_PATH}>{copy.home.seeAll}</Link>
      </p>
    </>
  );
}
