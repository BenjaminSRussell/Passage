import type { ReactNode } from "react";
import { cx } from "./cx";
import styles from "./EmptyState.module.css";
import { Icon, type IconName } from "./Icon";

export interface EmptyStateProps {
  icon?: IconName;
  title: string;
  description?: string;
  action?: ReactNode;
  headingLevel?: 2 | 3;
  compact?: boolean;
}

export function EmptyState({ icon, title, description, action, headingLevel = 3, compact = false }: EmptyStateProps) {
  const Heading = headingLevel === 2 ? "h2" : "h3";
  return (
    <div className={cx(styles.empty, compact && styles.compact)}>
      {icon && !compact ? (
        <span className={styles.tile}>
          <Icon name={icon} size={20} />
        </span>
      ) : null}
      <Heading className={styles.title}>{title}</Heading>
      {description ? <p className={styles.description}>{description}</p> : null}
      {action ? <div className={styles.action}>{action}</div> : null}
    </div>
  );
}
