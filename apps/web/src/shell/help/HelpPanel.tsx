import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { Link, useLocation } from "react-router-dom";
import { copy } from "../copy";
import { useFocusTrap } from "../hooks/useFocusTrap";
import styles from "./HelpPanel.module.css";
import { helpForRoute, type RouteHelp } from "./routeHelp";
import { isOptedOut, setOptOut } from "../uxEvents";

/** Where the end-user guides are read. A docs site can replace this at build time. */
const GUIDE_BASE = import.meta.env.VITE_USER_GUIDE_BASE ?? "https://github.com/BenjaminSRussell/Passage/blob/main/docs/user/";

export const guideUrl = (guide: RouteHelp["guide"]): string => {
  const [file = "", anchor = ""] = guide.split("#");
  return `${GUIDE_BASE}${file}${anchor ? `#${anchor}` : ""}`;
};

export interface HelpPanelProps {
  pathname: string;
  onClose(): void;
}

/** The side panel: the page's purpose, its common tasks and a link to the guide section. */
export function HelpPanel({ pathname, onClose }: HelpPanelProps) {
  const panel = useRef<HTMLElement>(null);
  const titleId = useId();
  const help = helpForRoute(pathname);
  useFocusTrap(panel, true);

  const onKeyDown = (event: KeyboardEvent) => {
    if (event.key === "Escape") {
      event.stopPropagation();
      onClose();
    }
  };

  return (
    <div className={styles.overlay} onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <aside ref={panel} role="dialog" aria-modal="true" aria-labelledby={titleId} className={styles.panel} onKeyDown={onKeyDown}>
        <header className={styles.head}>
          <h2 id={titleId} className={styles.title}>
            {help ? help.title : copy.help.panelLabel}
          </h2>
          <button type="button" className={styles.close} aria-label={copy.help.close} onClick={onClose}>
            ×
          </button>
        </header>
        {help ? (
          <div className={styles.body}>
            <h3 className={styles.heading}>{copy.help.purposeHeading}</h3>
            <p className={styles.text}>{help.purpose}</p>
            <h3 className={styles.heading}>{copy.help.tasksHeading}</h3>
            <ul className={styles.tasks}>
              {help.howTo.map((task) => (
                <li key={task}>{task}</li>
              ))}
            </ul>
            <a className={styles.link} href={guideUrl(help.guide)} target="_blank" rel="noreferrer">
              {copy.help.guideLink}
              <span className="visually-hidden"> {copy.help.opensInNewTab}</span>
            </a>
          </div>
        ) : (
          <div className={styles.body}>
            <p className={styles.text}>{copy.help.noEntry}</p>
          </div>
        )}
        <footer className={styles.foot}>
          <UsageDataChoice />
          <Link to="/whats-new" onClick={onClose}>
            {copy.help.whatsNew}
          </Link>
        </footer>
      </aside>
    </div>
  );
}

/** The opt-out for anonymous usage data. It is on by default for people who hold the record duty. */
function UsageDataChoice() {
  const [share, setShare] = useState(() => !isOptedOut());
  const id = useId();
  return (
    <div className={styles.choice}>
      <input
        id={id}
        type="checkbox"
        checked={share}
        onChange={(event) => {
          setShare(event.target.checked);
          setOptOut(!event.target.checked);
        }}
      />
      <label htmlFor={id}>{copy.help.shareUsage}</label>
      <p className={styles.choiceHint}>{copy.help.shareUsageHint}</p>
    </div>
  );
}

/** The "?" button for the top bar. It owns the open state and gives focus back to itself on close. */
export function HelpButton({ announce }: { announce?(message: string): void }) {
  const { pathname } = useLocation();
  const [open, setOpen] = useState(false);
  const button = useRef<HTMLButtonElement>(null);
  const wasOpen = useRef(false);

  useEffect(() => {
    if (wasOpen.current && !open) button.current?.focus();
    wasOpen.current = open;
  }, [open]);
  useEffect(() => setOpen(false), [pathname]);

  return (
    <>
      <button
        ref={button}
        type="button"
        className={styles.trigger}
        aria-label={copy.help.button}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => {
          setOpen(true);
          announce?.(copy.help.announceOpened);
        }}
      >
        <span aria-hidden="true">?</span>
      </button>
      {open ? <HelpPanel pathname={pathname} onClose={() => setOpen(false)} /> : null}
    </>
  );
}
