import { useState } from "react";
import { Link } from "react-router-dom";
import { copy } from "../copy";
import { matchesQuery, type NavEntry } from "../workspaces/model";
import { useWorkspaceNav } from "../workspaces/WorkspaceNav";
import { EmptyState, Input, PageHeader } from "../ui";
import styles from "./pages.module.css";

function EntryList({ entries }: { entries: NavEntry[] }) {
  return (
    <ul className={styles.links}>
      {entries.map((e) => (
        <li key={e.to}>
          <Link to={e.to} className={styles.link} aria-label={`${e.label} — ${e.moduleTitle}`}>
            <span>
              {e.label}
              <span className={styles.moduleHint}>{` — ${e.moduleTitle}`}</span>
            </span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

export function AllFeaturesPage() {
  const { areaGroups } = useWorkspaceNav();
  const [query, setQuery] = useState("");
  const searching = query.trim() !== "";
  const groups = areaGroups
    .map((g) => ({
      area: g.area,
      main: g.main.filter((e) => matchesQuery(e, query)),
      advanced: g.advanced.filter((e) => matchesQuery(e, query)),
    }))
    .filter((g) => g.main.length + g.advanced.length > 0);
  const total = groups.reduce((sum, g) => sum + g.main.length + g.advanced.length, 0);

  return (
    <>
      <PageHeader title={copy.allFeatures.pageTitle} description={copy.allFeatures.description} />
      <div className={styles.filter}>
        <label htmlFor="feature-filter">{copy.allFeatures.filterLabel}</label>
        <Input
          id="feature-filter"
          type="search"
          value={query}
          placeholder={copy.allFeatures.filterPlaceholder}
          onChange={(e) => setQuery(e.target.value)}
        />
      </div>
      <p role="status">{searching && total === 0 ? copy.allFeatures.noMatches(query.trim()) : copy.allFeatures.count(total)}</p>
      {groups.length === 0 && !searching ? (
        <EmptyState icon="grid" title={copy.allFeatures.emptyTitle} description={copy.allFeatures.emptyDescription} />
      ) : null}
      {groups.map((g) => (
        <section key={g.area.id} className={styles.areaSection} aria-labelledby={`area-${g.area.id}`}>
          <h2 id={`area-${g.area.id}`} className={styles.areaTitle}>
            {g.area.label}
          </h2>
          {g.main.length > 0 ? <EntryList entries={g.main} /> : null}
          {g.advanced.length > 0 ? (
            <details className={styles.advanced} open={searching}>
              <summary>{`${copy.allFeatures.advanced} (${g.advanced.length})`}</summary>
              <EntryList entries={g.advanced} />
            </details>
          ) : null}
        </section>
      ))}
    </>
  );
}
