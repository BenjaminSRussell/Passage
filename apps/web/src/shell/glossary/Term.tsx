import { useId, useState, type ReactNode } from "react";
import styles from "./glossary.module.css";
import { glossaryById } from "./terms";

/**
 * A domain word with its meaning in a tooltip. The meaning shows on hover and
 * keyboard focus, hides on Escape, and is linked with aria-describedby.
 */
export function Term({ id, children }: { id: string; children?: ReactNode }) {
  const entry = glossaryById.get(id);
  const tipId = useId();
  const [dismissed, setDismissed] = useState(false);
  if (!entry) {
    return <>{children}</>;
  }
  return (
    <span
      className={styles.term}
      data-dismissed={dismissed ? "true" : undefined}
      onMouseLeave={() => setDismissed(false)}
      onBlur={() => setDismissed(false)}
      onKeyDown={(e) => {
        if (e.key === "Escape" && !dismissed) {
          e.stopPropagation();
          setDismissed(true);
        }
      }}
    >
      <span className={styles.word} tabIndex={0} aria-describedby={tipId}>
        {children ?? entry.term}
      </span>
      <span id={tipId} role="tooltip" className={styles.tip}>
        {entry.meaning}
      </span>
    </span>
  );
}
