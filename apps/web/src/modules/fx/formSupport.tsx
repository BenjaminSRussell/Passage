import { useEffect, type ReactNode, type RefObject } from "react";
import { Alert } from "../../shell/ui";
import { FxApiError } from "./api/client";
import type { Errors } from "./validate";

const editedErrors = new WeakSet<Errors>();

export function withoutFields(errors: Errors, fields: readonly string[]): Errors {
  const remaining = Object.fromEntries(Object.entries(errors).filter(([field]) => !fields.includes(field)));
  editedErrors.add(remaining);
  return remaining;
}

export function useFocusFirstInvalid(form: RefObject<HTMLFormElement | null>, errors: Errors) {
  useEffect(() => {
    if (Object.keys(errors).length === 0 || editedErrors.has(errors)) return;
    form.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
  }, [errors, form]);
}

function fieldName(field: string, prefix: string): string {
  return field.startsWith(prefix) ? field.slice(prefix.length) : field;
}

export function mapServerFields(error: unknown, prefix: string, known: readonly string[]): Errors {
  if (!(error instanceof FxApiError)) return {};
  const mapped: Errors = {};
  for (const [field, message] of Object.entries(error.fields)) {
    const name = fieldName(field, prefix);
    if (known.includes(name)) mapped[name] = message;
  }
  return mapped;
}

export function fieldsOwnFailure(error: unknown, prefix: string, known: readonly string[]): boolean {
  if (!(error instanceof FxApiError)) return false;
  const names = Object.keys(error.fields);
  return names.length > 0 && names.every((field) => known.includes(fieldName(field, prefix)));
}

interface ApiFailureProps {
  error: unknown;
  title: string;
  duty: string;
  action?: ReactNode;
}

function conflictBody(error: FxApiError) {
  if (error.code === "FX_IDEMPOTENCY_IN_PROGRESS") {
    const wait = error.retryAfter ? `Wait ${error.retryAfter} second${error.retryAfter === 1 ? "" : "s"}` : "Wait a moment";
    return { title: "This rate is still being saved", message: `${wait} and save again. Nothing is saved twice.` };
  }
  return {
    title: "Someone else saved this rate first",
    message: "Another change for this pair and date was stored at the same time. Check the rate history for the current value, then save again if it is still needed.",
  };
}

export function ApiFailure({ error, title, duty, action }: ApiFailureProps) {
  if (error instanceof FxApiError && error.status === 403) {
    return (
      <Alert tone="danger" title="You do not have permission" live="alert" action={action}>
        {`This needs the "${duty}" duty. Ask an administrator to grant it, then try again.`}
      </Alert>
    );
  }
  if (error instanceof FxApiError && error.status === 409) {
    const body = conflictBody(error);
    return (
      <Alert tone="warning" title={body.title} live="alert">
        {body.message}
      </Alert>
    );
  }
  return (
    <Alert tone="danger" title={title} live="alert" action={action}>
      {error instanceof Error ? error.message : "Something went wrong."}
    </Alert>
  );
}
