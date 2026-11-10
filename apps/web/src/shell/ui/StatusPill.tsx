import type { ReactNode } from "react";
import styles from "./StatusPill.module.css";
import { cx } from "./cx";
import { standardLabel, statusTone } from "./status";

export type BadgeTone = "neutral" | "success" | "warning" | "danger" | "info";

export interface BadgeProps {
  tone: BadgeTone;
  children?: ReactNode;
}

export function Badge({ tone, children }: BadgeProps) {
  return (
    <span className={cx(styles.badge, styles[tone])}>
      <span className={styles.dot} aria-hidden="true" />
      {children}
    </span>
  );
}

export interface StatusPillProps {
  status: string;
}

export function StatusPill({ status }: StatusPillProps) {
  return <Badge tone={statusTone(status)}>{standardLabel(status)}</Badge>;
}
