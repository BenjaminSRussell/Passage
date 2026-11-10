import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, PageHeader, Skeleton, Textarea } from "../../shell/ui";
import { approveChangeRequest, describeProblem, getChangeRequest, rejectChangeRequest, withdrawChangeRequest, type Problem } from "./api/client";
import { diffColumns } from "./ChangeOrderPage";
import { ChangeStatusBadge } from "./ChangeRequestsPage";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** Approving or rejecting a change needs this duty (SalesOrderApprove); the service enforces it. */
export const DUTY_ORDER_APPROVE = "sales.order.approve";
/** Withdrawing a request needs this duty (SalesOrderMaintain); the service also requires the caller to be the requester. */
export const DUTY_ORDER_WRITE = "sales.order.write";
const COMMENT_MAX = 500;

/** Titles for the service's change approval problems (README catalogue). */
const problemTitle: Record<string, string> = {
  "SLS-0006": "The request was changed by someone else",
  "SLS-0013": "Allocation quota cannot cover the changed line",
  "SLS-0024": "The stored change no longer applies",
  "SLS-0044": "The request was already decided",
  "SLS-0045": "You cannot approve your own change",
  "SLS-0046": "The order changed since the request was made",
  "SLS-0114": "Only the user who made the request can withdraw it",
};

type Outcome = { tone: "success"; text: ReactNode } | { tone: "danger"; problem: Problem };

/** One change request: the diff against the order, then approve (applies it) or reject with a comment. */
export function ChangeRequestDetailPage() {
  const { id = "" } = useParams();
  const duties = usePrincipalDuties();
  const canDecide = holdsDuty(duties, DUTY_ORDER_APPROVE);
  const canWithdraw = holdsDuty(duties, DUTY_ORDER_WRITE);
  const state = useLoad((signal) => getChangeRequest(id, signal), id);
  const [comment, setComment] = useState("");
  const [error, setError] = useState("");
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const [busy, setBusy] = useState<"approve" | "reject" | "withdraw" | null>(null);

  if (state.error && !state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Change request" />
        <Alert tone="danger" title="Could not load the change request" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
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

  async function decide(action: "approve" | "reject" | "withdraw", event?: FormEvent) {
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
              {request.kind === "CANCEL" ? "Approved and cancelled: " : "Approved and applied: "}
              <Link to={`/sales/orders/${res.value.order.id}`}>{res.value.order.orderNumber}</Link> is now at version {res.value.order.version}.
            </>
          ),
        });
      } else if (action === "withdraw") {
        await withdrawChangeRequest(request.id, etag, text);
        setOutcome({ tone: "success", text: "Withdrawn. The order stays as it was." });
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
    ["Type", request.kind === "CANCEL" ? "Cancellation of the order" : "Change of lines or dates"],
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
          <ProblemMessage problem={outcome.problem} />
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
      {request.status === "PENDING" && canWithdraw ? (
        <Card title="Withdraw">
          <p className={styles.muted}>Made this request yourself and no longer want it? Take it back; the order stays as it is.</p>
          <div className={styles.formActions}>
            <Button type="button" loading={busy === "withdraw"} disabled={busy !== null && busy !== "withdraw"} onClick={() => void decide("withdraw")}>
              Withdraw request
            </Button>
          </div>
        </Card>
      ) : null}
    </div>
  );
}
