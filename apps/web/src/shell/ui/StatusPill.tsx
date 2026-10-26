import type { ReactNode } from "react";
import styles from "./StatusPill.module.css";
import { cx } from "./cx";

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

const known: Record<string, { label: string; tone: BadgeTone }> = {
  active: { label: "Active", tone: "success" },
  released: { label: "Released", tone: "success" },
  preferred: { label: "Preferred", tone: "success" },
  ended: { label: "Completed", tone: "success" },
  executing: { label: "Running", tone: "info" },
  running: { label: "Running", tone: "info" },
  pending: { label: "Pending", tone: "neutral" },
  draft: { label: "Draft", tone: "neutral" },
  open: { label: "Open", tone: "neutral" },
  escalated: { label: "Escalated", tone: "warning" },
  overdue: { label: "Overdue", tone: "warning" },
  failed: { label: "Failed", tone: "danger" },
  error: { label: "Failed", tone: "danger" },
};

function sentenceCase(value: string): string {
  const spaced = value.replace(/[_-]+/g, " ").trim();
  return spaced.charAt(0).toUpperCase() + spaced.slice(1).toLowerCase();
}

export function StatusPill({ status }: StatusPillProps) {
  const match = known[status.toLowerCase()] ?? { label: sentenceCase(status), tone: "neutral" as const };
  return <Badge tone={match.tone}>{match.label}</Badge>;
}
