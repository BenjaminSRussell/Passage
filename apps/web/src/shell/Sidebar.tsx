import { useEffect, useMemo, useRef, useState, type RefObject } from "react";
import { Link, NavLink, matchPath, useLocation } from "react-router-dom";
import type { PassageModule } from "../modules/registry";
import { groupByArea } from "./areas";
import { copy, shortcutHint } from "./copy";
import { useFocusTrap } from "./hooks/useFocusTrap";
import styles from "./Shell.module.css";
import type { ShellNavItem } from "./shellNav";
import { Button, Icon } from "./ui";

export interface SidebarProps {
  modules: PassageModule[];
  shellNav: ShellNavItem[];
  drawerOpen: boolean;
  searchRef: RefObject<HTMLButtonElement | null>;
  onOpenSearch(): void;
  onCloseDrawer(): void;
}

/** Pages listed per open module before a "Show more" toggle; modules order their nav with everyday work first. */
export const PAGE_LIMIT = 8;

/** The module whose nav entry is the longest prefix of the path, so detail pages (/sales/orders/123) keep their module open. */
function ownerOf(pathname: string, modules: PassageModule[]): PassageModule | undefined {
  let best: { module: PassageModule; length: number } | undefined;
  for (const m of modules) {
    for (const n of m.nav) {
      if (matchPath({ path: n.to, end: false }, pathname) !== null && (best === undefined || n.to.length > best.length)) {
        best = { module: m, length: n.to.length };
      }
    }
  }
  return best?.module;
}

export function Sidebar({ modules, shellNav, drawerOpen, searchRef, onOpenSearch, onCloseDrawer }: SidebarProps) {
  const { pathname } = useLocation();
  const asideRef = useRef<HTMLElement>(null);
  const groups = useMemo(() => groupByArea(modules), [modules]);
  // One area and one module open at a time, so the sidebar stays short: areas list modules, only the open module lists pages.
  const [openArea, setOpenArea] = useState<string | null>(null);
  const [openModule, setOpenModule] = useState<string | null>(null);
  const [showAll, setShowAll] = useState<Record<string, boolean>>({});

  useFocusTrap(asideRef, drawerOpen);

  useEffect(() => {
    const owner = ownerOf(pathname, modules);
    if (owner) {
      setOpenModule(owner.id);
      const index = owner.nav.findIndex((n) => matchPath({ path: n.to, end: true }, pathname) !== null);
      if (index >= PAGE_LIMIT) setShowAll((current) => ({ ...current, [owner.id]: true }));
      setOpenArea(groups.find((g) => g.modules.some((m) => m.id === owner.id))?.area.id ?? null);
    }
  }, [pathname, modules, groups]);

  const dialogProps = drawerOpen ? { role: "dialog", "aria-modal": true, "aria-label": "Navigation" } : {};

  return (
    <aside
      id="sidebar"
      ref={asideRef}
      className={styles.sidebar}
      data-open={drawerOpen}
      onKeyDown={(e) => {
        if (drawerOpen && e.key === "Escape") {
          e.stopPropagation();
          onCloseDrawer();
        }
      }}
      {...dialogProps}
    >
      <div className={styles.brandRow}>
        {drawerOpen ? (
          <Button variant="ghost" size="sm" className={styles.closeNav} aria-label={copy.buttons.closeNavigation} onClick={onCloseDrawer}>
            <Icon name="close" />
          </Button>
        ) : null}
        <Link to="/" className={styles.brand} onClick={onCloseDrawer}>
          <span className={styles.logo} aria-hidden="true">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.25" strokeLinecap="round" strokeLinejoin="round" focusable="false" aria-hidden="true">
              <path d="M4 18c3-8 7-12 16-12M4 18c5 0 9-2 12-6" />
            </svg>
          </span>
          Passage
        </Link>
      </div>
      <div className={styles.searchWrap}>
        <button
          ref={searchRef}
          type="button"
          className={styles.search}
          aria-keyshortcuts="Control+K Meta+K"
          aria-haspopup="dialog"
          onClick={onOpenSearch}
        >
          <Icon name="search" />
          <span className={styles.searchLabel}>{copy.buttons.search}</span>
          <kbd aria-hidden="true">{shortcutHint}</kbd>
        </button>
      </div>
      <nav aria-label="Workspaces" className={styles.nav}>
        <ul className={styles.list}>
          {shellNav.map((n) => (
            <li key={n.to}>
              <NavLink end to={n.to} className={styles.item} onClick={onCloseDrawer}>
                <Icon name={n.icon} />
                {n.label}
              </NavLink>
            </li>
          ))}
        </ul>
        <ul className={styles.list}>
          {groups.map(({ area, modules: members }) => {
            const areaOpen = openArea === area.id;
            const areaHeaderId = `nav-area-${area.id}`;
            const areaListId = `nav-area-list-${area.id}`;
            return (
              <li key={area.id} className={styles.group}>
                <button
                  type="button"
                  id={areaHeaderId}
                  className={styles.areaHeader}
                  aria-expanded={areaOpen}
                  aria-controls={areaListId}
                  onClick={() => setOpenArea(areaOpen ? null : area.id)}
                >
                  <Icon name={area.icon} />
                  <span className={styles.groupTitle}>{area.title}</span>
                  <span className={styles.chevron}>
                    <Icon name="chevronRight" />
                  </span>
                </button>
                <ul id={areaListId} className={styles.list} aria-labelledby={areaHeaderId} hidden={!areaOpen}>
                  {members.map((m) => {
                    const moduleOpen = openModule === m.id;
                    const headerId = `nav-group-${m.id}`;
                    const listId = `nav-list-${m.id}`;
                    return (
                      <li key={m.id} className={styles.group}>
                        <button
                          type="button"
                          id={headerId}
                          className={styles.moduleHeader}
                          aria-expanded={moduleOpen}
                          aria-controls={listId}
                          onClick={() => setOpenModule(moduleOpen ? null : m.id)}
                        >
                          <span className={styles.groupTitle}>{m.title}</span>
                          <span className={styles.chevron}>
                            <Icon name="chevronRight" />
                          </span>
                        </button>
                        <ul id={listId} className={styles.list} aria-labelledby={headerId} hidden={!moduleOpen}>
                          {(showAll[m.id] ? m.nav : m.nav.slice(0, PAGE_LIMIT)).map((n) => (
                            <li key={n.to}>
                              <NavLink end to={n.to} className={`${styles.item} ${styles.groupItem}`} onClick={onCloseDrawer}>
                                {n.label}
                              </NavLink>
                            </li>
                          ))}
                          {m.nav.length > PAGE_LIMIT ? (
                            <li>
                              <button
                                type="button"
                                className={`${styles.item} ${styles.groupItem} ${styles.moreToggle}`}
                                aria-expanded={showAll[m.id] === true}
                                onClick={() => setShowAll((current) => ({ ...current, [m.id]: !current[m.id] }))}
                              >
                                {showAll[m.id] ? copy.nav.showFewer : copy.nav.showMore(m.nav.length - PAGE_LIMIT)}
                              </button>
                            </li>
                          ) : null}
                        </ul>
                      </li>
                    );
                  })}
                </ul>
              </li>
            );
          })}
        </ul>
      </nav>
    </aside>
  );
}
