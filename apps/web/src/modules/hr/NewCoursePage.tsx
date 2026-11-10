import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import {
  createCourse,
  describeProblem,
  listAllSkills,
  MAX_VALIDITY_MONTHS,
  newKey,
  SKILL_CODE,
  type NewCourse,
  type Problem,
  type Skill,
} from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export interface OutcomeValues {
  skillCode: string;
  rating: string;
}

export interface CourseValues {
  code: string;
  name: string;
  courseGroup: string;
  requiresManagerApproval: boolean;
  certificationCode: string;
  certificationName: string;
  validityMonths: string;
  outcomes: OutcomeValues[];
}

type Errors = Record<string, string>;
const MAX_OUTCOMES = 20;
const TEXT_FIELDS = ["code", "name", "courseGroup", "certificationCode", "certificationName", "validityMonths"] as const;

const blankOutcome = (): OutcomeValues => ({ skillCode: "", rating: "" });

/** The service's course rules (FR-42.14), and the request the form sends. Ratings must sit on each skill's scale. */
export function validateCourse(values: CourseValues, skills: Skill[]): { errors: Errors; input?: NewCourse } {
  const errors: Errors = {};
  const code = values.code.trim().toUpperCase();
  if (!SKILL_CODE.test(code)) errors.code = "Use 1 to 20 characters: A to Z, 0 to 9, a hyphen or an underscore.";
  const name = values.name.trim();
  if (name === "" || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  const group = values.courseGroup.trim();
  if (group.length > 60) errors.courseGroup = "Use up to 60 characters.";

  const seen = new Set<string>();
  const outcomes: NewCourse["skillOutcomes"] = [];
  values.outcomes.forEach((o, i) => {
    if (o.skillCode === "" && o.rating === "") return;
    const skill = skills.find((s) => s.code === o.skillCode);
    if (!skill) {
      errors[`skillOutcomes[${i}].skillCode`] = "Choose a skill.";
      return;
    }
    if (seen.has(skill.code)) {
      errors[`skillOutcomes[${i}].skillCode`] = "Each skill can appear once.";
      return;
    }
    seen.add(skill.code);
    const rating = Number(o.rating);
    if (!Number.isInteger(rating) || rating < 1 || rating > skill.scaleMax) {
      errors[`skillOutcomes[${i}].rating`] = `Choose a rating from 1 to ${skill.scaleMax}.`;
      return;
    }
    outcomes.push({ skillCode: skill.code, rating });
  });

  const certCode = values.certificationCode.trim().toUpperCase();
  const certName = values.certificationName.trim();
  if (certCode !== "" && !SKILL_CODE.test(certCode)) errors.certificationCode = "Use 1 to 20 characters: A to Z, 0 to 9, a hyphen or an underscore.";
  if ((certCode === "") !== (certName === "")) {
    if (certCode === "") errors.certificationCode = "Enter the certificate code, or clear its title.";
    else errors.certificationName = "Enter the certificate title, or clear its code.";
  }
  if (certName.length > 120) errors.certificationName = "Use up to 120 characters.";
  const validityRaw = values.validityMonths.trim();
  const validity = validityRaw === "" ? 0 : Number(validityRaw);
  if (!Number.isInteger(validity) || validity < 0 || validity > MAX_VALIDITY_MONTHS) {
    errors.validityMonths = `Enter 0 to ${MAX_VALIDITY_MONTHS} months; 0 means it does not expire.`;
  } else if (validity > 0 && certCode === "") {
    errors.validityMonths = "A validity needs a certificate.";
  }
  if (Object.keys(errors).length > 0) return { errors };

  const input: NewCourse = { code, name, skillOutcomes: outcomes, requiresManagerApproval: values.requiresManagerApproval };
  if (group) input.courseGroup = group;
  if (certCode) {
    input.certificationCode = certCode;
    input.certificationName = certName;
    input.validityMonths = validity;
  }
  return { errors, input };
}

/** Add a course with the skills a pass awards and the certificate it issues. */
export function NewCoursePage() {
  const navigate = useNavigate();
  const skills = useLoad((signal) => listAllSkills(signal), "course-skills");
  const [values, setValues] = useState<CourseValues>({
    code: "",
    name: "",
    courseGroup: "",
    requiresManagerApproval: false,
    certificationCode: "",
    certificationName: "",
    validityMonths: "",
    outcomes: [blankOutcome()],
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<CourseValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  function updateOutcome(i: number, patch: Partial<OutcomeValues>) {
    update({ outcomes: values.outcomes.map((o, j) => (j === i ? { ...o, ...patch } : o)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateCourse(values, skills.data ?? []);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const course = await createCourse(input, key.current);
      navigate(`/hr/courses/${course.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = { ...problem.fields };
      if (problem.code === "HCM-CRS-DUPLICATE") fieldErrors.code = "This course code is already in the catalog.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (name: (typeof TEXT_FIELDS)[number], label: string, hint?: string, extra?: Record<string, unknown>) => (
    <FormField id={`course-${name}`} label={label} hint={hint} error={errors[name]}>
      {(control) => (
        <Input {...control} {...extra} value={values[name]} invalid={Boolean(errors[name])} onChange={(event) => update({ [name]: event.target.value })} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New course"
        description="A pass raises each skill to the course rating when the worker is rated lower, and issues the certificate from the session's last day."
      />
      {skills.error ? (
        <Alert tone="danger" title="Could not load the skill catalog" live="alert" action={<Button size="sm" onClick={skills.reload}>Try again</Button>}>
          <ProblemMessage problem={skills.error} />
        </Alert>
      ) : null}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("code", "Code", "Unique in your tenant, for example FORK-101.", { maxLength: 20 })}
          {text("name", "Name", undefined, { maxLength: 120 })}
          {text("courseGroup", "Group", "Optional, for example Safety.", { maxLength: 60 })}
          <div className={styles.check}>
            <Input
              id="course-requiresManagerApproval"
              type="checkbox"
              checked={values.requiresManagerApproval}
              onChange={(event) => update({ requiresManagerApproval: event.target.checked })}
            />
            <label htmlFor="course-requiresManagerApproval">Registrations need a manager's approval before they take a seat</label>
          </div>
          <fieldset className={styles.fieldset}>
            <legend className={styles.legend}>Skills a pass awards</legend>
            {skills.data && skills.data.length === 0 ? (
              <p className={styles.note}>
                The skill catalog is empty. <Link to="/hr/skills/new">Add a skill</Link> first, or create the course without skills.
              </p>
            ) : null}
            {values.outcomes.map((o, i) => {
              const skill = skills.data?.find((s) => s.code === o.skillCode);
              const codeKey = `skillOutcomes[${i}].skillCode`;
              const ratingKey = `skillOutcomes[${i}].rating`;
              return (
                <div key={i} className={styles.optionRow} role="group" aria-label={`Skill ${i + 1}`}>
                  <FormField id={`course-outcome-${i}-skill`} label="Skill" error={errors[codeKey]}>
                    {(control) => (
                      <Select
                        {...control}
                        value={o.skillCode}
                        invalid={Boolean(errors[codeKey])}
                        disabled={!skills.data}
                        onChange={(event) => updateOutcome(i, { skillCode: event.target.value, rating: "" })}
                      >
                        <option value="">{skills.loading ? "Loading skills…" : "No skill"}</option>
                        {(skills.data ?? []).map((s) => (
                          <option key={s.id} value={s.code}>
                            {s.code} — {s.name}
                          </option>
                        ))}
                      </Select>
                    )}
                  </FormField>
                  <FormField id={`course-outcome-${i}-rating`} label="Rating on a pass" error={errors[ratingKey]}>
                    {(control) => (
                      <Select
                        {...control}
                        value={o.rating}
                        invalid={Boolean(errors[ratingKey])}
                        disabled={!skill}
                        onChange={(event) => updateOutcome(i, { rating: event.target.value })}
                      >
                        <option value="">Choose</option>
                        {Array.from({ length: skill?.scaleMax ?? 0 }, (_, n) => String(n + 1)).map((n) => (
                          <option key={n} value={n}>
                            {n} of {skill?.scaleMax}
                          </option>
                        ))}
                      </Select>
                    )}
                  </FormField>
                  {values.outcomes.length > 1 ? (
                    <div className={styles.optionRemove}>
                      <Button variant="ghost" size="sm" onClick={() => update({ outcomes: values.outcomes.filter((_, j) => j !== i) })} aria-label={`Remove skill ${i + 1}`}>
                        Remove
                      </Button>
                    </div>
                  ) : null}
                </div>
              );
            })}
            {values.outcomes.length < MAX_OUTCOMES ? (
              <div>
                <Button size="sm" onClick={() => update({ outcomes: [...values.outcomes, blankOutcome()] })}>
                  Add skill
                </Button>
              </div>
            ) : null}
          </fieldset>
          <fieldset className={styles.fieldset}>
            <legend className={styles.legend}>Certificate a pass issues</legend>
            <div className={styles.optionRow}>
              {text("certificationCode", "Certificate code", "Leave empty for no certificate.", { maxLength: 20 })}
              {text("certificationName", "Certificate title", undefined, { maxLength: 120 })}
              {text("validityMonths", "Valid for (months)", "0 or empty: does not expire.", { inputMode: "numeric" })}
            </div>
          </fieldset>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not add the course" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Add course
            </Button>
            <Link className={buttonClassName("ghost")} to="/hr/courses">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
