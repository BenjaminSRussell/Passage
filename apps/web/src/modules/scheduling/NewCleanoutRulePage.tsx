import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { createCleanoutRule, describeProblem, newKey, type NewCleanoutRule, type Problem } from "./api/client";
import { CODE, mapFields, optionalWhole, wholeNumber, WEEK_MINUTES } from "./forms";
import styles from "./pages.module.css";

export interface CleanoutValues {
  setupGroup: string;
  durationMinutes: string;
  operationSetId: string;
  afterBatches: string;
  afterHours: string;
  atCampaignEnd: boolean;
}

type Errors = Partial<Record<keyof CleanoutValues, string>>;

/** Checks that mirror the service, and the request the form sends. */
export function validateCleanout(values: CleanoutValues): { errors: Errors; input?: NewCleanoutRule } {
  const errors: Errors = {};
  if (!CODE.test(values.setupGroup)) errors.setupGroup = "Use 1 to 32 capital letters, digits, _ or -.";
  const duration = wholeNumber(values.durationMinutes, 1, WEEK_MINUTES);
  if (duration === null) errors.durationMinutes = `Enter 1 to ${WEEK_MINUTES.toLocaleString("en-US")} minutes.`;
  const operationSetId = values.operationSetId.trim();
  if (operationSetId.length > 64) errors.operationSetId = "Use at most 64 characters.";
  const afterBatches = optionalWhole(values.afterBatches, 1, 10_000);
  if (afterBatches === null) errors.afterBatches = "Enter 1 to 10,000, or leave blank.";
  const afterHours = optionalWhole(values.afterHours, 1, 8760);
  if (afterHours === null) errors.afterHours = "Enter 1 to 8,760, or leave blank.";
  if (afterBatches === undefined && afterHours === undefined && !values.atCampaignEnd) {
    errors.atCampaignEnd = "A rule needs a batch limit, an hour limit or a cleanout at campaign end.";
  }
  if (Object.keys(errors).length > 0 || duration === null) return { errors };
  const input: NewCleanoutRule = { setupGroup: values.setupGroup, durationMinutes: duration, atCampaignEnd: values.atCampaignEnd };
  if (operationSetId !== "") input.operationSetId = operationSetId;
  if (afterBatches !== undefined && afterBatches !== null) input.afterBatches = afterBatches;
  if (afterHours !== undefined && afterHours !== null) input.afterHours = afterHours;
  return { errors, input };
}

const fieldNames: Record<string, keyof CleanoutValues> = {
  setupGroup: "setupGroup",
  durationMinutes: "durationMinutes",
  operationSetId: "operationSetId",
  afterBatches: "afterBatches",
  afterHours: "afterHours",
  atCampaignEnd: "atCampaignEnd",
};

/** Define when a setup group must be cleaned out. */
export function NewCleanoutRulePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<CleanoutValues>({ setupGroup: "", durationMinutes: "", operationSetId: "", afterBatches: "", afterHours: "", atCampaignEnd: true });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<CleanoutValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateCleanout(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createCleanoutRule(input, key.current);
      navigate("/scheduling/cleanout-rules");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors = mapFields(problem.fields, fieldNames);
      if (problem.code === "SCHED-0111") fieldErrors.setupGroup = "This setup group already has a cleanout rule.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (name: "setupGroup" | "durationMinutes" | "operationSetId" | "afterBatches" | "afterHours", props: { inputMode?: "numeric"; upper?: boolean } = {}) =>
    function Control(control: { id: string }) {
      return (
        <Input
          {...control}
          inputMode={props.inputMode}
          value={values[name]}
          onChange={(event) => update({ [name]: props.upper ? event.target.value.toUpperCase() : event.target.value })}
          invalid={Boolean(errors[name])}
        />
      );
    };

  return (
    <div className={styles.stack}>
      <PageHeader title="New cleanout rule" description="A cleanout is never split across shifts. A changeover that needs a cleaning set also resets the counters." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="cleanout-group" label="Setup group" error={errors.setupGroup}>
            {text("setupGroup", { upper: true })}
          </FormField>
          <FormField id="cleanout-duration" label="Duration minutes" error={errors.durationMinutes}>
            {text("durationMinutes", { inputMode: "numeric" })}
          </FormField>
          <FormField id="cleanout-set" label="Cleaning operation set" optional hint="The standard operation set the cleanout order runs." error={errors.operationSetId}>
            {text("operationSetId")}
          </FormField>
          <FormField id="cleanout-batches" label="After batches" optional error={errors.afterBatches}>
            {text("afterBatches", { inputMode: "numeric" })}
          </FormField>
          <FormField id="cleanout-hours" label="After run hours" optional error={errors.afterHours}>
            {text("afterHours", { inputMode: "numeric" })}
          </FormField>
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input
                id="cleanout-end"
                type="checkbox"
                checked={values.atCampaignEnd}
                onChange={(event) => update({ atCampaignEnd: event.target.checked })}
                aria-describedby={errors.atCampaignEnd ? "cleanout-end-error" : undefined}
              />
              <label htmlFor="cleanout-end">Clean out at campaign end</label>
            </span>
            {errors.atCampaignEnd ? (
              <p id="cleanout-end-error" role="alert" className={styles.fieldError}>
                {errors.atCampaignEnd}
              </p>
            ) : null}
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the rule" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create rule
            </Button>
            <Link className={buttonClassName("ghost")} to="/scheduling/cleanout-rules">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
