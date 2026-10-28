import { useEffect, type RefObject } from "react";
import { Alert } from "../../shell/ui";
import { FxApiError } from "./api/client";
import type { Errors } from "./validate";

export function useFocusFirstInvalid(form: RefObject<HTMLFormElement | null>, errors: Errors) {
  useEffect(() => {
    if (Object.keys(errors).length === 0) return;
    form.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
  }, [errors, form]);
}

export function mapServerFields(error: unknown, prefix: string, known: readonly string[]): Errors {
  if (!(error instanceof FxApiError)) return {};
  const mapped: Errors = {};
  for (const [field, message] of Object.entries(error.fields)) {
    const name = field.startsWith(prefix) ? field.slice(prefix.length) : field;
    if (known.includes(name)) mapped[name] = message;
  }
  return mapped;
}

interface ApiFailureProps {
  error: unknown;
  title: string;
  duty: string;
}

export function ApiFailure({ error, title, duty }: ApiFailureProps) {
  const status = error instanceof FxApiError ? error.status : 0;
  const message = error instanceof Error ? error.message : "Something went wrong.";
  if (status === 403) {
    return (
      <Alert tone="danger" title="You do not have permission" live="alert">
        {`This needs the "${duty}" duty. Ask an administrator to grant it, then try again.`}
      </Alert>
    );
  }
  if (status === 409) {
    return (
      <Alert tone="warning" title="Another change is still in progress" live="alert">
        Wait a moment and try again. Nothing was saved twice.
      </Alert>
    );
  }
  return (
    <Alert tone="danger" title={title} live="alert">
      {message}
    </Alert>
  );
}
