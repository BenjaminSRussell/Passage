import styles from "./pages.module.css";

export function Detail({ term, title, children }: { term: string; title?: string; children: string }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value} title={title}>
        {children}
      </dd>
    </div>
  );
}
