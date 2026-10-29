import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  acceptRisk,
  describeProblem,
  getConflict,
  mitigateConflict,
  newKey,
  type ConflictDetail,
  type Mitigation,
  type MitigationInput,
  type Problem,
  type Versioned,
} from "./api/client";
import { rolesText } from "./ConflictsPage";
import { dash, dateIn, formatDateTime, sentence, severityTone, stateTone } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** Default mitigation window offered on the form (AC-48.4 uses 180 days). */
export const DEFAULT_MITIGATION_DAYS = 180;

export interface MitigationValues {
  controlRef: string;
  reason: string;
  expiresOn: string;
}

type Errors = Partial<Record<keyof MitigationValues, string>>;

/** Checks that mirror the service (api.mitigate): control, reason and a future expiry date. */
export function validateMitigation(values: MitigationValues, now: Date = new Date()): { errors: Errors; input?: MitigationInput } {
  const errors: Errors = {};
  if (!values.controlRef.trim()) errors.controlRef = "Name the compensating control.";
  if (!values.reason.trim()) errors.reason = "Explain why the control covers the risk.";
  if (!DATE.test(values.expiresOn)) errors.expiresOn = "Choose the expiry date.";
  else if (values.expiresOn <= now.toISOString().slice(0, 10)) errors.expiresOn = "Choose a date after today.";
  if (Object.keys(errors).length > 0) return { errors };
  // The mitigation lapses at the end of the chosen day, UTC.
  return { errors, input: { controlRef: values.controlRef.trim(), reason: values.reason.trim(), expiresAt: `${values.expiresOn}T23:59:59Z` } };
}

const mitigationColumns: DataTableColumn<Mitigation>[] = [
  { key: "approvedAt", header: "Approved", render: (m) => formatDateTime(m.approvedAt) },
  { key: "approverId", header: "Approver", mono: true },
  { key: "controlRef", header: "Control", mono: true },
  { key: "reason", header: "Reason" },
  { key: "expiresAt", header: "Expires", render: (m) => formatDateTime(m.expiresAt) },
];

/** One conflict: what it is, its mitigation history, and the mitigation and risk-acceptance forms. */
export function ConflictDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getConflict(id, signal), id);
  const [latest, setLatest] = useState<Versioned<ConflictDetail> | null>(null);
  const [values, setValues] = useState<MitigationValues>({ controlRef: "", reason: "", expiresOn: dateIn(DEFAULT_MITIGATION_DAYS) });
  const [errors, setErrors] = useState<Errors>({});
  const [riskReason, setRiskReason] = useState("");
  const [riskError, setRiskError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState<"mitigate" | "accept" | null>(null);
  const key = useRef(newKey());

  const current = latest && state.data && latest.value.conflictId === state.data.value.conflictId && latest.value.version >= state.data.value.version ? latest : state.data;

  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="SoD conflict" />
        <Alert tone="danger" title="Could not load the conflict" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link to="/compliance/sod/conflicts">Back to conflicts</Link>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="SoD conflict" />
        <span className="visually-hidden">Loading the conflict…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const conflict = current.value;
  const etag = current.etag;

  function update(patch: Partial<MitigationValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function run(kind: "mitigate" | "accept", action: () => Promise<Versioned<ConflictDetail>>, message: string) {
    setFailure(null);
    setDone(null);
    setBusy(kind);
    try {
      setLatest(await action());
      setDone(message);
      key.current = newKey();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (kind === "mitigate" && Object.keys(problem.fields).length > 0) setErrors(problem.fields as Errors);
      if (problem.code === "SOD-1110") key.current = newKey();
    } finally {
      setBusy(null);
    }
  }

  function submitMitigation(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateMitigation(values);
    setErrors(found);
    if (!input) return;
    void run("mitigate", () => mitigateConflict(conflict.conflictId, etag, input, key.current), `Conflict mitigated until ${values.expiresOn}.`);
  }

  function submitRisk(event: FormEvent) {
    event.preventDefault();
    const reason = riskReason.trim();
    setRiskError(reason ? undefined : "Explain why the risk is acceptable.");
    if (!reason) return;
    void run("accept", () => acceptRisk(conflict.conflictId, etag, reason), "Risk accepted.");
  }

  const facts: [string, ReactNode][] = [
    ["Rule", <span className={styles.mono}>{conflict.ruleId}</span>],
    ["Risk id", <span className={styles.mono}>{conflict.riskId}</span>],
    ["Severity", <Badge tone={severityTone[conflict.severity]}>{sentence(conflict.severity)}</Badge>],
    ["State", <Badge tone={stateTone[conflict.state]}>{sentence(conflict.state)}</Badge>],
    ["User", <span className={styles.mono}>{conflict.userId}</span>],
    ["Duties", `${conflict.dutyA} and ${conflict.dutyB}`],
    ["Granted by roles", rolesText(conflict)],
    ["Detected", formatDateTime(conflict.detectedAt)],
    ["Mitigation expires", conflict.state === "MITIGATED" ? formatDateTime(conflict.expiresAt) : dash],
    ["Decided by", conflict.decidedBy ? <span className={styles.mono}>{conflict.decidedBy}</span> : dash],
    ["Decision reason", conflict.decisionReason ?? dash],
    ["Resolved", formatDateTime(conflict.resolvedAt)],
  ];
  const open = conflict.state === "OPEN";
  const canAccept = open && conflict.severity !== "HIGH";

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Conflict ${conflict.ruleId}`}
        description={`${conflict.dutyA} and ${conflict.dutyB} held by one user.`}
        meta={<Badge tone={stateTone[conflict.state]}>{sentence(conflict.state)}</Badge>}
        actions={<Link to="/compliance/sod/conflicts">Back to conflicts</Link>}
      />
      <Card title="Conflict">
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <Alert tone="danger" title={failure ? "Could not save" : undefined} live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {open ? (
        <Card title="Mitigate">
          <form className={styles.form} onSubmit={submitMitigation} noValidate aria-label="Mitigate conflict">
            <p className={`${styles.note} ${styles.wide}`}>
              You approve this mitigation as yourself, and you cannot approve one for your own conflict. The expiry cannot be later than the rule's or your tenant's maximum mitigation period; when it passes, the conflict opens again.
            </p>
            <FormField id="mitigation-control" label="Compensating control" error={errors.controlRef}>
              {(control) => <Input {...control} className={styles.mono} value={values.controlRef} onChange={(e) => update({ controlRef: e.target.value })} invalid={Boolean(errors.controlRef)} />}
            </FormField>
            <FormField id="mitigation-expires" label="Expires on" error={errors.expiresOn}>
              {(control) => <Input {...control} type="date" value={values.expiresOn} onChange={(e) => update({ expiresOn: e.target.value })} invalid={Boolean(errors.expiresOn)} />}
            </FormField>
            <div className={styles.wide}>
              <FormField id="mitigation-reason" label="Reason" error={errors.reason}>
                {(control) => <Textarea {...control} rows={3} value={values.reason} onChange={(e) => update({ reason: e.target.value })} invalid={Boolean(errors.reason)} />}
              </FormField>
            </div>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "mitigate"} disabled={busy === "accept"}>
                Approve mitigation
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      {canAccept ? (
        <Card title="Accept risk">
          <form className={styles.form} onSubmit={submitRisk} noValidate aria-label="Accept risk">
            <p className={`${styles.note} ${styles.wide}`}>Only low and medium conflicts can be accepted, and only by an SoD administrator.</p>
            <div className={styles.wide}>
              <FormField id="risk-reason" label="Risk acceptance reason" error={riskError}>
                {(control) => <Textarea {...control} rows={2} value={riskReason} onChange={(e) => setRiskReason(e.target.value)} invalid={Boolean(riskError)} />}
              </FormField>
            </div>
            <div className={styles.formActions}>
              <Button type="submit" loading={busy === "accept"} disabled={busy === "mitigate"}>
                Accept risk
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      <Card title="Mitigation history">
        <DataTable
          label="Mitigation history"
          columns={mitigationColumns}
          rows={conflict.mitigations}
          getRowKey={(m) => m.mitigationId}
          empty={{ title: "No mitigations yet", description: "Approved compensating controls are kept here, including lapsed ones." }}
        />
      </Card>
    </div>
  );
}
