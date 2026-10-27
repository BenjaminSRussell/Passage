import styles from "./pages.module.css";

export function Detail({ term, children }: { term: string; children: string }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}
