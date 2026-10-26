import { useEffect, useRef, useState, type RefObject } from "react";
import { Link, NavLink, matchPath, useLocation } from "react-router-dom";
import type { NavItem, PassageModule } from "../modules/registry";
import { copy, shortcutHint } from "./copy";
import { useFocusTrap } from "./hooks/useFocusTrap";
import styles from "./Shell.module.css";
import type { ShellNavItem } from "./shellNav";
import { Button, Icon } from "./ui";
import { workspaceIcon } from "./workspaceIcons";

export interface SidebarProps {
  modules: PassageModule[];
  shellNav: ShellNavItem[];
  drawerOpen: boolean;
  searchRef: RefObject<HTMLButtonElement>;
  onOpenSearch(): void;
  onCloseDrawer(): void;
}

function containsPath(items: NavItem[], pathname: string): boolean {
  return items.some((n) => matchPath({ path: n.to, end: true }, pathname) !== null);
}

export function Sidebar({ modules, shellNav, drawerOpen, searchRef, onOpenSearch, onCloseDrawer }: SidebarProps) {
  const { pathname } = useLocation();
  const asideRef = useRef<HTMLElement>(null);
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});

  useFocusTrap(asideRef, drawerOpen);

  useEffect(() => {
    const owner = modules.find((m) => containsPath(m.nav, pathname));
    if (owner) {
      setCollapsed((current) => (current[owner.id] ? { ...current, [owner.id]: false } : current));
    }
  }, [pathname, modules]);

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
          aria-label={copy.buttons.search}
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
        {modules.map((m) => {
          const open = !collapsed[m.id];
          const headerId = `nav-group-${m.id}`;
          const listId = `nav-list-${m.id}`;
          return (
            <div key={m.id} className={styles.group}>
              <button
                type="button"
                id={headerId}
                className={styles.groupHeader}
                aria-expanded={open}
                aria-controls={listId}
                onClick={() => setCollapsed((current) => ({ ...current, [m.id]: open }))}
              >
                <Icon name={workspaceIcon(m.id)} />
                <span className={styles.groupTitle}>{m.title}</span>
                <span className={styles.chevron}>
                  <Icon name="chevronRight" />
                </span>
              </button>
              <ul id={listId} className={styles.list} aria-labelledby={headerId} hidden={!open}>
                {m.nav.map((n) => (
                  <li key={n.to}>
                    <NavLink end to={n.to} className={`${styles.item} ${styles.groupItem}`} onClick={onCloseDrawer}>
                      {n.label}
                    </NavLink>
                  </li>
                ))}
              </ul>
            </div>
          );
        })}
      </nav>
    </aside>
  );
}
