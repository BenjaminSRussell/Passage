import type { ReactNode } from "react";
import styles from "./pages.module.css";

export interface DetailEntry {
  term: string;
  value: ReactNode;
  mono?: boolean;
}

/** A definition list of a record's header fields, as on the freight bill detail page. */
export function Details({ entries }: { entries: DetailEntry[] }) {
  return (
    <dl className={styles.details}>
      {entries.map(({ term, value, mono }) => (
        <div key={term} className={styles.entry}>
          <dt className={styles.term}>{term}</dt>
          <dd className={`${styles.value} ${mono ? styles.mono : ""}`}>{value}</dd>
        </div>
      ))}
    </dl>
  );
}
