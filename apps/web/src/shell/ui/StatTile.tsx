import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { cx } from "./cx";
import styles from "./StatTile.module.css";

export interface StatGroupProps {
  label: string;
  children?: ReactNode;
}

export function StatGroup({ label, children }: StatGroupProps) {
  return (
    <ul className={styles.group} aria-label={label}>
      {children}
    </ul>
  );
}

export type StatTone = "neutral" | "danger" | "warning";

export interface StatTileProps {
  label: string;
  value: ReactNode;
  tone?: StatTone;
  hint?: string;
  /** Where the number comes from: the label becomes a link to that list. */
  to?: string;
}

export function StatTile({ label, value, tone = "neutral", hint, to }: StatTileProps) {
  // "100+" is a count too, so read the leading number.
  const flagged = tone !== "neutral" && Number.parseFloat(String(value)) > 0;
  return (
    <li className={styles.tile}>
      <span className={styles.label}>
        {flagged ? <span className={cx(styles.dot, styles[tone])} aria-hidden="true" /> : null}
        {to ? (
          <Link to={to} className={styles.link}>
            {label}
          </Link>
        ) : (
          label
        )}
      </span>{" "}
      <span className={cx(styles.value, flagged && styles[tone])}>{value}</span>
      {hint ? <span className={styles.hint}>{hint}</span> : null}
    </li>
  );
}
