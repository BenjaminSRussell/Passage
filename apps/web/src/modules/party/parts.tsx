import type { ReactNode } from "react";
import { Alert, Button } from "../../shell/ui";
import type { Problem } from "./api/client";
import styles from "./pages.module.css";
import { lookupProblem, ProblemMessage } from "../../shell/errors";

const KNOWN: Record<string, string> = {
  "PTY-PARTY-NOT-FOUND": "No party exists with that id in this tenant.",
  "PTY-CHANGE-NOT-FOUND": "No credit-limit change exists with that id in this tenant or legal entity.",
  "PTY-PENDING-SELF-APPROVAL": "You cannot approve or reject a change you requested.",
  "PTY-CHANGE-DECIDED": "This change was already decided, or someone else decided it.",
  "PTY-PENDING-STALE": "The stored credit limit changed after this request. The request can only be rejected.",
  "PTY-REASON-TOO-LONG": "The decision reason must be at most 500 characters.",
  "PTY-LEGAL-ENTITY-REQUIRED": "Writing a party or role needs exactly one legal entity in your session.",
  "PTY-VERSION-CONFLICT": "Someone changed this party after you opened it. Reload and try again.",
  "PTY-PARTY-MERGED": "This party was merged into another one and cannot take new roles.",
  "PTY-VENDOR-EXISTS": "This party already has a vendor role here, or the account number is taken.",
  "PTY-CUSTOMER-EXISTS": "This party already has a customer role here, or the account number is taken.",
  "PTY-CHANGE-PENDING": "A credit limit change is already awaiting approval for this customer.",
  "PTY-CUSTOMER-NOT-FOUND": "No customer role exists with that id in your legal entity.",
  "PTY-0001": "The tax registration failed format validation.",
  "AUTH-0003": "You don't have access to do this.",
};

/** Plain wording for a problem; `ProblemMessage` keeps its code under Details. */
export function problemText(problem: Problem): string {
  const known = KNOWN[problem.code];
  if (known) return known;
  const entry = lookupProblem(problem.code);
  if (entry) return `${entry.message} ${entry.action}`;
  const lead =
    problem.status === 403
      ? "You don't have access to do this. "
      : problem.status === 404
        ? "Not found. "
        : problem.status === 409
          ? "This conflicts with the current state of the record; reload and try again. "
          : "";
  return `${lead}${problem.message}`;
}

export function ProblemAlert({ problem, title, retry }: { problem: Problem; title: string; retry?: () => void }) {
  return (
    <Alert
      tone="danger"
      title={title}
      live="alert"
      action={
        retry ? (
          <Button size="sm" onClick={retry}>
            Try again
          </Button>
        ) : undefined
      }
    >
      <ProblemMessage problem={problem} message={problemText(problem)} />
    </Alert>
  );
}

export function Entry({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}
