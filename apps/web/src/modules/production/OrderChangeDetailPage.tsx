import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Alert, Badge, Button, Card, FormField, PageHeader, Skeleton, StatGroup, StatTile, Textarea } from "../../shell/ui";
import {
  applyOrderChange,
  describeProblem,
  dismissOrderChange,
  getOrderChange,
  newIdempotencyKey,
  previewOrderChange,
  type ChangePreview,
  type OrderChange,
  type Problem,
} from "./api/client";
import { changeStatusLabel, changeStatusTone, dash, describeDelta } from "./labels";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export interface OrderChangeDetailPageProps {
  changeId: string;
  onBack?(): void;
  /** Opens the production order the change belongs to. */
  onOpenOrder?(orderId: string): void;
}

const maxNote = 500;

function Detail({ term, children, mono = false }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={`${styles.value} ${mono ? styles.mono : ""}`}>{children}</dd>
    </div>
  );
}

/**
 * One order change (FR-21.16): the record, a preview of the delta and change cost against the order as it
 * is now (BR-21.12), and the planner's decision: apply it, or dismiss it with a note.
 */
export function OrderChangeDetailPage({ changeId, onBack, onOpenOrder }: OrderChangeDetailPageProps) {
  const [change, setChange] = useState<OrderChange | null>(null);
  const [preview, setPreview] = useState<ChangePreview | null>(null);
  // Why the preview is missing on a pending change: the duty, or a failed request.
  const [previewError, setPreviewError] = useState<Problem | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Problem | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [note, setNote] = useState("");
  const [noteError, setNoteError] = useState("");
  const [busy, setBusy] = useState<"apply" | "dismiss" | null>(null);
  // One key per user intent: a retry after a network failure replays instead of applying twice.
  const applyKey = useRef(newIdempotencyKey());

  const load = useCallback(
    async (signal?: AbortSignal) => {
      try {
        const record = await getOrderChange(changeId, signal);
        setChange(record);
        setError(null);
        setPreview(null);
        setPreviewError(null);
        if (record.status === "pending") {
          // The record needs View production orders; the preview needs Apply order change.
          try {
            setPreview(await previewOrderChange(changeId, signal));
          } catch (failed) {
            if (!signal?.aborted) {
              setPreviewError(describeProblem(failed));
            }
          }
        }
      } catch (failed) {
        if (!signal?.aborted) {
          setError(describeProblem(failed));
        }
      } finally {
        if (!signal?.aborted) {
          setLoading(false);
        }
      }
    },
    [changeId],
  );

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);

  const apply = async () => {
    if (!change) {
      return;
    }
    setBusy("apply");
    setFailure(null);
    setNotice("");
    try {
      const result = await applyOrderChange(change.id, change.version, applyKey.current);
      applyKey.current = newIdempotencyKey();
      setChange(result.change);
      setPreview(null);
      setNotice(`Change applied: order ${result.change.orderNumber} is now at version ${result.order.version} with its planned cost re-estimated.`);
    } catch (failed) {
      setFailure(describeProblem(failed));
    } finally {
      setBusy(null);
    }
  };

  const dismiss = async (event: FormEvent) => {
    event.preventDefault();
    if (!change) {
      return;
    }
    if (note.length > maxNote) {
      setNoteError(`Keep the note to ${maxNote} characters or fewer.`);
      return;
    }
    setNoteError("");
    setBusy("dismiss");
    setFailure(null);
    setNotice("");
    try {
      const result = await dismissOrderChange(change.id, change.version, note.trim());
      setChange(result);
      setPreview(null);
      setNotice("Change dismissed: the order stays on its current BOM and route.");
    } catch (failed) {
      setFailure(describeProblem(failed));
    } finally {
      setBusy(null);
    }
  };

  if (loading) {
    return (
      <div className={styles.stack} aria-busy="true">
        <Skeleton height="text" />
        <Skeleton height="tile" />
      </div>
    );
  }
  if (error || !change) {
    return (
      <div className={styles.stack}>
        <Alert tone={error?.status === 404 ? "info" : "danger"} title="The order change is not available" live="alert">
          {error?.status === 404 ? "This order change does not exist." : `${error?.message ?? "Request failed"} (${error?.code ?? "COM-0001"})`}
        </Alert>
        {onBack ? <Button onClick={onBack}>Back to order changes</Button> : null}
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Order ${change.orderNumber}: ${change.ecoId}`}
        description="A newer engineering change supersedes this order's BOM or route."
        actions={
          <>
            {onBack ? <Button onClick={onBack}>Back to order changes</Button> : null}
            {onOpenOrder ? <Button onClick={() => onOpenOrder(change.orderId)}>Open production order</Button> : null}
          </>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert tone="danger" live="alert" title="The change was not saved">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <Card title="Change record">
        <dl className={styles.details}>
          <Detail term="Status">
            <Badge tone={changeStatusTone[change.status]}>{changeStatusLabel[change.status]}</Badge>
          </Detail>
          <Detail term="Profile">{change.profileMode === "automatic" ? "Automatic" : "Manual"}</Detail>
          <Detail term="Review">{change.needsReview ? "Edited by hand: review before applying" : "Standard"}</Detail>
          <Detail term="Replaces">{[change.changesBom ? "BOM" : "", change.changesRoute ? "Route" : ""].filter(Boolean).join(" and ")}</Detail>
          <Detail term="Changes">{describeDelta(change.delta)}</Detail>
          <Detail term="Change cost at detection">{`${change.changeCost} ${change.currency}`}</Detail>
          <Detail term="Detected">
            <time dateTime={change.detectedAt}>{change.detectedAt.slice(0, 10)}</time> by {change.detectedBy}
          </Detail>
          <Detail term="Resolved">
            {change.resolvedAt ? (
              <>
                <time dateTime={change.resolvedAt}>{change.resolvedAt.slice(0, 10)}</time> by {change.resolvedBy}
              </>
            ) : (
              dash
            )}
          </Detail>
          <Detail term="Note">{change.note || dash}</Detail>
          <Detail term="Snapshot before" mono>
            {change.beforeHash ? change.beforeHash.slice(0, 12) : dash}
          </Detail>
          <Detail term="Snapshot after" mono>
            {change.afterHash ? change.afterHash.slice(0, 12) : dash}
          </Detail>
        </dl>
      </Card>
      {previewError ? (
        <Alert tone={previewError.status === 403 ? "info" : "danger"} title="No preview of this change" live="status">
          {previewError.status === 403
            ? "You need the Apply order change duty to preview, apply or dismiss order changes."
            : <ProblemMessage problem={previewError} />}
        </Alert>
      ) : null}
      {preview ? (
        <Card title="Preview against the order now">
          <div className={styles.stack}>
            <StatGroup label={`Change cost in ${change.currency}`}>
              <StatTile label="Planned now" value={preview.cost.oldPlanned} />
              <StatTile label="Planned after" value={preview.cost.newPlanned} />
              <StatTile label="Delta" value={preview.cost.delta} />
              <StatTile label="Surcharge" value={preview.cost.surcharge} />
              <StatTile label="Change cost" value={`${preview.cost.total} ${change.currency}`} />
            </StatGroup>
            <p className={styles.hint}>{describeDelta(preview.delta)}.</p>
            {preview.blocked ? (
              <Alert tone="warning" title="This change cannot be applied now" live="status">
                {preview.blocked.detail} ({preview.blocked.code})
              </Alert>
            ) : null}
            <div className={styles.formActions}>
              <Button variant="primary" onClick={apply} loading={busy === "apply"} disabled={preview.blocked !== null || busy !== null}>
                Apply change
              </Button>
            </div>
          </div>
        </Card>
      ) : null}
      {change.status === "pending" && !(previewError?.status === 403) ? (
        <Card title="Dismiss">
          <form className={styles.form} onSubmit={dismiss} noValidate aria-label="Dismiss the change">
            <FormField id="chg-note" label="Note" optional hint="Why the order stays on its version." error={noteError}>
              {(control) => <Textarea {...control} value={note} onChange={(e) => setNote(e.target.value)} rows={3} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" loading={busy === "dismiss"} disabled={busy !== null}>
                Dismiss change
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
    </div>
  );
}
