import { useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, Select, Skeleton, Textarea, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import {
  ACTION_KINDS,
  describeProblem,
  listWorkOrderActions,
  newKey,
  recordWorkOrderActions,
  type ActionInput,
  type ActionKind,
  type ActionOutcome,
  type Problem,
  type Versioned,
  type WorkOrder,
  type WorkOrderAction,
} from "./api/client";
import { DECIMAL, formatDateTime, sentence, utcInstant, utcLocal } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** The duty the service enforces on the action log (PRD 8.45.17). */
export const DUTY_TECHNICIAN = "fieldservice.work-order.execute";
/** Office staff key actions in for a technician only when they also hold this duty (the service refuses others with SVC-SYNC-003). */
export const DUTY_WORK_ORDER = "fieldservice.work-order.maintain";

const WORKED = ["DISPATCHED", "TRAVELING", "IN_PROGRESS", "ON_HOLD", "COMPLETED"];

const tone: Record<ActionOutcome, BadgeTone> = { APPLIED: "success", SUPERSEDED: "neutral", REJECTED: "danger" };

export interface ActionValues {
  kind: ActionKind;
  deviceTime: string;
  lineId: string;
  qty: string;
  signedBy: string;
  signatureRef: string;
  text: string;
}

type Errors = Record<string, string>;

export function emptyAction(now: Date = new Date()): ActionValues {
  return { kind: "WORK_START", deviceTime: utcLocal(now.toISOString()), lineId: "", qty: "", signedBy: "", signatureRef: "", text: "" };
}

/** Checks that mirror the service (api/actions.go recordActions). The device time is read as UTC. */
export function validateAction(values: ActionValues, id: string): { errors: Errors; input?: ActionInput } {
  const errors: Errors = {};
  const at = utcInstant(values.deviceTime);
  if (!at) errors.deviceTime = "Enter the device time as date and time.";
  const input: ActionInput = { id, kind: values.kind, deviceTime: at ?? "" };
  if (values.kind === "PART_USED") {
    if (!values.lineId) errors.lineId = "Choose the part line.";
    if (!DECIMAL.test(values.qty) || Number(values.qty) <= 0) errors.qty = "Enter a positive quantity with at most 6 decimals.";
    input.lineId = values.lineId;
    input.qty = values.qty;
  }
  if (values.kind === "SIGNATURE") {
    if (values.signedBy.trim().length < 1 || values.signedBy.length > 120) errors.signedBy = "Enter who signed, up to 120 characters.";
    if (values.signatureRef.trim().length < 1 || values.signatureRef.length > 500) errors.signatureRef = "Enter the stored signature reference, up to 500 characters.";
    input.signedBy = values.signedBy.trim();
    input.signatureRef = values.signatureRef.trim();
  }
  if (values.kind === "NOTE") {
    if (values.text.length > 4000) errors.text = "Use at most 4000 characters.";
    input.text = values.text;
  }
  return Object.keys(errors).length > 0 ? { errors } : { errors, input };
}

function detail(a: WorkOrderAction): string {
  const p = a.payload ?? {};
  switch (a.kind) {
    case "PART_USED":
      return `${p.qty ?? ""} of line ${(p.lineId ?? "").split("-")[0]}`;
    case "SIGNATURE":
      return `Signed by ${p.signedBy ?? ""}`;
    case "NOTE":
      return p.text ?? "";
    default:
      return "";
  }
}

const columns: DataTableColumn<WorkOrderAction>[] = [
  { key: "deviceTime", header: "Device time", render: (row) => formatDateTime(row.deviceTime), mono: true },
  { key: "kind", header: "Action", render: (row) => sentence(row.kind) },
  { key: "detail", header: "Detail", render: (row) => detail(row) },
  { key: "outcome", header: "Outcome", render: (row) => <Badge tone={tone[row.outcome] ?? "neutral"}>{sentence(row.outcome)}</Badge> },
  { key: "reason", header: "Reason", render: (row) => (row.reasonCode ? `${row.reasonCode}: ${row.reason ?? ""}` : (row.reason ?? "")) },
];

interface Props {
  workOrder: WorkOrder;
  onSynced(next: Versioned<WorkOrder>): void;
}

/**
 * The technician's action log of a work order (BR-45.6), in merge order with each action's
 * outcome, and an entry form for office staff keying actions in for a device that cannot sync.
 */
export function ActionLogPanel({ workOrder: wo, onSynced }: Props) {
  const log = useLoad((signal) => listWorkOrderActions(wo.id, signal), `${wo.id}:${wo.version}`);
  const duties = usePrincipalDuties();
  const canRecord = holdsDuty(duties, DUTY_TECHNICIAN) && holdsDuty(duties, DUTY_WORK_ORDER);
  const [values, setValues] = useState<ActionValues>(() => emptyAction());
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [rejected, setRejected] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const actionId = useRef(newKey());
  const parts = wo.lines.filter((l) => l.kind === "PART");
  const enterable = Boolean(wo.technicianId) && WORKED.includes(wo.status);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setNotice("");
    setRejected([]);
    const { errors: found, input } = validateAction(values, actionId.current);
    setErrors(found);
    if (!input || !wo.technicianId) return;
    setBusy(true);
    try {
      const res = await recordWorkOrderActions(wo.id, wo.technicianId, [input]);
      actionId.current = newKey();
      onSynced({ value: res.workOrder, etag: `"${res.workOrder.version}"` });
      const bad = res.results.filter((r) => r.outcome === "REJECTED");
      setRejected(bad.map((r) => `${r.code ?? ""}: ${r.reason ?? ""}`));
      setNotice(
        res.duplicates > 0
          ? "That action was already recorded."
          : (res.message ?? (res.superseded > 0 ? "The action is behind the work order's state and was kept in the log without changing it." : bad.length === 0 ? "Action recorded." : "")),
      );
      log.reload();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const field = (name: keyof ActionValues, label: string, extra: { type?: string; hint?: string } = {}) => (
    <FormField id={`action-${name}`} label={label} hint={extra.hint} error={errors[name]}>
      {(control) => <Input {...control} type={extra.type} value={values[name]} onChange={(e) => setValues({ ...values, [name]: e.target.value })} invalid={Boolean(errors[name])} />}
    </FormField>
  );

  return (
    <Card title="Technician actions" headingLevel={3}>
      {log.error && !log.data ? (
        <Alert tone="danger" title="Could not load the action log" live="alert" action={<Button size="sm" onClick={log.reload}>Try again</Button>}>
          <ProblemMessage problem={log.error} />
        </Alert>
      ) : !log.data ? (
        <div aria-busy="true">
          <span className="visually-hidden">Loading the action log…</span>
          <Skeleton height="tile" />
        </div>
      ) : (
        <DataTable
          label="Technician actions"
          columns={columns}
          rows={log.data.items ?? []}
          getRowKey={(row) => row.id}
          empty={{ title: "No actions yet", description: "Actions the technician's device syncs, or that the office keys in, appear here in the order they merge." }}
        />
      )}
      {enterable && canRecord ? (
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Record an action">
          <FormField id="action-kind" label="Action">
            {(control) => (
              <Select {...control} value={values.kind} onChange={(e) => setValues({ ...values, kind: e.target.value as ActionKind })}>
                {ACTION_KINDS.map((k) => (
                  <option key={k} value={k}>
                    {sentence(k)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {field("deviceTime", "Device time", { type: "datetime-local", hint: "UTC" })}
          {values.kind === "PART_USED" ? (
            <>
              <FormField id="action-lineId" label="Part line" error={errors.lineId}>
                {(control) => (
                  <Select {...control} value={values.lineId} onChange={(e) => setValues({ ...values, lineId: e.target.value })} invalid={Boolean(errors.lineId)}>
                    <option value="">Choose a part line</option>
                    {parts.map((l) => (
                      <option key={l.id} value={l.id}>{`${l.id.split("-")[0]} (planned ${l.qty})`}</option>
                    ))}
                  </Select>
                )}
              </FormField>
              {field("qty", "Quantity used")}
            </>
          ) : null}
          {values.kind === "SIGNATURE" ? (
            <>
              {field("signedBy", "Signed by")}
              {field("signatureRef", "Signature reference", { hint: "Where the signature image is stored." })}
            </>
          ) : null}
          {values.kind === "NOTE" ? (
            <div className={styles.wide}>
              <FormField id="action-text" label="Note" error={errors.text}>
                {(control) => <Textarea {...control} rows={2} value={values.text} onChange={(e) => setValues({ ...values, text: e.target.value })} invalid={Boolean(errors.text)} />}
              </FormField>
            </div>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not record the action" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          {rejected.length > 0 ? (
            <div className={styles.formActions}>
              <Alert tone="warning" title="The action was refused" live="alert">
                {rejected.join("; ")}
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Record action
            </Button>
          </div>
          <Alert tone="success" live="status">
            {notice}
          </Alert>
        </form>
      ) : (
        <p className={styles.note}>
          {!wo.technicianId
            ? "Actions can be recorded once the work order is assigned and dispatched."
            : !WORKED.includes(wo.status)
              ? "Actions can be recorded from dispatch until the work order is posted."
              : `Keying in actions for the technician needs the ${DUTY_TECHNICIAN} and ${DUTY_WORK_ORDER} duties.`}
        </p>
      )}
    </Card>
  );
}
