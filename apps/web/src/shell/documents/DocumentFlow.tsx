import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, StatusPill } from "../ui";
import styles from "./DocumentFlow.module.css";

/** One document in a business process: where it is, what it is called and its status. */
export interface FlowDocument {
  id: string;
  /** What the user calls it: "Purchase order PO-1042". Never an id. */
  label: string;
  to?: string;
  status?: string;
  /** The document the panel is shown on. */
  current?: boolean;
}

/** One step of the process with the documents found there, oldest first. */
export interface FlowStage {
  key: string;
  title: string;
  documents: FlowDocument[];
  /** Shown when the stage holds no documents yet. */
  emptyText?: string;
  /** The stage could not be read; shown instead of the documents. */
  unavailable?: boolean;
}

/** What to do next and where, with the values carried over in the link. */
export interface FlowNextStep {
  title: string;
  description: string;
  to: string;
  action: string;
}

export interface DocumentFlowProps {
  /** Names the region, for example "Document flow of purchase order PO-1042". */
  label: string;
  stages: FlowStage[];
  nextStep?: FlowNextStep | null;
  /** Everything in the process is done; say so instead of offering a step. */
  complete?: string;
  loading?: boolean;
  error?: ReactNode;
  onRetry?: () => void;
}

/** The shared document flow panel and next-step offer (procure-to-pay and order-to-cash). */
export function DocumentFlow({ label, stages, nextStep, complete, loading, error, onRetry }: DocumentFlowProps) {
  if (loading) {
    return (
      <Card title="Document flow">
        <p role="status">Loading related documents…</p>
      </Card>
    );
  }
  if (error) {
    return (
      <Alert tone="warning" title="Related documents are not available" live="status" action={onRetry ? <Button size="sm" onClick={onRetry}>Try again</Button> : undefined}>
        {error}
      </Alert>
    );
  }
  const current = stages.findIndex((s) => s.documents.some((d) => d.current));
  return (
    <section aria-label={label} className={styles.panel}>
      <h2 className={styles.title}>Document flow</h2>
      <ol className={styles.stages}>
        {stages.map((stage, i) => (
          <li key={stage.key} className={styles.stage} data-here={i === current ? "true" : undefined} data-empty={stage.documents.length === 0 ? "true" : undefined}>
            <span className={styles.stageTitle}>{stage.title}</span>
            {stage.unavailable ? (
              <span className={styles.none}>Not available to you</span>
            ) : stage.documents.length === 0 ? (
              <span className={styles.none}>{stage.emptyText ?? "None yet"}</span>
            ) : (
              <ul className={styles.docs}>
                {stage.documents.map((d) => (
                  <li key={d.id}>
                    {d.to && !d.current ? <Link to={d.to}>{d.label}</Link> : <span aria-current={d.current ? "page" : undefined}>{d.label}</span>}
                    {d.status ? <StatusPill status={d.status} /> : null}
                  </li>
                ))}
              </ul>
            )}
          </li>
        ))}
      </ol>
      {nextStep ? (
        <div className={styles.next} role="group" aria-label="Next step">
          <div>
            <strong>Next: {nextStep.title}</strong>
            <div className={styles.note}>{nextStep.description}</div>
          </div>
          <Link className={buttonClassName("primary")} to={nextStep.to}>
            {nextStep.action}
          </Link>
        </div>
      ) : complete ? (
        <p className={styles.note}>{complete}</p>
      ) : null}
    </section>
  );
}
