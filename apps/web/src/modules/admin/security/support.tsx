import { useEffect, type ReactNode, type RefObject } from "react";
import { Alert } from "../../../shell/ui";
import {
  ASSIGNMENT_ENDED,
  ASSIGNMENT_OVERLAP,
  LEGAL_ENTITY_EXISTS,
  LEGAL_ENTITY_INACTIVE,
  SELF_GRANT,
  SecurityApiError,
} from "./api";

export type Errors = Record<string, string>;

export function withoutFields(errors: Errors, fields: readonly string[]): Errors {
  return Object.fromEntries(Object.entries(errors).filter(([field]) => !fields.includes(field)));
}

export function useFocusFirstInvalid(form: RefObject<HTMLFormElement | null>, errors: Errors) {
  useEffect(() => {
    if (Object.keys(errors).length === 0) return;
    form.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
  }, [errors, form]);
}

export function mapServerFields(error: unknown, known: readonly string[]): Errors {
  if (!(error instanceof SecurityApiError)) return {};
  return Object.fromEntries(Object.entries(error.fields).filter(([field]) => known.includes(field)));
}

export function fieldsOwnFailure(error: unknown, known: readonly string[]): boolean {
  if (!(error instanceof SecurityApiError)) return false;
  const names = Object.keys(error.fields);
  return names.length > 0 && names.every((field) => known.includes(field));
}

export function toIsoFromLocal(value: string): string | undefined {
  if (value === "") return undefined;
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? undefined : parsed.toISOString();
}

interface Notice {
  tone: "warning" | "danger";
  title: string;
  message: string;
}

function noticeFor(error: SecurityApiError, title: string, duty: string, scopeNote?: string): Notice {
  if (error.status === 403 && error.code === SELF_GRANT) {
    return {
      tone: "danger",
      title: "You cannot grant a role to yourself",
      message: "Separation of duties blocks self-assignment. Ask another security administrator to make this assignment.",
    };
  }
  if (error.status === 403) {
    return {
      tone: "danger",
      title: "You do not have permission",
      message: scopeNote ?? `This needs the "${duty}" duty. Ask an administrator to grant it, then try again.`,
    };
  }
  if (error.status === 409 && error.code === ASSIGNMENT_OVERLAP) {
    return {
      tone: "warning",
      title: "This overlaps an existing assignment",
      message: "The user already holds this role for the same legal entity during part of this period. End the existing assignment or choose different dates.",
    };
  }
  if (error.status === 409 && error.code === ASSIGNMENT_ENDED) {
    return { tone: "warning", title: "This assignment has already ended", message: "Reload the list to see its current end date." };
  }
  if (error.status === 409 && error.code === LEGAL_ENTITY_EXISTS) {
    return { tone: "warning", title: "That code is already in use", message: "Choose a different legal entity code." };
  }
  if (error.status === 422 && error.code === LEGAL_ENTITY_INACTIVE) {
    return { tone: "warning", title: "That legal entity is inactive", message: "Choose an active legal entity, or assign the role tenant-wide." };
  }
  return { tone: "danger", title, message: error.message };
}

interface ApiFailureProps {
  error: unknown;
  title: string;
  duty: string;
  scopeNote?: string;
  action?: ReactNode;
}

export function ApiFailure({ error, title, duty, scopeNote, action }: ApiFailureProps) {
  const notice =
    error instanceof SecurityApiError
      ? noticeFor(error, title, duty, scopeNote)
      : { tone: "danger" as const, title, message: error instanceof Error ? error.message : "Something went wrong." };
  return (
    <Alert tone={notice.tone} title={notice.title} live="alert" action={action}>
      {notice.message}
    </Alert>
  );
}
