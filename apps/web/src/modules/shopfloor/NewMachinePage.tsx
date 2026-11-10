import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { createMachine, describeProblem, IDLE_THRESHOLD, newKey, type NewMachine, type Problem } from "./api/client";
import { UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

interface Values {
  machineId: string;
  resourceId: string;
  siteId: string;
  idealCycleSeconds: string;
  /** Empty means the service default (300 s). */
  idleThresholdSeconds?: string;
}

type Field = keyof Values;
type Errors = Partial<Record<Field, string>>;

const CYCLE = /^\d{1,5}(\.\d{1,3})?$/;
const WHOLE = /^\d{1,4}$/;

/** Checks that mirror the service, and the request the form sends. */
export function validateMachine(values: Values): { errors: Errors; input?: NewMachine } {
  const errors: Errors = {};
  const code = values.machineId.trim();
  if (code.length < 1 || code.length > 64) errors.machineId = "Enter the machine code the edge reports, up to 64 characters.";
  if (!UUID.test(values.resourceId.trim())) errors.resourceId = "Enter the resource id (a UUID).";
  if (values.siteId.trim() !== "" && !UUID.test(values.siteId.trim())) errors.siteId = "Enter the site id as a UUID, or leave it empty.";
  const cycle = values.idealCycleSeconds.trim();
  if (!CYCLE.test(cycle) || /^0+(\.0+)?$/.test(cycle) || Number(cycle) > 86_400) {
    errors.idealCycleSeconds = "Enter seconds per unit above 0 and at most 86400, such as 60 or 1.5.";
  }
  const idle = (values.idleThresholdSeconds ?? "").trim();
  if (idle !== "" && (!WHOLE.test(idle) || Number(idle) < IDLE_THRESHOLD.min || Number(idle) > IDLE_THRESHOLD.max)) {
    errors.idleThresholdSeconds = `Enter whole seconds from ${IDLE_THRESHOLD.min} to ${IDLE_THRESHOLD.max}, or leave it empty for ${IDLE_THRESHOLD.default}.`;
  }
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewMachine = { machineId: code, resourceId: values.resourceId.trim(), siteId: values.siteId.trim() || null, idealCycleSeconds: cycle };
  if (idle !== "") input.idleThresholdSeconds = Number(idle);
  return { errors, input };
}

/** Register a machine so its edge events are stored and its OEE computed. */
export function NewMachinePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Values>({ machineId: "", resourceId: "", siteId: "", idealCycleSeconds: "", idleThresholdSeconds: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateMachine(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createMachine(input, key.current);
      navigate("/shopfloor/machines");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of ["machineId", "resourceId", "siteId", "idealCycleSeconds", "idleThresholdSeconds"] as const) {
        if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      }
      if (problem.code === "SF-0109") fieldErrors.machineId = "This machine code or resource is already registered.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const field = (id: Field, label: string, hint?: string, optional?: boolean, extra?: Record<string, unknown>) => (
    <FormField id={`machine-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input
          {...control}
          {...extra}
          value={values[id] ?? ""}
          onChange={(event) => update({ [id]: event.target.value } as Partial<Values>)}
          invalid={Boolean(errors[id])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="Register machine" description="One machine per resource. Events from the edge connector are matched by the machine code." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {field("machineId", "Machine code", "As the edge connector reports it, e.g. M-101.", false, { maxLength: 64 })}
          {field("resourceId", "Resource id", "The work center resource this machine runs.")}
          {field("siteId", "Site id", "Andon alerts can be filtered by site.", true)}
          {field("idealCycleSeconds", "Ideal cycle time (seconds)", "Seconds per unit at rated speed; sets OEE performance.", false, { inputMode: "decimal" })}
          {field(
            "idleThresholdSeconds",
            "Idle threshold (seconds)",
            `Running without a count for longer than this counts as idle. ${IDLE_THRESHOLD.min} to ${IDLE_THRESHOLD.max}; empty means ${IDLE_THRESHOLD.default}.`,
            true,
            { inputMode: "numeric" },
          )}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not register the machine" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Register machine
            </Button>
            <Link className={buttonClassName("ghost")} to="/shopfloor/machines">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
