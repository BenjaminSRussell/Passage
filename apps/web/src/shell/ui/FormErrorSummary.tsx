import { useEffect, useRef } from "react";
import { copy } from "../copy";
import styles from "./FormErrorSummary.module.css";

export interface FormErrorItem {
  fieldId?: string;
  label: string;
  message: string;
}

export interface FormErrorSummaryProps {
  items: FormErrorItem[];
  attempt: number;
}

export function focusField(fieldId: string): boolean {
  const field = document.getElementById(fieldId);
  if (!field) {
    return false;
  }
  for (let section = field.closest("details"); section; section = section.parentElement?.closest("details") ?? null) {
    section.open = true;
  }
  field.focus();
  return true;
}

export function FormErrorSummary({ items, attempt }: FormErrorSummaryProps) {
  const first = items[0]?.fieldId;
  const focused = useRef(0);
  useEffect(() => {
    if (attempt > focused.current && first) {
      focused.current = attempt;
      focusField(first);
    }
  }, [attempt, first]);

  return (
    <div className={styles.summary} role="group" aria-label={copy.forms.errorSummaryLabel} hidden={items.length === 0}>
      <p className={styles.title}>{copy.forms.errorSummaryTitle(items.length)}</p>
      <ul className={styles.list}>
        {items.map((item) => (
          <li key={`${item.fieldId ?? item.label}-${item.message}`}>
            {item.fieldId ? (
              <button type="button" className={styles.link} onClick={() => focusField(item.fieldId!)}>
                {item.label}: {item.message}
              </button>
            ) : (
              <span>
                {item.label}: {item.message}
              </span>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
