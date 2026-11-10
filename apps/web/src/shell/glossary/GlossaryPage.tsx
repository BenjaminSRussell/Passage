import { PageHeader } from "../ui";
import styles from "./glossary.module.css";
import { glossary } from "./terms";

export function GlossaryPage() {
  const sorted = [...glossary].sort((a, b) => a.term.localeCompare(b.term));
  return (
    <>
      <PageHeader title="Glossary" description="What the business words in Passage mean. Words with a dotted underline show their meaning when you point at them or tab to them." />
      <dl className={styles.list}>
        {sorted.map((t) => (
          <div key={t.id} id={t.id}>
            <dt>{t.term}</dt>
            <dd>{t.meaning}</dd>
          </div>
        ))}
      </dl>
    </>
  );
}
