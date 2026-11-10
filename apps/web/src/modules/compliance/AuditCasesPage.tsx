import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Textarea, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import {
  CASE_STATUSES,
  CASE_TRANSITIONS,
  describeProblem,
  groupViolations,
  ISSUE_CODES,
  NO_ISSUE_CODES,
  transitionAuditCase,
  type AuditViolation,
  type CaseAction,
  type CaseCategory,
  type CaseDraft,
  type CaseState,
  type CaseStatus,
  type CaseTransitionInput,
  type Problem,
} from "./api/client";
import { formatDateTime, sentence } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const categoryLabel: Record<CaseCategory, string> = {
  DUPLICATE_PAYMENT: "Duplicate payment",
  POLICY_BREACH: "Policy breach",
  SAMPLED_REVIEW: "Sampled review",
};

export const caseStatusTone: Record<CaseStatus, BadgeTone> = {
  OPEN: "warning",
  IN_REVIEW: "info",
  RESOLVED_NO_ISSUE: "success",
  RESOLVED_ISSUE: "danger",
  CLOSED: "neutral",
  ARCHIVED: "neutral",
};

const actionLabel: Record<CaseAction, string> = {
  START_REVIEW: "Start review",
  RESOLVE_NO_ISSUE: "Resolve: no issue",
  RESOLVE_ISSUE: "Resolve: issue found",
  CLOSE: "Close",
  ARCHIVE: "Archive",
};

const DECIMAL = /^\d+(\.\d+)?$/;
const CURRENCY = /^[A-Z]{3}$/;

/**
 * Parses one violation per line: id | policy | rule type | document | party |
 * case. Party is the vendor and required for DUPLICATE; case is set when the
 * violation already belongs to a case.
 */
export function parseViolations(text: string): { violations: AuditViolation[]; error?: string } {
  const violations: AuditViolation[] = [];
  const seen = new Set<string>();
  const lines = text.split("\n");
  for (let i = 0; i < lines.length; i++) {
    if (!lines[i].trim()) continue;
    const [id = "", policyId = "", ruleType = "", documentId = "", partyId = "", caseId = ""] = lines[i].split("|").map((f) => f.trim());
    const at = `Line ${i + 1}`;
    if (!id || !policyId || !ruleType || !documentId) return { violations: [], error: `${at}: write "id | policy | rule type | document | party | case".` };
    if (seen.has(id)) return { violations: [], error: `${at}: violation ${id} is listed twice.` };
    if (ruleType.toUpperCase() === "DUPLICATE" && !partyId) return { violations: [], error: `${at}: a DUPLICATE violation needs its vendor.` };
    seen.add(id);
    const v: AuditViolation = { id, policyId, ruleType: ruleType.toUpperCase(), documentId };
    if (partyId) v.partyId = partyId;
    if (caseId) v.caseId = caseId;
    violations.push(v);
  }
  return { violations };
}

export interface ActionValues {
  status: CaseStatus;
  action: CaseAction | "";
  resolutionCode: string;
  recovery: string;
  currency: string;
  closedOn: string;
}

type ActionErrors = Partial<Record<"action" | "resolutionCode" | "recovery" | "currency" | "closedOn", string>>;

/** The resolution codes that fit an action; CLOSE takes the code already on the case. */
export function codesFor(action: CaseAction | ""): readonly string[] {
  if (action === "RESOLVE_NO_ISSUE") return NO_ISSUE_CODES;
  if (action === "RESOLVE_ISSUE") return ISSUE_CODES;
  if (action === "CLOSE") return [...NO_ISSUE_CODES, ...ISSUE_CODES];
  return [];
}

/** Checks the action form and builds the request; closedOn is a UTC date. */
export function toTransitionInput(v: ActionValues, now: Date = new Date()): { input?: CaseTransitionInput; errors: ActionErrors } {
  const errors: ActionErrors = {};
  if (!v.action || !CASE_TRANSITIONS[v.status][v.action]) errors.action = "Choose an action allowed from this status.";
  const input: CaseTransitionInput = { status: v.status, action: v.action || "START_REVIEW" };
  if (codesFor(v.action).length > 0) {
    if (!codesFor(v.action).includes(v.resolutionCode)) errors.resolutionCode = "Choose a resolution code.";
    else input.resolutionCode = v.resolutionCode;
  }
  if (v.action === "RESOLVE_ISSUE" && v.recovery.trim() !== "") {
    if (!DECIMAL.test(v.recovery.trim())) errors.recovery = "Enter an amount such as 1250.50.";
    else input.recoveryAmount = v.recovery.trim();
    if (Number(v.recovery) > 0 && !CURRENCY.test(v.currency.trim())) errors.currency = "Enter the three-letter currency of the recovery.";
    else if (v.currency.trim()) input.currency = v.currency.trim();
  }
  if (v.action === "ARCHIVE") {
    const closed = v.closedOn ? new Date(`${v.closedOn}T00:00:00Z`) : null;
    if (!closed || Number.isNaN(closed.getTime())) errors.closedOn = "Enter the day the case was closed.";
    else if (closed.getTime() > now.getTime()) errors.closedOn = "The closing day cannot be in the future.";
    else input.closedAt = closed.toISOString();
  }
  if (Object.keys(errors).length > 0) return { errors };
  return { input, errors };
}

const draftColumns: DataTableColumn<CaseDraft>[] = [
  { key: "category", header: "Category", render: (d) => categoryLabel[d.category] ?? sentence(d.category) },
  { key: "key", header: "Grouped by", render: (d) => <span className={styles.mono}>{d.key.slice(d.key.indexOf(":") + 1)}</span> },
  { key: "violationIds", header: "Violations", render: (d) => d.violationIds.join(", ") },
  { key: "documentIds", header: "Documents", align: "end", render: (d) => d.documentIds.length },
];

/**
 * Audit cases (PRD §9.48.6, duty compliance.audit-case.manage): group violations into
 * cases, one per vendor for duplicate payments and one per policy otherwise,
 * and check a case action against the status machine (FR-48.15). Nothing is
 * saved until the audit case store exists.
 */
export function AuditCasesPage() {
  const [violationText, setViolationText] = useState("");
  const [groupError, setGroupError] = useState<string | undefined>();
  const [grouping, setGrouping] = useState(false);
  const [groupFailure, setGroupFailure] = useState<Problem | null>(null);
  const [drafts, setDrafts] = useState<CaseDraft[] | null>(null);

  const [values, setValues] = useState<ActionValues>({ status: "OPEN", action: "START_REVIEW", resolutionCode: "", recovery: "", currency: "", closedOn: "" });
  const [errors, setErrors] = useState<ActionErrors>({});
  const [moving, setMoving] = useState(false);
  const [moveFailure, setMoveFailure] = useState<Problem | null>(null);
  const [moved, setMoved] = useState<CaseState | null>(null);

  const allowed = Object.keys(CASE_TRANSITIONS[values.status]) as CaseAction[];

  async function group(event: FormEvent) {
    event.preventDefault();
    const { violations, error } = parseViolations(violationText);
    const err = error ?? (violations.length === 0 ? "Add at least one violation." : undefined);
    setGroupError(err);
    if (err) return;
    setGrouping(true);
    setGroupFailure(null);
    try {
      setDrafts(await groupViolations(violations));
    } catch (failure) {
      setGroupFailure(describeProblem(failure));
      setDrafts(null);
    } finally {
      setGrouping(false);
    }
  }

  function setStatus(status: CaseStatus) {
    const first = (Object.keys(CASE_TRANSITIONS[status]) as CaseAction[])[0] ?? "";
    setValues((v) => ({ ...v, status, action: first, resolutionCode: "" }));
  }

  async function move(event: FormEvent) {
    event.preventDefault();
    const { input, errors: found } = toTransitionInput(values);
    setErrors(found);
    if (!input) return;
    setMoving(true);
    setMoveFailure(null);
    try {
      setMoved(await transitionAuditCase(input));
    } catch (failure) {
      setMoveFailure(describeProblem(failure));
      setMoved(null);
    } finally {
      setMoving(false);
    }
  }

  const caseCount = drafts?.length ?? 0;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Audit cases"
        description="Group audit violations into cases and check what a case action would do. Closing needs a resolution code; closed cases are archived after the tenant's archive window. Nothing is saved yet."
      />
      <Card title="Group violations into cases">
        <form className={styles.form} onSubmit={group} noValidate aria-label="Group violations">
          <div className={styles.wide}>
            <FormField
              id="case-violations"
              label="Violations"
              hint="One per line: id | policy | rule type | document | party | case. Party is the vendor for DUPLICATE; fill case only when the violation already has one."
              error={groupError}
            >
              {(control) => <Textarea {...control} rows={5} mono value={violationText} onChange={(e) => setViolationText(e.target.value)} invalid={Boolean(groupError)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={grouping}>
              Group into cases
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" title={groupFailure ? "The violations could not be grouped" : undefined} live="alert">
        {groupFailure ? <ProblemMessage problem={groupFailure} /> : null}
      </Alert>
      <Alert tone="info" live="status">
        {drafts ? (caseCount === 0 ? "No new cases: every violation already belongs to a case." : `${caseCount} ${caseCount === 1 ? "case" : "cases"} to open.`) : null}
      </Alert>
      {drafts ? (
        <DataTable label="Cases to open" columns={draftColumns} rows={drafts} getRowKey={(d) => d.key} empty={{ title: "No new cases", description: "Every violation already belongs to a case." }} />
      ) : (
        <Card>
          <EmptyState icon="info" title="No cases yet" description="Paste the violations of an audit run to see the cases they make." compact />
        </Card>
      )}

      <Card title="Check a case action">
        <form className={styles.form} onSubmit={move} noValidate aria-label="Case action">
          <FormField id="case-status" label="Current status">
            {(control) => (
              <Select {...control} value={values.status} onChange={(e) => setStatus(e.target.value as CaseStatus)}>
                {CASE_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {sentence(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="case-action" label="Action" error={errors.action} hint={allowed.length === 0 ? "An archived case takes no further action." : undefined}>
            {(control) => (
              <Select {...control} value={values.action} disabled={allowed.length === 0} onChange={(e) => setValues({ ...values, action: e.target.value as CaseAction, resolutionCode: "" })} invalid={Boolean(errors.action)}>
                {allowed.length === 0 ? <option value="">None</option> : null}
                {allowed.map((a) => (
                  <option key={a} value={a}>
                    {actionLabel[a]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {codesFor(values.action).length > 0 ? (
            <FormField id="case-resolution" label="Resolution code" hint={values.action === "CLOSE" ? "The resolution already on the case." : undefined} error={errors.resolutionCode}>
              {(control) => (
                <Select {...control} value={values.resolutionCode} onChange={(e) => setValues({ ...values, resolutionCode: e.target.value })} invalid={Boolean(errors.resolutionCode)}>
                  <option value="">Choose…</option>
                  {codesFor(values.action).map((c) => (
                    <option key={c} value={c}>
                      {sentence(c)}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          ) : null}
          {values.action === "RESOLVE_ISSUE" ? (
            <>
              <FormField id="case-recovery" label="Recovery amount" error={errors.recovery} optional>
                {(control) => <Input {...control} inputMode="decimal" value={values.recovery} onChange={(e) => setValues({ ...values, recovery: e.target.value })} invalid={Boolean(errors.recovery)} />}
              </FormField>
              <FormField id="case-currency" label="Recovery currency" error={errors.currency} optional>
                {(control) => <Input {...control} maxLength={3} value={values.currency} onChange={(e) => setValues({ ...values, currency: e.target.value.toUpperCase() })} invalid={Boolean(errors.currency)} />}
              </FormField>
            </>
          ) : null}
          {values.action === "ARCHIVE" ? (
            <FormField id="case-closed" label="Closed on (UTC)" error={errors.closedOn}>
              {(control) => <Input {...control} type="date" value={values.closedOn} onChange={(e) => setValues({ ...values, closedOn: e.target.value })} invalid={Boolean(errors.closedOn)} />}
            </FormField>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={moving} disabled={allowed.length === 0}>
              Check action
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" title={moveFailure ? "The action is not allowed" : undefined} live="alert">
        {moveFailure ? <ProblemMessage problem={moveFailure} /> : null}
      </Alert>
      {moved ? (
        <Card title="Case after the action">
          <dl className={styles.details}>
            <div className={styles.entry}>
              <dt className={styles.term}>Status</dt>
              <dd className={styles.value}>
                <Badge tone={caseStatusTone[moved.status]}>{sentence(moved.status)}</Badge>
              </dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>Resolution</dt>
              <dd className={styles.value}>{moved.resolutionCode ? sentence(moved.resolutionCode) : "Not resolved yet"}</dd>
            </div>
            {moved.currency && moved.recoveryAmount !== "0" ? (
              <div className={styles.entry}>
                <dt className={styles.term}>Recovery</dt>
                <dd className={styles.value}>
                  {moved.recoveryAmount} {moved.currency}
                </dd>
              </div>
            ) : null}
            {moved.closedAt ? (
              <div className={styles.entry}>
                <dt className={styles.term}>Closed</dt>
                <dd className={styles.value}>{formatDateTime(moved.closedAt)}</dd>
              </div>
            ) : null}
            <div className={styles.entry}>
              <dt className={styles.term}>Checked at</dt>
              <dd className={styles.value}>{formatDateTime(moved.asOf)}</dd>
            </div>
          </dl>
        </Card>
      ) : null}
    </div>
  );
}
