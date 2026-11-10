import type { RefObject } from "react";
import { Link } from "react-router-dom";
import { UserMenu } from "./auth/account";
import { copy } from "./copy";
import type { PageMeta } from "./pageMeta";
import { ContextPicker } from "./lookup/workContext";
import styles from "./Shell.module.css";
import { Button, Icon } from "./ui";

export interface TopBarProps {
  page: PageMeta;
  titleRef: RefObject<HTMLHeadingElement | null>;
  menuRef: RefObject<HTMLButtonElement | null>;
  drawerOpen: boolean;
  compact: boolean;
  showContext?: boolean;
  onOpenDrawer(): void;
  onToggleCompact(): void;
}

export function TopBar({ page, titleRef, menuRef, drawerOpen, compact, showContext = false, onOpenDrawer, onToggleCompact }: TopBarProps) {
  const workspace = page.workspace && page.workspace.title !== page.title ? page.workspace : undefined;
  return (
    <div className={styles.topbar}>
      <button
        ref={menuRef}
        type="button"
        className={styles.menuButton}
        aria-label={copy.buttons.openNavigation}
        aria-expanded={drawerOpen}
        aria-controls="sidebar"
        onClick={onOpenDrawer}
      >
        <Icon name="menu" size={20} />
      </button>
      {workspace ? (
        <>
          <nav aria-label="Breadcrumb" className={styles.breadcrumb}>
            <ol>
              <li>
                <Link to={workspace.to}>{workspace.title}</Link>
              </li>
            </ol>
          </nav>
          <span className={styles.crumbSeparator} aria-hidden="true">
            <Icon name="chevronRight" />
          </span>
        </>
      ) : null}
      <h1 ref={titleRef} tabIndex={-1} className={styles.title}>
        {page.title}
      </h1>
      <div className={styles.topbarActions}>
        {showContext ? <ContextPicker /> : null}
        <Button variant="ghost" size="sm" aria-pressed={compact} className={styles.compactToggle} onClick={onToggleCompact}>
          {copy.buttons.compactView}
        </Button>
        <UserMenu />
      </div>
    </div>
  );
}
