import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent, type ReactNode } from "react";
import { Alert, Button } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import styles from "./pages.module.css";

export type EditErrors<K extends string> = Partial<Record<K, string>>;

export interface EditFieldArgs<V, K extends string> {
  values: V;
  errors: EditErrors<K>;
  text(key: keyof V & string): { value: string; onChange(event: { target: { value: string } }): void };
}

interface EditActionProps<V, K extends string> {
  subject: string;
  // initial is the row as the table shows it now; it changes when the row is re-read after a version conflict.
  initial: V;
  validate(values: V): EditErrors<K>;
  // save sends only the fields the user changed; it resolves once the table row is refreshed and throws the
  // service's problem otherwise.
  save(change: Partial<V>): Promise<void>;
  // Service codes that belong to one field, shown on that field instead of in the form alert.
  fieldCodes?: Record<string, { field: K; message: string }>;
  fields(args: EditFieldArgs<V, K>): ReactNode;
}

const VERSION_CONFLICT = "ORG-VERSION-CONFLICT";

// changedFields lists the fields whose value differs from the row the form was opened (or last refreshed) from.
function changedFields<V>(values: V, base: V): Partial<V> {
  const change: Partial<V> = {};
  for (const key of Object.keys(values as object) as (keyof V)[]) {
    if (values[key] !== base[key]) {
      change[key] = values[key];
    }
  }
  return change;
}

// EditAction is the row's Edit button; it opens the edit form in place with Save and Cancel (Escape also cancels),
// keeps focus where the user was, and shows field errors on the field and any other failure in an alert. Only the
// fields the user changed are sent, so a save never overwrites what someone else changed in the other fields. On
// ORG-VERSION-CONFLICT the page re-reads the row: the fields the user left alone take the fresh values, the user's
// own edits are kept, and the alert says so; saving again sends them with the new version.
export function EditAction<V, K extends string>({ subject, initial, validate, save, fieldCodes, fields }: EditActionProps<V, K>) {
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<V>(initial);
  const [base, setBase] = useState<V>(initial);
  const [errors, setErrors] = useState<EditErrors<K>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const opener = useRef<HTMLButtonElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  const opened = useRef(false);
  const fresh = JSON.stringify(initial);

  useEffect(() => {
    if (open) {
      opened.current = true;
      formRef.current?.querySelector<HTMLElement>("input, select")?.focus();
    } else if (opened.current) {
      opened.current = false;
      opener.current?.focus();
    }
  }, [open]);

  // A re-read row (after a version conflict) refreshes the fields the user has not touched. Only a new row value
  // triggers it; opening the form sets the base itself.
  useEffect(() => {
    if (!open || JSON.stringify(base) === fresh) {
      return;
    }
    const next = JSON.parse(fresh) as V;
    setValues((current) => ({ ...next, ...changedFields(current, base) }));
    setBase(next);
  }, [fresh]);

  const begin = () => {
    setValues(initial);
    setBase(initial);
    setErrors({});
    setFailure(null);
    setOpen(true);
  };

  const cancel = () => {
    if (!busy) {
      setOpen(false);
    }
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validate(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) {
      return;
    }
    const change = changedFields(values, base);
    if (Object.keys(change).length === 0) {
      setOpen(false);
      return;
    }
    setBusy(true);
    try {
      await save(change);
      setOpen(false);
    } catch (error) {
      const problem = describeProblem(error);
      const mapped = fieldCodes?.[problem.code];
      if (mapped) {
        setErrors({ [mapped.field]: mapped.message } as EditErrors<K>);
      } else {
        setFailure(problem);
      }
    } finally {
      setBusy(false);
    }
  };

  const onKeyDown = (event: KeyboardEvent<HTMLFormElement>) => {
    if (event.key === "Escape") {
      event.preventDefault();
      cancel();
    }
  };

  if (!open) {
    return (
      <Button ref={opener} size="sm" aria-label={`Edit ${subject}`} onClick={begin}>
        Edit
      </Button>
    );
  }
  const text = (key: keyof V & string) => ({
    value: String(values[key] ?? ""),
    onChange: (event: { target: { value: string } }) => setValues({ ...values, [key]: event.target.value }),
  });
  return (
    <form ref={formRef} onSubmit={submit} onKeyDown={onKeyDown} noValidate className={styles.form} aria-label={`Edit ${subject}`}>
      {fields({ values, errors, text })}
      {failure ? (
        <Alert tone="danger" title={`Could not save ${subject}`} live="alert">
          {failure.code === VERSION_CONFLICT
            ? "Someone else changed this row; it has been reloaded and your changes are kept. Check them and save again. "
            : null}
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <div className={styles.rowActions}>
        <Button type="submit" size="sm" variant="primary" loading={busy} aria-label={`Save ${subject}`}>
          Save
        </Button>
        <Button size="sm" variant="ghost" aria-label={`Cancel editing ${subject}`} onClick={cancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
