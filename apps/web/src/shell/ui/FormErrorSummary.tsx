import { useEffect, useRef } from "react";
import { copy } from "../copy";
import styles from "./FormErrorSummary.module.css";

export interface FormErrorItem {
  fieldId?: string;
  label: string;
  message: string;
}

export interface FormFieldTarget {
  id?: string;
  label: string;
}

export function summaryItems(
  errors: Record<string, string>,
  fields: Record<string, FormFieldTarget>,
  dynamic?: (key: string) => FormFieldTarget | undefined,
): FormErrorItem[] {
  const declared = Object.keys(fields)
    .filter((key) => errors[key])
    .map((key) => ({ fieldId: fields[key].id, label: fields[key].label, message: errors[key] }));
  const computed = Object.keys(errors)
    .filter((key) => !(key in fields))
    .flatMap((key) => {
      const target = dynamic?.(key);
      return target ? [{ fieldId: target.id, label: target.label, message: errors[key] }] : [];
    });
  return [...declared, ...computed];
}

export interface FormErrorSummaryProps {
  items: FormErrorItem[];
  attempt: number;
}

/**
 * The `attempt` counter for a form whose field errors are set when it is submitted, client side or from the service:
 * each new errors object that holds a message counts as one more failed submit. Use `setAttempt` by hand instead when
 * a form also sets errors while the person types, or focus would jump to the summary mid-edit.
 */
export function useSubmitAttempt(errors: Record<string, string | undefined>): number {
  const last = useRef(errors);
  const count = useRef(0);
  if (errors !== last.current) {
    last.current = errors;
    if (Object.values(errors).some(Boolean)) count.current += 1;
  }
  return count.current;
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
  const first = items.find((item) => item.fieldId)?.fieldId;
  const has = items.length > 0;
  const focused = useRef(0);
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (attempt > focused.current && has) {
      focused.current = attempt;
      if (!first || !focusField(first)) {
        box.current?.focus();
      }
    }
  }, [attempt, first, has]);

  return (
    <div ref={box} tabIndex={-1} className={styles.summary} role="alert" aria-label={copy.forms.errorSummaryLabel} hidden={!has}>
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
