import type { ReactNode } from "react";
import { copy } from "../copy";
import { Icon } from "./Icon";
import styles from "./FormField.module.css";

export interface FormFieldControlProps {
  id: string;
  "aria-describedby"?: string;
  "aria-invalid"?: true;
  "aria-required"?: true;
}

export interface FormFieldProps {
  id: string;
  label: string;
  hint?: string;
  error?: string;
  optional?: boolean;
  required?: boolean;
  children: (control: FormFieldControlProps) => ReactNode;
}

export function FormField({ id, label, hint, error, optional = false, required = false, children }: FormFieldProps) {
  const hintId = `${id}-hint`;
  const errorId = `${id}-error`;
  const describedBy = [hint ? hintId : null, error ? errorId : null].filter(Boolean).join(" ");
  return (
    <div className={styles.field}>
      <div className={styles.labelRow}>
        <label htmlFor={id} className={styles.label}>
          {label}
        </label>
        {optional ? <span className={styles.optional}>{copy.forms.optional}</span> : null}
        {required && !optional ? <span className={styles.optional}>{copy.forms.required}</span> : null}
      </div>
      {children({ id, "aria-describedby": describedBy || undefined, "aria-invalid": error ? true : undefined, "aria-required": required && !optional ? true : undefined })}
      {hint ? (
        <p id={hintId} className={styles.hint}>
          {hint}
        </p>
      ) : null}
      {error ? (
        <p id={errorId} role="alert" className={styles.error}>
          <Icon name="xCircle" />
          <span>{error}</span>
        </p>
      ) : null}
    </div>
  );
}
