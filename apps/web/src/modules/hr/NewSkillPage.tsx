import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createSkill, describeProblem, MAX_SKILL_SCALE, newKey, SKILL_CODE, type NewSkill, type Problem } from "./api/client";
import styles from "./pages.module.css";

export interface SkillValues {
  code: string;
  name: string;
  scaleMax: string;
}
type Field = keyof SkillValues;
type Errors = Partial<Record<Field, string>>;
const FIELDS: Field[] = ["code", "name", "scaleMax"];

/** The service's catalog rules (FR-42.10), and the request the form sends. */
export function validateSkill(values: SkillValues): { errors: Errors; input?: NewSkill } {
  const errors: Errors = {};
  const code = values.code.trim().toUpperCase();
  if (!SKILL_CODE.test(code)) errors.code = "Use 1 to 20 characters: A to Z, 0 to 9, a hyphen or an underscore.";
  const name = values.name.trim();
  if (name === "" || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  const scale = Number(values.scaleMax);
  if (!Number.isInteger(scale) || scale < 1 || scale > MAX_SKILL_SCALE) errors.scaleMax = `Choose a scale from 1 to ${MAX_SKILL_SCALE}.`;
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { code, name, scaleMax: scale } };
}

/** Add a skill to the catalog with its rating scale. */
export function NewSkillPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<SkillValues>({ code: "", name: "", scaleMax: "5" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<SkillValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateSkill(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createSkill(input, key.current);
      navigate("/hr/skills");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of FIELDS) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.code === "HCM-SKL-DUPLICATE") fieldErrors.code = "This skill code is already in the catalog.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New skill" description="A rating runs from 1 to the scale. A worker who is not rated on a skill counts as 0 in gap analysis." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="skill-code" label="Code" hint="Unique in your tenant, for example WELDING." error={errors.code}>
            {(control) => (
              <Input {...control} maxLength={20} value={values.code} invalid={Boolean(errors.code)} onChange={(event) => update({ code: event.target.value })} />
            )}
          </FormField>
          <FormField id="skill-name" label="Name" error={errors.name}>
            {(control) => (
              <Input {...control} maxLength={120} value={values.name} invalid={Boolean(errors.name)} onChange={(event) => update({ name: event.target.value })} />
            )}
          </FormField>
          <FormField id="skill-scaleMax" label="Rating scale" hint="The highest rating." error={errors.scaleMax}>
            {(control) => (
              <Select {...control} value={values.scaleMax} invalid={Boolean(errors.scaleMax)} onChange={(event) => update({ scaleMax: event.target.value })}>
                {Array.from({ length: MAX_SKILL_SCALE }, (_, i) => String(i + 1)).map((n) => (
                  <option key={n} value={n}>
                    1 to {n}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not add the skill" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Add skill
            </Button>
            <Link className={buttonClassName("ghost")} to="/hr/skills">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
