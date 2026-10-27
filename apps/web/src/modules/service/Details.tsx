import type { ReactNode } from "react";
import styles from "./pages.module.css";

export interface DetailsProps {
  label: string;
  items: [term: string, value: ReactNode, mono?: boolean][];
}

/** A labelled description list of one record's fields. */
export function Details({ label, items }: DetailsProps) {
  return (
    <dl className={styles.details} aria-label={label}>
      {items.map(([term, value, mono]) => (
        <div key={term} className={styles.entry}>
          <dt className={styles.term}>{term}</dt>
          <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
        </div>
      ))}
    </dl>
  );
}
