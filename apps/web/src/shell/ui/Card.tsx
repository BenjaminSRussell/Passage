import type { ReactNode } from "react";
import styles from "./Card.module.css";
import { cx } from "./cx";

export interface CardProps {
  title?: string;
  headingLevel?: 2 | 3;
  actions?: ReactNode;
  children?: ReactNode;
  padded?: boolean;
}

export function Card({ title, headingLevel = 3, actions, children, padded = true }: CardProps) {
  const Heading = headingLevel === 2 ? "h2" : "h3";
  return (
    <div className={styles.card}>
      {title || actions ? (
        <div className={styles.head}>
          {title ? (
            <Heading className={styles.title}>
              {title}
            </Heading>
          ) : null}
          {actions ? <div className={styles.actions}>{actions}</div> : null}
        </div>
      ) : null}
      <div className={cx(styles.body, padded && styles.padded)}>{children}</div>
    </div>
  );
}

export interface CardGridProps {
  children?: ReactNode;
  label?: string;
  as?: "ul" | "div";
}

export function CardGrid({ children, label, as = "div" }: CardGridProps) {
  if (as === "ul") {
    return (
      <ul className={styles.grid} aria-label={label}>
        {children}
      </ul>
    );
  }
  return (
    <div className={styles.grid} role={label ? "group" : undefined} aria-label={label}>
      {children}
    </div>
  );
}
