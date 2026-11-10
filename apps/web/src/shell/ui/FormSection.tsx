import { useId, useState, type ReactNode } from "react";
import { copy } from "../copy";
import styles from "./FormSection.module.css";

export interface FormSectionProps {
  title?: string;
  description?: string;
  collapse?: "more" | "advanced";
  defaultOpen?: boolean;
  children: ReactNode;
}

export function FormSection({ title, description, collapse, defaultOpen = false, children }: FormSectionProps) {
  const headingId = useId();
  const [openOnMount] = useState(defaultOpen);
  const body = (
    <>
      {description ? <p className={styles.description}>{description}</p> : null}
      <div className={styles.body}>{children}</div>
    </>
  );
  if (collapse) {
    const summary = title ?? (collapse === "advanced" ? copy.forms.advanced : copy.forms.moreOptions);
    return (
      <details className={styles.section} open={openOnMount}>
        <summary className={styles.summary}>{summary}</summary>
        {body}
      </details>
    );
  }
  return (
    <section className={styles.section} aria-labelledby={title ? headingId : undefined}>
      {title ? (
        <h3 id={headingId} className={styles.title}>
          {title}
        </h3>
      ) : null}
      {body}
    </section>
  );
}
