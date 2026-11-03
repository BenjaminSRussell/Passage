import { Button, FormField, Input, Select } from "../../shell/ui";
import { PROCEDURE_STEP_TYPES, type ExclusionMode, type ProcedureStep, type ProcedureStepType } from "./api/client";
import { STEP_TYPE_LABELS } from "./campaignFormat";
import styles from "./pages.module.css";

type Errors = Record<string, string>;

export const MAX_STEPS = 99;

/** The reference procedure AX_STANDARD as the engine runs it today (FR-26.1). */
export function referenceSteps(): ProcedureStep[] {
  const step = (stepNo: number, conditionType: ProcedureStepType, accountKey = ""): ProcedureStep => ({
    stepNo,
    conditionType,
    requirementCel: "",
    exclusionGroup: "",
    exclusionMode: "",
    statistical: false,
    accountKey,
  });
  return [step(10, "PRICE"), step(20, "LINE_DISC"), step(30, "MULTILINE_DISC"), step(40, "TOTAL_DISC"), step(50, "SURCHARGE", "ERS")];
}

/**
 * Client checks that mirror the service's step rules (FR-26.1, FR-26.8): steps 10–990 in
 * increasing order, exactly one PRICE step, every other condition type at most once,
 * requirement ≤ 512 characters, an exclusion group
 * with a BEST or FIRST mode (one mode per group, never on PRICE). Keys match the service's
 * field names (steps[i].field), so service errors land on the same inputs.
 */
export function validateSteps(steps: ProcedureStep[]): Errors {
  const errors: Errors = {};
  if (steps.length === 0) return { steps: "Add at least one step." };
  if (steps.length > MAX_STEPS) return { steps: `Use at most ${MAX_STEPS} steps.` };
  let prev = 0;
  const modes = new Map<string, string>();
  const typeAt = new Map<string, number>();
  steps.forEach((s, i) => {
    const key = `steps[${i}]`;
    if (s.conditionType !== "PRICE") {
      const first = typeAt.get(s.conditionType);
      if (first !== undefined) errors[`${key}.conditionType`] = `This condition type is already step ${first}; use each type once.`;
      else typeAt.set(s.conditionType, s.stepNo);
    }
    if (!Number.isInteger(s.stepNo) || s.stepNo < 10 || s.stepNo > 990) errors[`${key}.stepNo`] = "Use a step number from 10 to 990.";
    else if (s.stepNo <= prev) errors[`${key}.stepNo`] = "Step numbers must increase.";
    prev = Number.isInteger(s.stepNo) ? s.stepNo : prev;
    if (s.requirementCel.length > 512) errors[`${key}.requirementCel`] = "Use at most 512 characters.";
    if (s.accountKey.trim().length > 32) errors[`${key}.accountKey`] = "Use at most 32 characters.";
    const group = s.exclusionGroup.trim();
    if (group === "" && s.exclusionMode !== "") errors[`${key}.exclusionMode`] = "Choose a mode only with an exclusion group.";
    else if (group !== "") {
      if (group.length > 64) errors[`${key}.exclusionGroup`] = "Use at most 64 characters.";
      else if (s.conditionType === "PRICE") errors[`${key}.exclusionGroup`] = "The price step cannot be in an exclusion group.";
      else if (s.exclusionMode === "") errors[`${key}.exclusionMode`] = "Choose Best or First.";
      else if (modes.has(group) && modes.get(group) !== s.exclusionMode) errors[`${key}.exclusionMode`] = `Group ${group} already uses ${modes.get(group)}.`;
      else modes.set(group, s.exclusionMode);
    }
  });
  if (steps.filter((s) => s.conditionType === "PRICE").length !== 1 && !errors.steps) errors.steps = "Use exactly one Price step.";
  return errors;
}

/** Trims the free-text fields before sending. */
export function cleanSteps(steps: ProcedureStep[]): ProcedureStep[] {
  return steps.map((s) => ({ ...s, requirementCel: s.requirementCel.trim(), exclusionGroup: s.exclusionGroup.trim(), accountKey: s.accountKey.trim() }));
}

interface Props {
  idPrefix: string;
  steps: ProcedureStep[];
  errors: Errors;
  onChange(steps: ProcedureStep[]): void;
}

/** The step list of the Procedure designer (§5.26.6): add, edit and remove steps. */
export function ProcedureStepsEditor({ idPrefix, steps, errors, onChange }: Props) {
  function patch(i: number, change: Partial<ProcedureStep>) {
    onChange(steps.map((s, j) => (j === i ? { ...s, ...change } : s)));
  }
  function add() {
    const last = steps.length > 0 ? steps[steps.length - 1].stepNo : 0;
    // A condition type appears once per procedure, so a new step starts at the first type not used yet.
    const conditionType = PROCEDURE_STEP_TYPES.find((t) => t !== "PRICE" && !steps.some((s) => s.conditionType === t)) ?? "LINE_DISC";
    onChange([
      ...steps,
      { stepNo: Math.min(last + 10, 990), conditionType, requirementCel: "", exclusionGroup: "", exclusionMode: "", statistical: false, accountKey: "" },
    ]);
  }

  return (
    <>
      {steps.map((s, i) => {
        const id = `${idPrefix}-${i}`;
        const err = (field: string) => errors[`steps[${i}].${field}`];
        return (
          <fieldset key={i} className={styles.lineSet}>
            <legend className={styles.groupsLegend}>{`Step ${s.stepNo || i + 1}`}</legend>
            <FormField id={`${id}-no`} label="Step number" error={err("stepNo")}>
              {(c) => (
                <Input
                  {...c}
                  className={styles.mono}
                  inputMode="numeric"
                  value={Number.isNaN(s.stepNo) ? "" : String(s.stepNo)}
                  onChange={(e) => patch(i, { stepNo: e.target.value === "" ? Number.NaN : Number(e.target.value) })}
                  invalid={Boolean(err("stepNo"))}
                />
              )}
            </FormField>
            <FormField id={`${id}-type`} label="Condition type" error={err("conditionType")}>
              {(c) => (
                <Select {...c} value={s.conditionType} onChange={(e) => patch(i, { conditionType: e.target.value as ProcedureStepType })}>
                  {PROCEDURE_STEP_TYPES.map((t) => (
                    <option key={t} value={t}>
                      {STEP_TYPE_LABELS[t]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id={`${id}-req`} label="Requirement (CEL)" optional error={err("requirementCel")}>
              {(c) => (
                <Input {...c} className={styles.mono} value={s.requirementCel} onChange={(e) => patch(i, { requirementCel: e.target.value })} invalid={Boolean(err("requirementCel"))} />
              )}
            </FormField>
            <FormField id={`${id}-group`} label="Exclusion group" optional error={err("exclusionGroup")}>
              {(c) => (
                <Input {...c} className={styles.mono} value={s.exclusionGroup} onChange={(e) => patch(i, { exclusionGroup: e.target.value })} invalid={Boolean(err("exclusionGroup"))} />
              )}
            </FormField>
            <FormField id={`${id}-mode`} label="Exclusion mode" error={err("exclusionMode")}>
              {(c) => (
                <Select {...c} value={s.exclusionMode} onChange={(e) => patch(i, { exclusionMode: e.target.value as ExclusionMode })} invalid={Boolean(err("exclusionMode"))}>
                  <option value="">None</option>
                  <option value="BEST">Best (lowest net)</option>
                  <option value="FIRST">First (lowest step)</option>
                </Select>
              )}
            </FormField>
            <FormField id={`${id}-account`} label="Account key" optional error={err("accountKey")}>
              {(c) => <Input {...c} className={styles.mono} value={s.accountKey} onChange={(e) => patch(i, { accountKey: e.target.value })} invalid={Boolean(err("accountKey"))} />}
            </FormField>
            <span className={styles.check}>
              <Input id={`${id}-stat`} type="checkbox" checked={s.statistical} onChange={(e) => patch(i, { statistical: e.target.checked })} />
              <label htmlFor={`${id}-stat`}>Statistical</label>
            </span>
            {steps.length > 1 ? (
              <Button size="sm" variant="ghost" onClick={() => onChange(steps.filter((_, j) => j !== i))}>
                {`Remove step ${s.stepNo || i + 1}`}
              </Button>
            ) : null}
          </fieldset>
        );
      })}
      <div className={styles.formActions}>
        <Button variant="secondary" onClick={add} disabled={steps.length >= MAX_STEPS}>
          Add step
        </Button>
        {errors.steps ? (
          <span className={styles.note} role="alert">
            {errors.steps}
          </span>
        ) : (
          <span className={styles.note}>Steps run in step-number order. A statistical step is computed but not added to the net.</span>
        )}
      </div>
    </>
  );
}
