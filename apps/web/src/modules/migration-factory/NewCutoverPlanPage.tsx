import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { describeProblem, generateCutoverPlan, newKey, type GenerateCutoverRequest, type Problem } from "./api/client";
import { localToUtc, PACKS, splitList, type Pack } from "./domain";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export interface PlanValues {
  t0: string;
  packs: Pack[];
  entities: string;
  abortOffsetHours: string;
  rehearsalPlanId: string;
  /** Expand the runbook per legal entity; left out means one plan-wide runbook. */
  expandByEntity?: boolean;
}

type Field = "t0" | "packs" | "entities" | "abortOffsetHours" | "rehearsalPlanId";
type Errors = Partial<Record<Field, string>>;

/** The most legal entities a per-entity runbook covers (the service refuses more). */
const MAX_EXPANDED_ENTITIES = 50;

/** The checks the planner makes, and the request the form sends. */
export function validatePlan(values: PlanValues): { errors: Errors; input?: GenerateCutoverRequest } {
  const errors: Errors = {};
  const t0 = localToUtc(values.t0);
  if (!t0) errors.t0 = "Enter T-0, the start of cutover day.";
  if (values.packs.length === 0) errors.packs = "Select at least one pack.";
  const entities = splitList(values.entities);
  if (entities.length === 0) errors.entities = "Enter at least one legal entity.";
  else if (values.expandByEntity && entities.length > MAX_EXPANDED_ENTITIES) {
    errors.entities = `A per-entity runbook covers at most ${MAX_EXPANDED_ENTITIES} legal entities.`;
  }
  const hours = values.abortOffsetHours.trim();
  if (hours !== "" && (!/^\d{1,2}$/.test(hours) || Number(hours) < 12 || Number(hours) > 72)) {
    errors.abortOffsetHours = "Enter whole hours from 12 to 72.";
  }
  const rehearsal = values.rehearsalPlanId.trim();
  if (rehearsal !== "" && !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(rehearsal)) {
    errors.rehearsalPlanId = "Enter the id of the rehearsal plan (a UUID), or leave it empty.";
  }
  if (Object.keys(errors).length > 0 || !t0) return { errors };
  return {
    errors,
    input: {
      t0,
      packs: values.packs,
      entities,
      ...(hours !== "" ? { abortOffsetHours: Number(hours) } : {}),
      ...(rehearsal !== "" ? { rehearsalPlanId: rehearsal } : {}),
      ...(values.expandByEntity ? { expandByEntity: true } : {}),
    },
  };
}

/** Generate a cutover runbook (FR-49.6). */
export function NewCutoverPlanPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<PlanValues>({ t0: "", packs: ["open_items"], entities: "", abortOffsetHours: "", rehearsalPlanId: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<PlanValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  function togglePack(pack: Pack, on: boolean) {
    update({ packs: on ? PACKS.filter((p) => p === pack || values.packs.includes(p)) : values.packs.filter((p) => p !== pack) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validatePlan(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const plan = await generateCutoverPlan(input, key.current);
      navigate(`/migration-factory/cutover/${plan.planId}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "MIG-0007") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Generate cutover plan" description="Steps run from T-14d to T+30d. Pack loads land at T+4h (balances) or T+12h (open items, orders, on-hand, WIP)." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="plan-t0" label="T-0 (UTC)" hint="00:00 of cutover day." error={errors.t0}>
            {(control) => <Input {...control} type="datetime-local" value={values.t0} invalid={Boolean(errors.t0)} onChange={(event) => update({ t0: event.target.value })} />}
          </FormField>
          <FormField id="plan-entities" label="Legal entities" hint="Separated by commas." error={errors.entities}>
            {(control) => <Input {...control} value={values.entities} invalid={Boolean(errors.entities)} onChange={(event) => update({ entities: event.target.value })} />}
          </FormField>
          <FormField id="plan-abortOffsetHours" label="Abort deadline (hours after T-0)" hint="Default 36." optional error={errors.abortOffsetHours}>
            {(control) => (
              <Input {...control} inputMode="numeric" value={values.abortOffsetHours} invalid={Boolean(errors.abortOffsetHours)} onChange={(event) => update({ abortOffsetHours: event.target.value })} />
            )}
          </FormField>
          <FormField
            id="plan-rehearsalPlanId"
            label="Rehearsal plan"
            hint="Optional. The id of an earlier plan: the durations measured on its completed steps become this plan's planned durations."
            optional
            error={errors.rehearsalPlanId}
          >
            {(control) => (
              <Input {...control} value={values.rehearsalPlanId} invalid={Boolean(errors.rehearsalPlanId)} onChange={(event) => update({ rehearsalPlanId: event.target.value })} />
            )}
          </FormField>
          <div className={styles.check}>
            <Input id="plan-expandByEntity" type="checkbox" checked={Boolean(values.expandByEntity)} onChange={(event) => update({ expandByEntity: event.target.checked })} />
            <label htmlFor="plan-expandByEntity">Expand the runbook per legal entity</label>
          </div>
          <p className={styles.note}>
            Each entity gets its own freeze, extract, load and reconcile steps per pack, a sign-off and a hypercare check, run in that order. The go/no-go steps wait for every entity&apos;s sign-off. Three entities and nine packs give 84 steps.
          </p>
          <fieldset className={styles.choices} aria-describedby={errors.packs ? "plan-packs-error" : undefined}>
            <legend className={styles.legend}>Packs</legend>
            {PACKS.map((pack) => (
              <div key={pack} className={styles.check}>
                <Input id={`plan-pack-${pack}`} type="checkbox" checked={values.packs.includes(pack)} onChange={(event) => togglePack(pack, event.target.checked)} />
                <label htmlFor={`plan-pack-${pack}`}>{pack}</label>
              </div>
            ))}
          </fieldset>
          {errors.packs ? (
            <p id="plan-packs-error" className={styles.note}>
              {errors.packs}
            </p>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not generate the plan" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Generate plan
            </Button>
            <Link className={buttonClassName("ghost")} to="/migration-factory/cutover">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
