import { useRef, type RefObject } from "react";
import { Link, matchPath, NavLink, useLocation } from "react-router-dom";
import { copy, shortcutHint } from "./copy";
import { useFocusTrap } from "./hooks/useFocusTrap";
import styles from "./Shell.module.css";
import type { ShellNavItem } from "./shellNav";
import { Button, Icon } from "./ui";
import { useWorkspaceNav } from "./workspaces/WorkspaceNav";
import { WorkspaceSwitcher } from "./workspaces/WorkspaceSwitcher";

export interface SidebarProps {
  shellNav: ShellNavItem[];
  drawerOpen: boolean;
  announce(message: string): void;
  searchRef: RefObject<HTMLButtonElement | null>;
  onOpenSearch(): void;
  onCloseDrawer(): void;
}

export function Sidebar({ shellNav, drawerOpen, announce, searchRef, onOpenSearch, onCloseDrawer }: SidebarProps) {
  const asideRef = useRef<HTMLElement>(null);
  const { active, entries } = useWorkspaceNav();
  const { pathname } = useLocation();
  const covers = (to: string) => matchPath({ path: to, end: false }, pathname) !== null;
  const outsideWorkspace =
    shellNav.some((n) => matchPath({ path: n.to, end: true }, pathname) !== null) || active?.entries.some((e) => covers(e.to))
      ? undefined
      : entries.filter((e) => covers(e.to)).sort((a, b) => b.to.length - a.to.length)[0];

  useFocusTrap(asideRef, drawerOpen);

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
      <WorkspaceSwitcher announce={announce} />
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
        {active ? (
          <div className={styles.group}>
            <h2 id="nav-active-workspace" className={styles.groupLabel}>
              {active.label}
            </h2>
            <ul className={styles.list} aria-labelledby="nav-active-workspace">
              {active.entries.map((n) => (
                <li key={n.to}>
                  <NavLink end to={n.to} className={styles.item} onClick={onCloseDrawer}>
                    {n.label}
                  </NavLink>
                </li>
              ))}
            </ul>
          </div>
        ) : null}
        {outsideWorkspace ? (
          <div className={styles.group}>
            <h2 id="nav-current-page" className={styles.groupLabel}>
              {copy.workspaces.currentPage}
            </h2>
            <ul className={styles.list} aria-labelledby="nav-current-page">
              <li>
                <NavLink to={outsideWorkspace.to} className={styles.item} onClick={onCloseDrawer}>
                  {outsideWorkspace.label}
                </NavLink>
              </li>
            </ul>
          </div>
        ) : null}
      </nav>
    </aside>
  );
}
