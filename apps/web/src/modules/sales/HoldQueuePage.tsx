import { useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, PageHeader, Select, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeProblem, etagOf, HOLD_TYPES, listHolds, rejectHold, releaseHold, type HoldQueueFilter, type HoldQueueItem, type HoldType, type Problem } from "./api/client";
import { dash, formatDateTime, money, sentence, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { EntityLookup } from "../../shell/lookup/EntityLookup";

const COMMENT_MAX = 500;

/** Only a CREDIT hold of an order still awaiting confirmation can be rejected (the rejection cancels it). */
function canReject(item: HoldQueueItem): boolean {
  return item.holdType === "CREDIT" && (item.orderStatus === "DRAFT" || item.orderStatus === "OPEN");
}

/** "3 d 4 h", "5 h", "12 min": how long a hold has waited. */
export function holdAge(appliedAt: string, now: Date = new Date()): string {
  const minutes = Math.max(0, Math.floor((now.getTime() - new Date(appliedAt).getTime()) / 60000));
  if (Number.isNaN(minutes)) return dash;
  const days = Math.floor(minutes / 1440);
  const hours = Math.floor((minutes % 1440) / 60);
  if (days > 0) return hours > 0 ? `${days} d ${hours} h` : `${days} d`;
  if (hours > 0) return `${hours} h`;
  return `${minutes} min`;
}

/** Whether a hold is past its SLA due time (the service counts the legal entity's business days, FR-27.8). */
export function overSla(slaDueAt: string, now: Date = new Date()): boolean {
  return now.getTime() > new Date(slaDueAt).getTime();
}

/** "11500 vs 11000" for the failed rule, with its note when there is one. */
function ruleText(item: HoldQueueItem): ReactNode {
  const rule = item.failedRule;
  if (!rule) return item.reasonCode;
  return (
    <>
      {rule.id}: {rule.value} vs {rule.threshold}
      {rule.note ? <span className={styles.muted}> ({rule.note})</span> : null}
    </>
  );
}

/**
 * Credit release queue (PRD §5.27.6): ACTIVE holds oldest first with the order,
 * amount, the failed rule against its threshold, the credit limit and the age.
 * A credit manager releases a CREDIT hold here (the release confirms the order)
 * or rejects it with a comment (the unconfirmed order is cancelled).
 */
export function HoldQueuePage() {
  const [draft, setDraft] = useState<{ legalEntityId: string; holdType: HoldType | "" }>({ legalEntityId: "", holdType: "CREDIT" });
  const [filter, setFilter] = useState<HoldQueueFilter>({ holdType: "CREDIT" });
  const [busy, setBusy] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<{ tone: "success" | "warning"; text: ReactNode } | { tone: "danger"; problem: Problem } | null>(null);
  const [rejecting, setRejecting] = useState<HoldQueueItem | null>(null);
  const [comment, setComment] = useState("");
  const [commentError, setCommentError] = useState<string | undefined>();
  const invalid = draft.legalEntityId.trim() && !UUID.test(draft.legalEntityId.trim()) ? "Enter a legal entity UUID." : undefined;
  const state = useLoad((signal) => listHolds(filter, signal), JSON.stringify(filter));

  async function release(item: HoldQueueItem) {
    setBusy(item.holdId);
    setOutcome(null);
    try {
      const res = await releaseHold(item.orderId, item.holdId, etagOf(item.orderVersion));
      const o = res.value;
      const allocation = (o.warnings ?? []).filter((w) => w.code === "SLS-0013").map((w) => w.message);
      setOutcome({
        tone: allocation.length > 0 ? "warning" : "success",
        text: (
          <>
            {sentence(item.holdType)} hold on <Link to={`/sales/orders/${o.id}`}>{o.orderNumber}</Link> released
            {o.confirmed ? " and the order confirmed" : allocation.length > 0 ? ", but the order is not confirmed" : ""}.
            {allocation.length > 0 ? ` ${allocation.join(" ")}` : null}
          </>
        ),
      });
      state.reload();
    } catch (error) {
      setOutcome({ tone: "danger", problem: describeProblem(error) });
    } finally {
      setBusy(null);
    }
  }

  async function reject(item: HoldQueueItem) {
    const text = comment.trim();
    if (!text) {
      setCommentError("Say why the hold is rejected.");
      return;
    }
    if (text.length > COMMENT_MAX) {
      setCommentError(`Keep the comment to ${COMMENT_MAX} characters.`);
      return;
    }
    setBusy(item.holdId);
    setOutcome(null);
    try {
      const res = await rejectHold(item.orderId, item.holdId, etagOf(item.orderVersion), text);
      setOutcome({
        tone: "success",
        text: (
          <>
            Credit hold on <Link to={`/sales/orders/${res.value.id}`}>{res.value.orderNumber}</Link> rejected and the order cancelled.
          </>
        ),
      });
      setRejecting(null);
      setComment("");
      state.reload();
    } catch (error) {
      setOutcome({ tone: "danger", problem: describeProblem(error) });
    } finally {
      setBusy(null);
    }
  }

  const columns: DataTableColumn<HoldQueueItem>[] = [
    { key: "orderNumber", header: "Order", mono: true, render: (h) => <Link to={`/sales/orders/${h.orderId}`}>{h.orderNumber}</Link> },
    { key: "customerId", header: "Customer", render: (h) => <EntityName entity="customer" id={h.customerId} /> },
    { key: "holdType", header: "Hold", render: (h) => <Badge tone={h.holdType === "CREDIT" || h.holdType === "FRAUD" ? "danger" : "warning"}>{sentence(h.holdType)}</Badge> },
    { key: "amount", header: "Amount", align: "end", render: (h) => money(h.amount, h.currency) },
    { key: "rule", header: "Rule failed", render: ruleText },
    { key: "creditLimit", header: "Credit limit", align: "end", render: (h) => (h.creditLimit ? money(h.creditLimit, h.limitCurrency ?? h.currency) : "No profile") },
    {
      key: "age",
      header: "Waiting",
      render: (h) => (
        <span title={`Waiting since ${formatDateTime(h.appliedAt)}; due ${formatDateTime(h.slaDueAt)}`}>
          {holdAge(h.appliedAt)} {overSla(h.slaDueAt) ? <Badge tone="warning">Past SLA</Badge> : null}
        </span>
      ),
    },
    {
      key: "release",
      header: "Action",
      render: (h) => (
        <span className={styles.rowActions}>
          <Button size="sm" loading={busy === h.holdId} disabled={busy !== null && busy !== h.holdId} onClick={() => void release(h)} aria-label={`Release ${sentence(h.holdType)} hold on ${h.orderNumber}`}>
            Release
          </Button>
          {canReject(h) ? (
            <Button
              size="sm"
              variant="danger"
              disabled={busy !== null}
              onClick={() => {
                setRejecting(h);
                setComment("");
                setCommentError(undefined);
                setOutcome(null);
              }}
              aria-label={`Reject Credit hold on ${h.orderNumber}`}
            >
              Reject
            </Button>
          ) : null}
        </span>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Credit release queue"
        description="Orders on hold, oldest first, with the rule that failed against its threshold. Releasing a credit hold confirms the order when nothing else blocks it; rejecting it cancels the order."
      />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          if (!invalid) setFilter({ legalEntityId: draft.legalEntityId.trim(), holdType: draft.holdType });
        }}
      >
        <FormField id="queue-le" label="Legal entity" error={invalid} optional>
          {(control) => <EntityLookup entity="legalEntity" {...control} prefill={false} placeholder="All legal entities" value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })} invalid={Boolean(invalid)} />}
        </FormField>
        <FormField id="queue-type" label="Hold type">
          {(control) => (
            <Select {...control} value={draft.holdType} onChange={(e) => setDraft({ ...draft, holdType: e.target.value as HoldType | "" })}>
              <option value="">Any type</option>
              {HOLD_TYPES.map((t) => (
                <option key={t} value={t}>
                  {sentence(t)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {outcome?.tone === "danger" ? (
        <Alert tone="danger" title="Could not decide the hold" live="alert">
          <ProblemMessage problem={outcome.problem} />
        </Alert>
      ) : null}
      <Alert tone={outcome?.tone === "warning" ? "warning" : "success"} live="status">
        {outcome && outcome.tone !== "danger" ? outcome.text : null}
      </Alert>
      {rejecting ? (
        <Card title={`Reject the credit hold on ${rejecting.orderNumber}`}>
          <div className={styles.form}>
            <p className={styles.note}>
              Rejecting cancels the order: its lines, allocation quota and agreement quantity are returned and its other holds are cleared. This cannot be undone; the customer can be quoted again on a new order.
            </p>
            <FormField id="reject-comment" label="Comment" error={commentError} hint="Kept on the hold and in the order's history.">
              {(control) => <Textarea {...control} rows={3} maxLength={COMMENT_MAX} value={comment} onChange={(e) => setComment(e.target.value)} invalid={Boolean(commentError)} />}
            </FormField>
            <div className={styles.formActions}>
              <Button variant="danger" loading={busy === rejecting.holdId} onClick={() => void reject(rejecting)}>
                Reject hold and cancel order
              </Button>
              <Button variant="secondary" disabled={busy !== null} onClick={() => setRejecting(null)}>
                Keep the hold
              </Button>
            </div>
          </div>
        </Card>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load the release queue" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Holds waiting for release"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(h) => h.holdId}
          loading={state.loading}
          empty={{ title: "Nothing waiting for release", description: "Orders the credit check holds at confirmation wait here for a credit manager." }}
        />
      )}
    </div>
  );
}
