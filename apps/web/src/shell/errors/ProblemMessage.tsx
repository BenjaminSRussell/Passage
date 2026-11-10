import type { ReactNode } from "react";
import styles from "./ProblemMessage.module.css";
import { describeProblemForUser } from "./problem";

export interface ProblemMessageProps {
  /** Any failure: an API error, a `{ message, code }` pair, a problem body or a string. */
  problem: unknown;
  /** Shown when the failure carries no catalog entry and no server text. */
  fallback?: string;
  /**
   * Module wording for this failure. It replaces the catalog and server text;
   * the code, server detail and request id still go under "Details".
   */
  message?: string;
  /** Text shown before the message, for example the action that failed. */
  lead?: ReactNode;
}

/**
 * The shared way to show a failure.
 *
 * A known problem code shows its catalog message and what to do. An unknown
 * one falls back to the server's detail or title. The code, the server
 * detail and the request id sit under a "Details" disclosure for support.
 */
export function ProblemMessage({ problem, fallback, message, lead }: ProblemMessageProps) {
  const view = describeProblemForUser(problem, fallback);
  const { problem: p, entry } = view;
  const summary = message ?? view.summary;
  const serverText = p.detail ?? p.title;
  const showServerText = serverText !== undefined && !summary.includes(serverText);
  const hasDetails = Boolean(p.code) || Boolean(p.requestId) || showServerText;
  return (
    <div className={styles.problem} data-problem-code={p.code} data-problem-known={entry ? "true" : "false"}>
      <span className={styles.summary}>
        {lead}
        {summary}
      </span>
      {hasDetails ? (
        <details className={styles.details}>
          <summary className={styles.toggle}>Details</summary>
          <dl className={styles.list}>
            {p.code ? (
              <div className={styles.row}>
                <dt>Code</dt>
                <dd>
                  <code>{p.code}</code>
                </dd>
              </div>
            ) : null}
            {showServerText ? (
              <div className={styles.row}>
                <dt>Message from the service</dt>
                <dd>{serverText}</dd>
              </div>
            ) : null}
            {p.requestId ? (
              <div className={styles.row}>
                <dt>Request id</dt>
                <dd>
                  <code>{p.requestId}</code>
                </dd>
              </div>
            ) : null}
          </dl>
        </details>
      ) : null}
    </div>
  );
}
