import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, type DataTableColumn } from "../../shell/ui";
import {
  GOAL_DESCRIPTION_MAX,
  GOAL_TITLE_MAX,
  MAX_RATING,
  MIN_RATING,
  REVIEW_TEXT_MAX,
  createGoal,
  type Goal,
  type GoalProgress,
  type GoalStatus,
  type NewGoal,
  type NewReview,
  type Review,
  type SelfAssessment,
  type SignOff,
} from "./api/performance";
import { DATE } from "./format";
import styles from "./pages.module.css";
import { FormActions, SelectField, Status, TextAreaField, TextField, opt, useSubmit } from "./parts";

type Errors<K extends string> = Partial<Record<K, string>>;
export interface Checked<I, K extends string> {
  errors: Errors<K>;
  input?: I;
}

const result = <I, K extends string>(errors: Errors<K>, input: I): Checked<I, K> => (Object.keys(errors).length > 0 ? { errors } : { errors, input });

export interface GoalValues {
  title: string;
  description: string;
  dueOn: string;
}

/** The goal rules the service checks, so a bad form never leaves the browser. */
export function validateGoal(values: GoalValues): Checked<NewGoal, keyof GoalValues> {
  const errors: Errors<keyof GoalValues> = {};
  const title = values.title.trim();
  if (title === "" || [...title].length > GOAL_TITLE_MAX) errors.title = `Enter a title of up to ${GOAL_TITLE_MAX} characters.`;
  if ([...values.description.trim()].length > GOAL_DESCRIPTION_MAX) errors.description = `Use at most ${GOAL_DESCRIPTION_MAX} characters.`;
  if (values.dueOn !== "" && !DATE.test(values.dueOn)) errors.dueOn = "Enter a date.";
  return result(errors, { title, description: opt(values.description), dueOn: opt(values.dueOn) });
}

export interface ProgressValues {
  progress: string;
  status: GoalStatus;
}

export function validateProgress(values: ProgressValues): Checked<GoalProgress, keyof ProgressValues> {
  const errors: Errors<keyof ProgressValues> = {};
  const n = Number(values.progress);
  if (values.progress.trim() === "" || !Number.isInteger(n) || n < 0 || n > 100) errors.progress = "Enter a whole number from 0 to 100.";
  else if (values.status === "ACHIEVED" && n !== 100) errors.progress = "An achieved goal must be at 100 percent.";
  return result(errors, { status: values.status, progressPercent: n });
}

export function validateSelfAssessment(values: { achievements: string; development: string }): Checked<SelfAssessment, "achievements" | "development"> {
  const errors: Errors<"achievements" | "development"> = {};
  const achievements = values.achievements.trim();
  if (achievements === "" || [...achievements].length > REVIEW_TEXT_MAX) errors.achievements = `Describe your achievements in up to ${REVIEW_TEXT_MAX} characters.`;
  if ([...values.development.trim()].length > REVIEW_TEXT_MAX) errors.development = `Use at most ${REVIEW_TEXT_MAX} characters.`;
  return result(errors, { achievements, development: opt(values.development) });
}

export function validateSignOff(values: { comments: string; rating: string }): Checked<SignOff, "comments" | "rating"> {
  const errors: Errors<"comments" | "rating"> = {};
  if ([...values.comments.trim()].length > REVIEW_TEXT_MAX) errors.comments = `Use at most ${REVIEW_TEXT_MAX} characters.`;
  const rating = values.rating === "" ? undefined : Number(values.rating);
  if (rating !== undefined && (!Number.isInteger(rating) || rating < MIN_RATING || rating > MAX_RATING)) errors.rating = `Choose ${MIN_RATING} to ${MAX_RATING}.`;
  return result(errors, { managerComments: opt(values.comments), overallRating: rating });
}

export function validatePeriod(values: { periodStart: string; periodEnd: string }): Checked<NewReview, "periodStart" | "periodEnd"> {
  const errors: Errors<"periodStart" | "periodEnd"> = {};
  if (!DATE.test(values.periodStart)) errors.periodStart = "Enter the first day of the period.";
  if (!DATE.test(values.periodEnd)) errors.periodEnd = "Enter the last day of the period.";
  else if (DATE.test(values.periodStart) && values.periodEnd < values.periodStart) errors.periodEnd = "The period can't end before it starts.";
  return result(errors, { periodStart: values.periodStart, periodEnd: values.periodEnd });
}

export const goalColumns: DataTableColumn<Goal>[] = [
  { key: "title", header: "Goal" },
  { key: "dueOn", header: "Due", mono: true, render: (row) => row.dueOn ?? "–" },
  { key: "progressPercent", header: "Progress", mono: true, align: "end", render: (row) => `${row.progressPercent}%` },
  { key: "status", header: "Status", render: (row) => <Status value={row.status} /> },
];

export const reviewStatusLabel: Record<Review["status"], string> = {
  draft: "Waiting for the self-assessment",
  self_submitted: "Waiting for the manager",
  signed_off: "Signed off",
};

export function ReviewStatus({ review }: { review: Review }) {
  const tone = review.status === "signed_off" ? "success" : review.status === "self_submitted" ? "info" : "neutral";
  return <Badge tone={tone}>{reviewStatusLabel[review.status]}</Badge>;
}

/** Report progress on a goal. A manager may also cancel; an employee can only mark OPEN or ACHIEVED. */
export function GoalProgressForm({ goals, save, canCancel, onDone }: { goals: Goal[]; save(goalId: string, input: GoalProgress): Promise<Goal>; canCancel: boolean; onDone(goal: Goal): void }) {
  const editable = goals.filter((g) => g.status !== "CANCELLED");
  const [goalId, setGoalId] = useState("");
  const [values, setValues] = useState<ProgressValues>({ progress: "", status: "OPEN" });
  const [errors, setErrors] = useState<Errors<keyof ProgressValues | "goal">>({});
  const form = useSubmit();

  function choose(next: string) {
    setGoalId(next);
    const goal = editable.find((g) => g.id === next);
    if (goal) setValues({ progress: String(goal.progressPercent), status: goal.status });
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateProgress(values);
    const all: Errors<keyof ProgressValues | "goal"> = { ...found, ...(goalId === "" ? { goal: "Choose a goal." } : {}) };
    setErrors(all);
    if (!input || goalId === "") return;
    void form.run(
      () => save(goalId, input),
      (goal) => {
        setGoalId("");
        onDone(goal);
      },
    );
  }

  const statuses: Array<readonly [string, string]> = [["OPEN", "Open"], ["ACHIEVED", "Achieved"], ...(canCancel ? ([["CANCELLED", "Cancelled"]] as const) : [])];
  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Update goal progress">
      <SelectField
        id="progress-goal"
        label="Goal"
        value={goalId}
        onChange={choose}
        placeholder="Choose a goal…"
        options={editable.map((g) => [g.id, g.title] as const)}
        error={errors.goal}
      />
      <TextField id="progress-percent" label="Progress (percent)" type="number" min={0} max={100} value={values.progress} error={errors.progress ?? form.errors.progressPercent} onChange={(progress) => setValues({ ...values, progress })} />
      <SelectField id="progress-status" label="Status" value={values.status} onChange={(status) => setValues({ ...values, status: status as GoalStatus })} options={statuses} error={form.errors.status} />
      <FormActions busy={form.busy} submit="Save progress" failure={form.failure} failureTitle="Could not save the progress" errors={form.errors} />
    </form>
  );
}

/** Add a goal for a worker (their manager sets it). */
export function NewGoalForm({ workerId, onDone, onCancel }: { workerId: string; onDone(goal: Goal): void; onCancel(): void }) {
  const [values, setValues] = useState<GoalValues>({ title: "", description: "", dueOn: "" });
  const [errors, setErrors] = useState<Errors<keyof GoalValues>>({});
  const form = useSubmit();

  function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateGoal(values);
    setErrors(found);
    if (!input) return;
    void form.run((key) => createGoal(workerId, input, key), onDone);
  }
  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="New goal" onChange={form.touch}>
      <TextField id="goal-title" label="Title" value={values.title} error={errors.title ?? form.errors.title} onChange={(title) => setValues({ ...values, title })} />
      <TextAreaField id="goal-description" label="Description" optional value={values.description} error={errors.description ?? form.errors.description} onChange={(description) => setValues({ ...values, description })} />
      <TextField id="goal-dueOn" label="Due on" type="date" optional value={values.dueOn} error={errors.dueOn ?? form.errors.dueOn} onChange={(dueOn) => setValues({ ...values, dueOn })} />
      <FormActions busy={form.busy} submit="Add goal" failure={form.failure} failureTitle="Could not add the goal" errors={form.errors} />
      <div className={styles.formActions}>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

export function GoalsTable({ goals, label = "Goals" }: { goals: Goal[]; label?: string }) {
  return (
    <DataTable
      label={label}
      columns={goalColumns}
      rows={goals}
      getRowKey={(row) => row.id}
      empty={{ title: "No goals yet", description: "Goals set by your manager appear here." }}
    />
  );
}

export function Notice({ children }: { children: string | null }) {
  return (
    <Alert tone="success" live="status">
      {children}
    </Alert>
  );
}

export function ReviewText({ title, children }: { title: string; children?: string }) {
  return (
    <Card title={title}>
      <p className={styles.note}>{children ?? "Not given."}</p>
    </Card>
  );
}
