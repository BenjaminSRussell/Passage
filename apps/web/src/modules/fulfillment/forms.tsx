// Shared pieces of the fulfillment command forms: the problem alert, the
// conflict explanation and one Idempotency-Key per form submit.
import { useRef } from "react";
import { Alert } from "../../shell/ui";
import { newKey, type Problem } from "./api/client";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export function ProblemAlert({ title, problem }: { title: string; problem: Problem | null }) {
  if (!problem) return null;
  return (
    <div className={styles.formActions}>
      <Alert tone="danger" title={title} live="alert">
        <ProblemMessage problem={problem} />
      </Alert>
    </div>
  );
}

/** Messages for the codes a stale or conflicting command gets, so the user knows to reload. */
export function explain(problem: Problem): Problem {
  if (problem.code === "conflict_version") return { ...problem, message: "Someone else changed this shipment. Reload it and try again." };
  return problem;
}

/** One Idempotency-Key per form: renew it when the values change or the command succeeds. */
export function useCommandKey() {
  const key = useRef(newKey());
  return { current: () => key.current, renew: () => (key.current = newKey()) };
}
