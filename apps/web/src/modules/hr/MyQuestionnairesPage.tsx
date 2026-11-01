import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Card, EmptyState, PageHeader, type DataTableColumn } from "../../shell/ui";
import { getSchedule, openSchedules, submitAnswers, type AnswerValue, type Question, type Schedule, type ScheduleView } from "./api/questionnaires";
import { DATE, formatUtc } from "./format";
import styles from "./pages.module.css";
import { getMyProfile } from "./api/selfService";
import { BackLink, CheckField, FormActions, ListPage, Loaded, SelectField, TextAreaField, TextField, useSubmit } from "./parts";
import { useLoad } from "./useLoad";

interface OpenRow extends Schedule {
  name?: string;
  anonymous?: boolean;
}

const columns: DataTableColumn<OpenRow>[] = [
  { key: "name", header: "Questionnaire", render: (row) => <Link to={`/hr/my/questionnaires/${row.id}`}>{row.name ?? "Questionnaire"}</Link> },
  { key: "closesAt", header: "Closes", mono: true, render: (row) => formatUtc(row.closesAt) },
  { key: "anonymous", header: "Responses", render: (row) => (row.anonymous ? <Badge tone="info">Anonymous</Badge> : "Named") },
];

/** Questionnaires open to the signed-in employee now. */
export function MyQuestionnairesPage() {
  return (
    <ListPage<OpenRow>
      title="My questionnaires"
      description="Questionnaires you've been invited to that are open now."
      label="Open questionnaires"
      noun="questionnaires"
      load={async (_cursor, signal) => {
        const schedules = await openSchedules(signal);
        const views = await Promise.allSettled(schedules.map((s) => getSchedule(s.id, signal)));
        return {
          items: schedules.map((s, i) => {
            const view = views[i];
            return view?.status === "fulfilled" ? { ...s, name: view.value.questionnaire.name, anonymous: view.value.questionnaire.anonymous } : s;
          }),
        };
      }}
      loadKey="open-schedules"
      columns={columns}
      getRowKey={(row) => row.id}
      empty={{ title: "Nothing to answer", description: "When you are invited to a questionnaire it appears here while it is open." }}
    />
  );
}

type Draft = Record<string, string | string[]>;

export function validateAnswers(questions: Question[], draft: Draft): { errors: Record<string, string>; answers?: Record<string, AnswerValue> } {
  const errors: Record<string, string> = {};
  const answers: Record<string, AnswerValue> = {};
  for (const q of questions) {
    const raw = draft[q.code];
    const empty = raw === undefined || raw === "" || (Array.isArray(raw) && raw.length === 0);
    if (empty) {
      if (q.mandatory) errors[q.code] = "This question is required.";
      continue;
    }
    if (q.type === "MULTI") answers[q.code] = raw as string[];
    else if (q.type === "NUMERIC") {
      const text = String(raw).trim();
      if (!/^-?\d{1,12}(\.\d{1,6})?$/.test(text)) errors[q.code] = "Enter a number.";
      else answers[q.code] = Number(text);
    } else if (q.type === "SCALE") answers[q.code] = Number(raw);
    else if (q.type === "DATE") {
      if (!DATE.test(String(raw))) errors[q.code] = "Choose a date.";
      else answers[q.code] = String(raw);
    } else if (q.type === "TEXT") {
      const text = String(raw).trim();
      if (text.length > 2000) errors[q.code] = "Use up to 2000 characters.";
      else answers[q.code] = text;
    } else answers[q.code] = String(raw);
  }
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, answers };
}

function QuestionField({ question, draft, error, onChange }: { question: Question; draft: Draft; error?: string; onChange(value: string | string[]): void }) {
  const id = `answer-${question.code}`;
  const label = `${question.text}${question.mandatory ? " (required)" : ""}`;
  const value = draft[question.code];
  if (question.type === "MULTI") {
    const chosen = Array.isArray(value) ? value : [];
    return (
      <fieldset className={styles.fieldset}>
        <legend className={styles.legend}>{label}</legend>
        {(question.options ?? []).map((option) => (
          <CheckField
            key={option.value}
            id={`${id}-${option.value}`}
            label={option.label}
            checked={chosen.includes(option.value)}
            onChange={(on) => onChange(on ? [...chosen, option.value] : chosen.filter((v) => v !== option.value))}
          />
        ))}
        {error ? <p role="alert">{error}</p> : null}
      </fieldset>
    );
  }
  const text = typeof value === "string" ? value : "";
  if (question.type === "SINGLE") {
    return <SelectField id={id} label={label} value={text} error={error} onChange={onChange} options={(question.options ?? []).map((o) => [o.value, o.label] as const)} placeholder="Choose" />;
  }
  if (question.type === "SCALE") {
    const scale = Array.from({ length: question.scaleMax ?? 5 }, (_, n) => [String(n + 1), String(n + 1)] as const);
    return <SelectField id={id} label={label} value={text} error={error} onChange={onChange} options={scale} placeholder="Choose" />;
  }
  if (question.type === "TEXT") return <TextAreaField id={id} label={label} value={text} error={error} maxLength={2000} onChange={onChange} />;
  return <TextField id={id} label={label} type={question.type === "DATE" ? "date" : "text"} inputMode={question.type === "NUMERIC" ? "decimal" : undefined} value={text} error={error} onChange={onChange} />;
}

function AnswerForm({ view, workerId }: { view: ScheduleView; workerId: string }) {
  const [draft, setDraft] = useState<Draft>({});
  const [done, setDone] = useState(false);
  const form = useSubmit({ "HCM-QST-ANSWERED": "form" });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors, answers } = validateAnswers(view.questionnaire.questions, draft);
    form.setErrors(errors);
    form.setFailure(null);
    if (!answers) return;
    await form.run(() => submitAnswers(view.id, workerId, answers), () => setDone(true));
  }

  if (done) {
    return (
      <Alert tone="success" title="Thank you, your answers were recorded" live="status">
        {view.questionnaire.anonymous ? "This questionnaire is anonymous: your answers can't be traced back to you." : "Your answers can't be changed once submitted."}
      </Alert>
    );
  }
  if (form.errors.form) {
    return (
      <Alert tone="info" title="Already answered" live="alert">
        {form.errors.form}
      </Alert>
    );
  }

  return (
    <form className={styles.stack} onSubmit={submit} noValidate aria-label={`Answer ${view.questionnaire.name}`}>
      {view.questionnaire.questions.map((q) => (
        <QuestionField
          key={q.code}
          question={q}
          draft={draft}
          error={form.errors[q.code] ?? form.errors[`answers.${q.code}`]}
          onChange={(value) => {
            setDraft({ ...draft, [q.code]: value });
            form.touch();
          }}
        />
      ))}
      <FormActions busy={form.busy} submit="Submit answers" failure={form.failure} failureTitle="Could not submit your answers" />
    </form>
  );
}

/** Answer one open questionnaire as yourself. Answers can't be changed once submitted. */
export function AnswerQuestionnairePage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getSchedule(id, signal), id);
  const me = useLoad((signal) => getMyProfile(signal), "answer-me");

  return (
    <Loaded state={state} title="Questionnaire" what="questionnaire" backTo="/hr/my/questionnaires">
      {(view) => (
        <div className={styles.stack}>
          <PageHeader
            title={view.questionnaire.name}
            description={`Open until ${formatUtc(view.closesAt)}.`}
            meta={view.questionnaire.anonymous ? <Badge tone="info">Anonymous</Badge> : undefined}
            actions={<BackLink to="/hr/my/questionnaires">Back to my questionnaires</BackLink>}
          />
          {view.status !== "OPEN" ? (
            <Alert tone="info" title={view.status === "CLOSED" ? "This questionnaire has closed" : "This questionnaire is not open yet"}>
              Answers are accepted only while it is open.
            </Alert>
          ) : me.error ? (
            me.error.code === "HCM-ESS-001" ? (
              <Card>
                <EmptyState headingLevel={3} title="No worker record is linked to your sign-in" description="Ask HR to link your sign-in to your worker record." />
              </Card>
            ) : (
              <Alert tone="danger" title="Could not load your worker record" live="alert">
                {me.error.message} ({me.error.code})
              </Alert>
            )
          ) : me.data ? (
            <Card>
              <AnswerForm view={view} workerId={me.data.id} />
            </Card>
          ) : (
            <Alert tone="info" live="status">
              Loading the questions
            </Alert>
          )}
        </div>
      )}
    </Loaded>
  );
}
