import { useMemo } from "react";
import { Link } from "react-router-dom";
import type { PassageModule } from "../../modules/registry";
import { copy } from "../copy";
import { visibleModules } from "../dutyGate";
import { usePrincipalDuties } from "../principal";
import { Card, CardGrid, EmptyState, Icon, PageHeader } from "../ui";
import { workspaceIcon } from "../workspaceIcons";
import styles from "./pages.module.css";

export interface HomePageProps {
  modules: PassageModule[];
}

export function HomePage({ modules: allModules }: HomePageProps) {
  const duties = usePrincipalDuties();
  const modules = useMemo(() => visibleModules(allModules, duties), [allModules, duties]);
  return (
    <>
      <PageHeader title={copy.home.pageTitle} description={copy.home.description} />
      {modules.length === 0 ? (
        <Card>
          <EmptyState icon="grid" title={copy.home.emptyTitle} description={copy.home.emptyDescription} />
        </Card>
      ) : (
        <CardGrid as="ul" label={copy.home.pageTitle}>
          {modules.map((m) => (
            <li key={m.id} className={styles.cell}>
              <Card>
                <div className={styles.cardHead}>
                  <span className={styles.tile}>
                    <Icon name={workspaceIcon(m.id)} size={20} />
                  </span>
                  <h3 className={styles.cardTitle}>{m.title}</h3>
                </div>
                {m.description ? <p className={styles.cardDescription}>{m.description}</p> : null}
                <ul className={styles.links}>
                  {m.nav.map((n) => (
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
    </>
  );
}
