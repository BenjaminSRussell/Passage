import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { Button, Card, PageHeader } from "../../shell/ui";
import { QUESTION_TYPES, createQuestionnaire, type NewQuestionnaire, type QuestionOption, type QuestionType } from "./api/questionnaires";
import styles from "./pages.module.css";
import { CheckField, FormActions, labelled, SelectField, TextAreaField, TextField, useSubmit } from "./parts";

const CODE = /^[A-Z0-9][A-Z0-9_-]{0,19}$/;
const MAX_QUESTIONS = 100;

export interface QuestionValues {
  code: string;
  text: string;
  type: string;
  options: string;
  scaleMax: string;
  weight: string;
  mandatory: boolean;
  resultGroup: string;
}

export interface QuestionnaireValues {
  code: string;
  name: string;
  anonymous: boolean;
  questions: QuestionValues[];
}

const blankQuestion = (): QuestionValues => ({ code: "", text: "", type: "SCALE", options: "", scaleMax: "5", weight: "1", mandatory: false, resultGroup: "" });

export function parseOptions(raw: string): { options: QuestionOption[]; error?: string } {
  const options: QuestionOption[] = [];
  const seen = new Set<string>();
  for (const [n, line] of raw.split("\n").map((l) => l.trim()).filter((l) => l !== "").entries()) {
    const [value = "", label = "", score] = line.split(",").map((part) => part.trim());
    const code = value.toUpperCase();
    if (!CODE.test(code) || seen.has(code) || label === "" || label.length > 120) return { options, error: `Option ${n + 1}: use a unique value, a comma, then a label of up to 120 characters.` };
    if (score !== undefined && score !== "" && !/^-?\d{1,8}(\.\d{1,4})?$/.test(score)) return { options, error: `Option ${n + 1}: the score must be a number.` };
    seen.add(code);
    options.push(score ? { value: code, label, score } : { value: code, label });
  }
  if (options.length < 2 || options.length > 20) return { options, error: "Give 2 to 20 options, one per line." };
  return { options };
}

/** The service's questionnaire design rules (FR-42.15), and the request the form sends. */
export function validateQuestionnaire(v: QuestionnaireValues): { errors: Record<string, string>; input?: NewQuestionnaire } {
  const errors: Record<string, string> = {};
  const code = v.code.trim().toUpperCase();
  if (!CODE.test(code)) errors.code = "Use 1 to 20 characters: A to Z, 0 to 9, a hyphen or an underscore.";
  const name = v.name.trim();
  if (name === "" || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (v.questions.length === 0) errors.questions = "Add at least one question.";

  const seen = new Set<string>();
  const questions: NewQuestionnaire["questions"] = [];
  v.questions.forEach((q, i) => {
    const at = (field: string) => `questions[${i}].${field}`;
    const qCode = q.code.trim().toUpperCase();
    if (!CODE.test(qCode) || seen.has(qCode)) errors[at("code")] = "Use a unique code of 1 to 20 characters (A to Z, 0 to 9, hyphen, underscore).";
    seen.add(qCode);
    const text = q.text.trim();
    if (text === "" || text.length > 500) errors[at("text")] = "Enter the question, up to 500 characters.";
    const weight = q.weight.trim() === "" ? "1" : q.weight.trim();
    if (!/^\d{1,3}(\.\d{1,4})?$/.test(weight) || Number(weight) <= 0 || Number(weight) > 100) errors[at("weight")] = "Enter a number above 0 and up to 100.";
    const group = q.resultGroup.trim().toUpperCase();
    if (group !== "" && !CODE.test(group)) errors[at("resultGroup")] = "Use 1 to 20 characters: A to Z, 0 to 9, a hyphen or an underscore.";
    const built: NewQuestionnaire["questions"][number] = { code: qCode, text, type: q.type as QuestionType, weight, mandatory: q.mandatory };
    if (group) built.resultGroup = group;
    if (q.type === "SINGLE" || q.type === "MULTI") {
      const parsed = parseOptions(q.options);
      if (parsed.error) errors[at("options")] = parsed.error;
      built.options = parsed.options;
    }
    if (q.type === "SCALE") {
      const scale = Number(q.scaleMax);
      if (!Number.isInteger(scale) || scale < 2 || scale > 10) errors[at("scaleMax")] = "Enter a whole number from 2 to 10.";
      built.scaleMax = scale;
    }
    questions.push(built);
  });
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { code, name, anonymous: v.anonymous, questions } };
}

export function NewQuestionnairePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<QuestionnaireValues>({ code: "", name: "", anonymous: false, questions: [blankQuestion()] });
  const form = useSubmit({ "HCM-QST-DUPLICATE": "code" });
  const set = (patch: Partial<QuestionnaireValues>) => {
    setValues({ ...values, ...patch });
    form.touch();
  };
  const setQuestion = (i: number, patch: Partial<QuestionValues>) => set({ questions: values.questions.map((q, j) => (j === i ? { ...q, ...patch } : q)) });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors, input } = validateQuestionnaire(values);
    form.setErrors(errors);
    form.setFailure(null);
    if (!input) return;
    await form.run((key) => createQuestionnaire(input, key), (created) => navigate(`/hr/questionnaires/${created.id}`));
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New questionnaire" description="Design the questions now; schedule it to a population from its page." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <TextField id="questionnaire-code" label="Code" value={values.code} error={form.errors.code} onChange={(v) => set({ code: v })} />
          <TextField id="questionnaire-name" label="Name" value={values.name} error={form.errors.name} onChange={(v) => set({ name: v })} />
          <CheckField id="questionnaire-anonymous" label="Anonymous: results show only groups of five or more, after it closes" checked={values.anonymous} onChange={(anonymous) => set({ anonymous })} />
          <fieldset className={styles.fieldset}>
            <legend className={styles.legend}>Questions</legend>
            {values.questions.map((q, i) => {
              const err = (field: string) => form.errors[`questions[${i}].${field}`];
              return (
                <div key={i} className={styles.optionRow} role="group" aria-label={`Question ${i + 1}`}>
                  <TextField id={`question-${i}-code`} label="Code" value={q.code} error={err("code")} onChange={(v) => setQuestion(i, { code: v })} />
                  <TextField id={`question-${i}-text`} label="Question" value={q.text} error={err("text")} onChange={(v) => setQuestion(i, { text: v })} />
                  <SelectField id={`question-${i}-type`} label="Type" value={q.type} error={err("type")} onChange={(v) => setQuestion(i, { type: v })} options={labelled(QUESTION_TYPES)} />
                  {q.type === "SCALE" ? (
                    <TextField id={`question-${i}-scale`} label="Scale up to" inputMode="numeric" value={q.scaleMax} error={err("scaleMax")} onChange={(v) => setQuestion(i, { scaleMax: v })} />
                  ) : null}
                  {q.type === "SINGLE" || q.type === "MULTI" ? (
                    <TextAreaField
                      id={`question-${i}-options`}
                      label="Options"
                      hint="One per line: value, label, and an optional score."
                      value={q.options}
                      error={err("options") ?? Object.entries(form.errors).find(([key]) => key.startsWith(`questions[${i}].options[`))?.[1]}
                      onChange={(v) => setQuestion(i, { options: v })}
                    />
                  ) : null}
                  <TextField id={`question-${i}-weight`} label="Weight" value={q.weight} error={err("weight")} onChange={(v) => setQuestion(i, { weight: v })} />
                  <TextField id={`question-${i}-group`} label="Result group" optional value={q.resultGroup} error={err("resultGroup")} onChange={(v) => setQuestion(i, { resultGroup: v })} />
                  <CheckField id={`question-${i}-mandatory`} label="Required" checked={q.mandatory} onChange={(mandatory) => setQuestion(i, { mandatory })} />
                  {values.questions.length > 1 ? (
                    <div className={styles.optionRemove}>
                      <Button variant="ghost" size="sm" aria-label={`Remove question ${i + 1}`} onClick={() => set({ questions: values.questions.filter((_, j) => j !== i) })}>
                        Remove
                      </Button>
                    </div>
                  ) : null}
                </div>
              );
            })}
            {form.errors.questions ? <p role="alert">{form.errors.questions}</p> : null}
            {values.questions.length < MAX_QUESTIONS ? (
              <div>
                <Button size="sm" onClick={() => set({ questions: [...values.questions, blankQuestion()] })}>
                  Add question
                </Button>
              </div>
            ) : null}
          </fieldset>
          <FormActions errors={form.errors} busy={form.busy} submit="Create questionnaire" cancelTo="/hr/questionnaires" failure={form.failure} failureTitle="Could not create the questionnaire" />
        </form>
      </Card>
    </div>
  );
}
