import { Link } from "react-router-dom";
import { buttonClassName } from "../ui";
import styles from "./DocumentFlow.module.css";

/** How many documents wait at a step; the guided page fills it from the services' own lists. */
export interface StageCount {
  /** "12 purchase orders awaiting receipt" is `${value} ${noun}`. */
  value?: string;
  noun: string;
  loading?: boolean;
  /** The user may not read this list, or the call failed. */
  unavailable?: boolean;
}

export interface GuidedStep {
  key: string;
  title: string;
  description: string;
  to: string;
  action: string;
  /** The link to the full screen for exceptions, when the guided step is the standard case only. */
  fullScreen?: { to: string; label: string };
  count?: StageCount;
  /** The user lacks the duty for this step: the step is shown but has no link. */
  locked?: boolean;
}

function countText(c: StageCount): string {
  if (c.loading) return "Counting…";
  if (c.unavailable || c.value === undefined) return "Count not available";
  return `${c.value} ${c.noun}`;
}

/**
 * The guided process page: a stepper with what each step does, how many documents wait there and the
 * link that does it. Shared by procure-to-pay and order-to-cash.
 */
export function GuidedFlow({ label, steps }: { label: string; steps: GuidedStep[] }) {
  return (
    <ol className={styles.guided} aria-label={label}>
      {steps.map((step, i) => (
        <li key={step.key} className={styles.guidedStep}>
          <span className={styles.guidedNumber} aria-hidden="true">
            {i + 1}
          </span>
          <div className={styles.guidedBody}>
            <strong>{step.title}</strong>
            <div className={styles.note}>{step.description}</div>
            {step.count ? <div className={styles.count}>{countText(step.count)}</div> : null}
            {step.fullScreen && !step.locked ? (
              <Link to={step.fullScreen.to} className={styles.note}>
                {step.fullScreen.label}
              </Link>
            ) : null}
          </div>
          {step.locked ? (
            <span className={styles.note}>Needs a different role</span>
          ) : (
            <Link className={buttonClassName(i === 0 ? "primary" : "secondary")} to={step.to}>
              {step.action}
            </Link>
          )}
        </li>
      ))}
    </ol>
  );
}
