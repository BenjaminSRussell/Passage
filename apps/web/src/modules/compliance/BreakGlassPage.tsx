import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, EmptyState, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { BREAK_GLASS_REASONS, describeProblem, planElevatedSession, type BreakGlassReason, type ElevatedPlan, type ElevatedPlanInput, type Problem } from "./api/client";
import { formatDateTime, UUID } from "./format";
import styles from "./pages.module.css";

export const reasonLabel: Record<BreakGlassReason, string> = {
  INCIDENT_RESPONSE: "Incident response",
  PRODUCTION_OUTAGE: "Production outage",
  DATA_CORRECTION_APPROVED: "Approved data correction",
  PERIOD_CLOSE_BLOCKER: "Period close blocker",
  AUDIT_EVIDENCE_REQUEST: "Audit evidence request",
};

export interface BreakGlassValues {
  target: string;
  roles: string;
  reason: BreakGlassReason;
  ticket: string;
  minutes: string;
}

type Errors = Partial<Record<"target" | "roles" | "ticket" | "minutes", string>>;

/** Checks the form and builds the request; the requester is always the signed-in user. */
export function toPlanInput(v: BreakGlassValues): { input?: ElevatedPlanInput; errors: Errors } {
  const errors: Errors = {};
  const target = v.target.trim();
  if (target && !UUID.test(target)) errors.target = "Enter the user's UUID, or leave empty for yourself.";
  const roles = [...new Set(v.roles.split(/[\n,]/).map((r) => r.trim()).filter(Boolean))];
  if (roles.length === 0) errors.roles = "Enter at least one role.";
  if (!v.ticket.trim()) errors.ticket = "Enter the incident or change ticket.";
  let minutes: number | undefined;
  if (v.minutes.trim()) {
    minutes = Number(v.minutes.trim());
    if (!Number.isInteger(minutes) || minutes < 15 || minutes > 240) errors.minutes = "Enter whole minutes from 15 to 240.";
  }
  if (Object.keys(errors).length > 0) return { errors };
  const input: ElevatedPlanInput = { roles, reasonCode: v.reason, ticketRef: v.ticket.trim() };
  if (target) input.targetUserId = target.toLowerCase();
  if (minutes !== undefined) input.requestedMinutes = minutes;
  return { input, errors };
}

/** How the planned session gets switched on. */
export function approvalText(plan: ElevatedPlan): string {
  if (plan.approverDuty === "platform.global.admin") return "Waits for a second platform.global.admin to approve (protected role).";
  if (plan.needsApproval) return "Waits for a break-glass approver.";
  return "Starts at once; a reviewer must sign off afterwards.";
}

/**
 * Break-glass access (PRD §9.48.6 Break-glass sessions): plan an emergency
 * elevated session for yourself or another user (FR-48.23). The tenant sets
 * the length cap and whether sessions self-activate; compliance.security.admin and
 * platform.global.admin can never be granted to yourself (BR-48.19). No session is
 * opened and no token issued yet.
 */
export function BreakGlassPage() {
  const [values, setValues] = useState<BreakGlassValues>({ target: "", roles: "", reason: "INCIDENT_RESPONSE", ticket: "", minutes: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [plan, setPlan] = useState<ElevatedPlan | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { input, errors: found } = toPlanInput(values);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    setFailure(null);
    try {
      setPlan(await planElevatedSession(input));
    } catch (error) {
      setFailure(describeProblem(error));
      setPlan(null);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Break-glass access"
        description="Plan an emergency elevated session: how long it lasts, who must approve it and when the reviewer must sign off its call log. Nothing is opened yet."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Break-glass request">
          <FormField id="bg-target" label="For user" hint="Leave empty to request access for yourself." error={errors.target} optional>
            {(control) => <Input {...control} value={values.target} onChange={(e) => setValues({ ...values, target: e.target.value })} invalid={Boolean(errors.target)} />}
          </FormField>
          <FormField id="bg-roles" label="Roles" hint="Separate roles with commas." error={errors.roles}>
            {(control) => <Input {...control} value={values.roles} onChange={(e) => setValues({ ...values, roles: e.target.value })} invalid={Boolean(errors.roles)} />}
          </FormField>
          <FormField id="bg-reason" label="Reason">
            {(control) => (
              <Select {...control} value={values.reason} onChange={(e) => setValues({ ...values, reason: e.target.value as BreakGlassReason })}>
                {BREAK_GLASS_REASONS.map((r) => (
                  <option key={r} value={r}>
                    {reasonLabel[r]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="bg-ticket" label="Ticket reference" error={errors.ticket}>
            {(control) => <Input {...control} value={values.ticket} onChange={(e) => setValues({ ...values, ticket: e.target.value })} invalid={Boolean(errors.ticket)} />}
          </FormField>
          <FormField id="bg-minutes" label="Minutes" hint="60 when left empty; the tenant may cap it lower." error={errors.minutes} optional>
            {(control) => <Input {...control} inputMode="numeric" value={values.minutes} onChange={(e) => setValues({ ...values, minutes: e.target.value })} invalid={Boolean(errors.minutes)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Plan session
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" title={failure ? "The request was refused" : undefined} live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      <Alert tone="info" live="status">
        {plan ? `${plan.minutes}-minute session planned. ${approvalText(plan)}` : null}
      </Alert>
      {plan ? (
        <Card title="Planned session">
          <dl className={styles.details}>
            <div className={styles.entry}>
              <dt className={styles.term}>Roles</dt>
              <dd className={styles.value}>
                <span className={styles.badges}>
                  {plan.roles.map((r) => (
                    <Badge key={r} tone="info">
                      {r}
                    </Badge>
                  ))}
                </span>
              </dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>For user</dt>
              <dd className={`${styles.value} ${styles.mono}`}>{plan.targetUserId === plan.requesterId ? "You" : plan.targetUserId}</dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>Starts</dt>
              <dd className={styles.value}>{formatDateTime(plan.startsAt)}</dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>Ends</dt>
              <dd className={styles.value}>{formatDateTime(plan.endsAt)}</dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>Review due</dt>
              <dd className={styles.value}>{formatDateTime(plan.reviewDueAt)}</dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>Approval</dt>
              <dd className={styles.value}>{approvalText(plan)}</dd>
            </div>
          </dl>
        </Card>
      ) : (
        <Card>
          <EmptyState icon="info" title="No session planned" description="Enter the roles, reason and ticket to see the session the request would open." compact />
        </Card>
      )}
    </div>
  );
}
