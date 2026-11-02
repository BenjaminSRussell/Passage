import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  ConfirmDialog,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  StatGroup,
  StatTile,
  Textarea,
  type BadgeTone,
  type DataTableColumn,
} from "../../shell/ui";
import {
  describeProblem,
  getReadiness,
  newKey,
  recordDecision,
  type Attestation,
  type ChecklistItem,
  type ChecklistStatus,
  type DecisionRequest,
  type GoApproval,
  type GoNoGoDecision,
  type GoNoGoValue,
  type Problem,
} from "./api/client";
import { formatDateTime } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const statusTone: Record<ChecklistStatus, BadgeTone> = { MET: "success", OPEN: "danger", UNCONFIRMED: "warning" };
const statusLabel: Record<ChecklistStatus, string> = { MET: "Met", OPEN: "Open", UNCONFIRMED: "Unconfirmed" };

export interface DecisionValues {
  decision: GoNoGoValue;
  reason: string;
  attestations: Record<string, { confirmed: boolean; reference: string }>;
}

type Field = "reason" | "attestations";
export type DecisionErrors = Partial<Record<Field, string>>;

/**
 * The checks the service makes (CUT-1606, NO_GO needs a reason), and the request the form sends. The approver is the
 * signed-in user, so no approvers are sent (CUT-1605). The second approver of a GO approves the pending approval's
 * attestations, so none are sent for it.
 */
export function validateDecision(values: DecisionValues, version: number, pendingGo?: GoApproval): { errors: DecisionErrors; input?: DecisionRequest } {
  const errors: DecisionErrors = {};
  if (values.decision === "NO_GO" && values.reason.trim() === "") errors.reason = "A NO_GO decision needs a reason.";
  const approving = values.decision === "GO" && pendingGo !== undefined;
  const attestations: Attestation[] = [];
  for (const [code, a] of approving ? [] : Object.entries(values.attestations)) {
    if (!a.confirmed) continue;
    if (a.reference.trim() === "") {
      errors.attestations = "Every confirmed item needs a reference to its evidence (CUT-1606).";
      break;
    }
    attestations.push({ code, confirmed: true, reference: a.reference.trim() });
  }
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: { decision: values.decision, ...(values.reason.trim() ? { reason: values.reason.trim() } : {}), ...(approving ? {} : { attestations }), version },
  };
}

function runLink(item: ChecklistItem, id: string) {
  const to = item.code === "parallel_run_signed_off" ? `/migration-factory/parallel-runs/${id}` : `/migration-factory/runs/${id}`;
  return (
    <Link key={id} to={to} className={styles.mono}>
      {id.slice(0, 8)}
    </Link>
  );
}

const checklistColumns: DataTableColumn<ChecklistItem>[] = [
  { key: "title", header: "Checklist item" },
  { key: "source", header: "Checked by", render: (row) => (row.source === "system" ? "Migration factory" : "Approvers") },
  { key: "status", header: "Status", render: (row) => <Badge tone={statusTone[row.status]}>{statusLabel[row.status]}</Badge> },
  {
    key: "detail",
    header: "Evidence",
    render: (row) => (
      <span>
        {row.detail ?? ""}
        {row.references.length > 0 ? (
          <>
            {" "}
            {row.references.map((id, i) => (
              <span key={id}>
                {i > 0 ? ", " : ""}
                {runLink(row, id)}
              </span>
            ))}
          </>
        ) : null}
      </span>
    ),
  },
];

const decisionColumns: DataTableColumn<GoNoGoDecision>[] = [
  { key: "seq", header: "#", mono: true },
  { key: "decision", header: "Decision", render: (row) => <Badge tone={row.decision === "GO" ? "success" : "danger"}>{row.decision === "GO" ? "Go" : "No-go"}</Badge> },
  { key: "approvers", header: "Approvers", render: (row) => row.approvers.join(", ") },
  { key: "reason", header: "Reason", render: (row) => row.reason ?? "" },
  { key: "decidedBy", header: "Recorded by", render: (row) => row.decidedBy ?? "" },
  { key: "decidedAt", header: "When", render: (row) => formatDateTime(row.decidedAt) },
];

const emptyValues: DecisionValues = { decision: "NO_GO", reason: "", attestations: {} };

function conflictMessage(problem: Problem): string {
  return problem.code === "MIG-0006" ? "Someone changed this plan since you opened it. The checklist has been reloaded; check it and try again." : problem.message;
}

/**
 * The go/no-go decision of a cutover plan (FR-49.6, PRD 9.49.1): the checklist evaluated from the
 * factory's pack runs and parallel runs plus the items the approvers attest, the decisions so far,
 * a GO approval waiting for its second approver, and the form that records a decision or approves a
 * GO as the signed-in user. The service evaluates the checklist again when it stores the request.
 */
export function CutoverDecisionPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getReadiness(id, signal), id);
  const readiness = state.data;
  const [values, setValues] = useState<DecisionValues>(emptyValues);
  const [errors, setErrors] = useState<DecisionErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [pending, setPending] = useState<DecisionRequest | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<DecisionValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  function attest(code: string, patch: Partial<{ confirmed: boolean; reference: string }>) {
    const current = values.attestations[code] ?? { confirmed: false, reference: "" };
    update({ attestations: { ...values.attestations, [code]: { ...current, ...patch } } });
  }

  function ask(event: FormEvent) {
    event.preventDefault();
    if (!readiness) return;
    const { errors: found, input } = validateDecision(values, readiness.version, readiness.pendingGo);
    setErrors(found);
    setFailure(null);
    setDone(null);
    if (!input) return;
    if (input.decision === "GO") setPending(input);
    else void send(input);
  }

  async function send(input: DecisionRequest) {
    setPending(null);
    setBusy(true);
    try {
      const result = await recordDecision(id, input, key.current);
      key.current = newKey();
      setValues(emptyValues);
      setDone(
        result.decision
          ? `${result.decision.decision === "GO" ? "GO" : "NO_GO"} recorded with ${result.decision.approvers.join(" and ")} as approvers.`
          : "Your approval of the GO is recorded. It becomes the decision when a second, different CutoverManager approves it.",
      );
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure({ ...problem, message: conflictMessage(problem) });
      if (problem.code === "MIG-0006") state.reload();
      key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const attested = readiness?.items.filter((item) => item.source === "attested") ?? [];
  const gone = readiness?.decisions.some((d) => d.decision === "GO") ?? false;
  const met = readiness?.items.filter((item) => item.status === "MET").length ?? 0;
  const pendingGo = readiness?.pendingGo;
  const approving = values.decision === "GO" && pendingGo !== undefined;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Go/no-go decision"
        description="The phase-exit checklist for this cutover. Any open item is NO-GO."
        actions={
          <Link className={buttonClassName("ghost")} to={`/migration-factory/cutover/${id}`}>
            Runbook
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the checklist" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="The decision was not recorded" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {done ? (
        <Alert tone="success" title="Recorded" live="status">
          {done}
        </Alert>
      ) : null}
      {state.loading && !readiness && !state.error ? <p className={styles.note}>Loading the checklist.</p> : null}
      {readiness ? (
        <>
          {readiness.rolledBack ? (
            <Alert tone="warning" title="This plan was rolled back">
              No further decision can be recorded on it.
            </Alert>
          ) : null}
          <StatGroup label="Readiness">
            <StatTile label="Items met" value={`${met} / ${readiness.items.length}`} tone={met === readiness.items.length ? "neutral" : "warning"} />
            <StatTile label="Factory checks" value={readiness.systemReady ? "Met" : "Open"} tone={readiness.systemReady ? "neutral" : "danger"} />
            <StatTile label="Decisions recorded" value={readiness.decisions.length} />
          </StatGroup>
          {pendingGo && !gone && !readiness.rolledBack ? (
            <Alert tone="info" title="GO waiting for a second approver">
              {pendingGo.approver} approved a GO on {formatDateTime(pendingGo.approvedAt)} with the evidence shown in the checklist. A different
              CutoverManager records the GO by approving it below; a no-go recorded instead voids this approval.
            </Alert>
          ) : null}
          <Card title="Checklist">
            <DataTable label="Go/no-go checklist" columns={checklistColumns} rows={readiness.items} getRowKey={(row) => row.code} />
          </Card>
          <Card title="Decisions">
            <DataTable
              label="Decisions"
              columns={decisionColumns}
              rows={readiness.decisions}
              getRowKey={(row) => String(row.seq)}
              empty={{ title: "No decision recorded yet", description: "Record a no-go while items are open, or a go once every item is met." }}
            />
          </Card>
          {readiness.rolledBack || gone ? (
            <p className={styles.note}>{gone ? "A GO decision is recorded for this plan." : "The plan was rolled back."}</p>
          ) : (
            <Card title="Record decision">
              <form className={styles.form} onSubmit={ask} noValidate>
                <FormField id="decision-value" label="Decision">
                  {(control) => (
                    <Select {...control} value={values.decision} onChange={(event) => update({ decision: event.target.value as GoNoGoValue })}>
                      <option value="NO_GO">No-go</option>
                      <option value="GO">Go</option>
                    </Select>
                  )}
                </FormField>
                <p className={styles.note}>
                  {values.decision === "NO_GO"
                    ? "You are recorded as the approver of this no-go."
                    : approving
                      ? `You approve the GO ${pendingGo.approver} approved, with the same evidence. The GO is recorded with both of you as approvers.`
                      : "A GO needs two different CutoverManagers. Yours is recorded as the first approval; a second CutoverManager then approves it."}
                </p>
                <FormField id="decision-reason" label="Reason" hint="Required for a no-go." optional={values.decision === "GO"} error={errors.reason}>
                  {(control) => (
                    <Textarea {...control} rows={3} value={values.reason} invalid={Boolean(errors.reason)} onChange={(event) => update({ reason: event.target.value })} />
                  )}
                </FormField>
                {approving ? null : (
                  <fieldset className={styles.choices} aria-describedby={errors.attestations ? "decision-attest-error" : undefined}>
                    <legend className={styles.legend}>Confirm checklist items</legend>
                    {attested.map((item) => {
                      const a = values.attestations[item.code] ?? { confirmed: false, reference: "" };
                      return (
                        <div key={item.code} className={styles.wide}>
                          <div className={styles.check}>
                            <Input id={`attest-${item.code}`} type="checkbox" checked={a.confirmed} onChange={(event) => attest(item.code, { confirmed: event.target.checked })} />
                            <label htmlFor={`attest-${item.code}`}>{item.title}</label>
                          </div>
                          {a.confirmed ? (
                            <Input
                              aria-label={`Evidence for ${item.title}`}
                              placeholder="Link or ticket holding the evidence"
                              value={a.reference}
                              onChange={(event) => attest(item.code, { reference: event.target.value })}
                            />
                          ) : null}
                        </div>
                      );
                    })}
                  </fieldset>
                )}
                {errors.attestations ? (
                  <p id="decision-attest-error" role="alert" className={styles.note}>
                    {errors.attestations}
                  </p>
                ) : null}
                <div className={styles.formActions}>
                  <Button type="submit" variant="primary" disabled={busy}>
                    {busy ? "Recording" : approving ? "Approve GO" : "Record decision"}
                  </Button>
                </div>
              </form>
            </Card>
          )}
        </>
      ) : null}
      {pending ? (
        <ConfirmDialog
          title={pendingGo ? "Record the GO decision?" : "Approve a GO?"}
          description={
            pendingGo
              ? `This records the GO once, with ${pendingGo.approver} and you as approvers. It is checked against the factory's pack runs and parallel runs again when it is stored.`
              : "This records your approval of a GO. It becomes the decision when a second, different CutoverManager approves it, and is checked against the factory's pack runs and parallel runs again then."
          }
          confirmLabel={pendingGo ? "Record GO" : "Approve GO"}
          cancelLabel="Cancel"
          onConfirm={() => void send(pending)}
          onCancel={() => setPending(null)}
        />
      ) : null}
    </div>
  );
}
