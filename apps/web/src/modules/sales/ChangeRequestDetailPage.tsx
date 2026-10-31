import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, PageHeader, Skeleton, Textarea } from "../../shell/ui";
import { approveChangeRequest, describeProblem, getChangeRequest, rejectChangeRequest, type Problem } from "./api/client";
import { diffColumns } from "./ChangeOrderPage";
import { ChangeStatusBadge } from "./ChangeRequestsPage";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Approving or rejecting a change needs this duty (SalesOrderApprove); the service enforces it. */
export const DUTY_ORDER_APPROVE = "sales.order.approve";
const COMMENT_MAX = 500;

/** Titles for the service's change approval problems (README catalogue). */
const problemTitle: Record<string, string> = {
  "SLS-0006": "The request was changed by someone else",
  "SLS-0013": "Allocation quota cannot cover the changed line",
  "SLS-0024": "The stored change no longer applies",
  "SLS-0044": "The request was already decided",
  "SLS-0045": "You cannot approve your own change",
  "SLS-0046": "The order changed since the request was made",
};

type Outcome = { tone: "success"; text: ReactNode } | { tone: "danger"; problem: Problem };

/** One change request: the diff against the order, then approve (applies it) or reject with a comment. */
export function ChangeRequestDetailPage() {
  const { id = "" } = useParams();
  const canDecide = holdsDuty(usePrincipalDuties(), DUTY_ORDER_APPROVE);
  const state = useLoad((signal) => getChangeRequest(id, signal), id);
  const [comment, setComment] = useState("");
  const [error, setError] = useState("");
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const [busy, setBusy] = useState<"approve" | "reject" | null>(null);

  if (state.error && !state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Change request" />
        <Alert tone="danger" title="Could not load the change request" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Change request" />
        <span className="visually-hidden">Loading the change request…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: request, etag } = state.data;

  async function decide(action: "approve" | "reject", event?: FormEvent) {
    event?.preventDefault();
    const text = comment.trim();
    if (text.length > COMMENT_MAX || (action === "reject" && text.length === 0)) {
      setError(action === "reject" ? `Give a comment of 1 to ${COMMENT_MAX} characters.` : `Use at most ${COMMENT_MAX} characters.`);
      return;
    }
    setError("");
    setOutcome(null);
    setBusy(action);
    try {
      if (action === "approve") {
        const res = await approveChangeRequest(request.id, etag, text);
        setOutcome({
          tone: "success",
          text: (
            <>
              Approved and applied: <Link to={`/sales/orders/${res.value.order.id}`}>{res.value.order.orderNumber}</Link> is now at version {res.value.order.version}.
            </>
          ),
        });
      } else {
        await rejectChangeRequest(request.id, etag, text);
        setOutcome({ tone: "success", text: "Rejected. The order stays as it was." });
      }
      setComment("");
      state.reload();
    } catch (failure) {
      setOutcome({ tone: "danger", problem: describeProblem(failure) });
    } finally {
      setBusy(null);
    }
  }

  const facts: [string, ReactNode][] = [
    ["Order", <Link key="o" to={`/sales/orders/${request.orderId}`}>{request.orderNumber}</Link>],
    ["Made against version", String(request.baseVersion)],
    ["Reason", request.reason || "—"],
    ["Requested", `${formatDateTime(request.requestedAt)} by ${request.requestedBy}`],
    ...(request.decidedAt ? ([["Decided", `${formatDateTime(request.decidedAt)} by ${request.decidedBy ?? "—"}`]] as [string, ReactNode][]) : []),
    ...(request.decisionComment ? ([["Comment", request.decisionComment]] as [string, ReactNode][]) : []),
    ...(request.appliedVersion ? ([["Applied as version", String(request.appliedVersion)]] as [string, ReactNode][]) : []),
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Change request for ${request.orderNumber}`}
        meta={<ChangeStatusBadge status={request.status} />}
        actions={
          <Link className={buttonClassName("ghost")} to="/sales/change-requests">
            Back to change requests
          </Link>
        }
      />
      {outcome?.tone === "danger" ? (
        <Alert
          tone="danger"
          title={problemTitle[outcome.problem.code] ?? "The action failed"}
          live="alert"
          action={
            outcome.problem.code === "SLS-0006" ? (
              <Button size="sm" onClick={() => { setOutcome(null); state.reload(); }}>
                Reload request
              </Button>
            ) : undefined
          }
        >
          {outcome.problem.message} ({outcome.problem.code})
        </Alert>
      ) : null}
      <Alert tone="success" live="status">
        {outcome?.tone === "success" ? outcome.text : null}
      </Alert>
      <Card>
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <DataTable label="Requested changes" columns={diffColumns} rows={request.diff} getRowKey={(c, i) => `${c.lineNo}-${c.field}-${i}`} empty={{ title: "No field changes" }} />
      {request.status === "PENDING" ? (
        <Card title="Decision">
          {canDecide ? (
            <form className={styles.form} onSubmit={(e) => void decide("reject", e)} noValidate aria-label="Decision">
              <FormField id="decision-comment" label="Comment" hint="Required to reject; optional when approving." error={error || undefined}>
                {(control) => <Textarea {...control} rows={2} maxLength={COMMENT_MAX} value={comment} onChange={(e) => setComment(e.target.value)} invalid={Boolean(error)} />}
              </FormField>
              <div className={styles.formActions}>
                <Button variant="primary" type="button" loading={busy === "approve"} disabled={busy !== null && busy !== "approve"} onClick={() => void decide("approve")}>
                  Approve and apply
                </Button>
                <Button type="submit" variant="danger" loading={busy === "reject"} disabled={busy !== null && busy !== "reject"}>
                  Reject
                </Button>
              </div>
            </form>
          ) : (
            <p className={styles.muted}>Deciding a change request needs the {DUTY_ORDER_APPROVE} duty.</p>
          )}
        </Card>
      ) : null}
    </div>
  );
}
