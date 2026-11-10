// Small building blocks shared by the collections pages.
import type { ReactNode } from "react";
import { Alert, Button } from "../../shell/ui";
import { ReceivablesApiError, type Problem } from "./api/client";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

/** One term and value in a details list (`<dl className={styles.details}>`). */
export function Detail({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/** The load-failure alert every page shows, with a retry. */
export function LoadFailure({ title, problem, retry }: { title: string; problem: Problem; retry(): void }) {
  return (
    <Alert tone="danger" title={title} live="alert" action={<Button size="sm" onClick={retry}>Try again</Button>}>
      <ProblemMessage problem={problem} />
    </Alert>
  );
}

/** A refused command, with the service's code. */
export function CommandFailure({ title, problem }: { title: string; problem: Problem }) {
  return (
    <Alert tone="danger" title={title} live="alert">
      <ProblemMessage problem={problem} />
    </Alert>
  );
}

/** A refused request is final, so the next attempt needs a new key; a network or server failure may retry with the same one. */
export function keepKeyAfter(error: unknown): boolean {
  return !(error instanceof ReceivablesApiError) || error.status >= 500;
}

/** 412 means the record changed since the page read it. */
export function isStale(error: unknown): boolean {
  return error instanceof ReceivablesApiError && error.status === 412;
}
