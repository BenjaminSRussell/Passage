import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { LookupFormField } from "../../shell/lookup/LookupFormField";
import { Badge, Button, buttonClassName, Card, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { createSchedule, getQuestionnaire, listQuestionnaires, type NewSchedule, type Question, type Questionnaire } from "./api/questionnaires";
import { UUID, utcFromLocalInput } from "./format";
import styles from "./pages.module.css";
import { BackLink, Entry, Facts, FormActions, humanize, ListPage, Loaded, TextAreaField, TextField, useSubmit } from "./parts";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Questionnaire>[] = [
  { key: "code", header: "Code", mono: true, render: (row) => <Link to={`/hr/questionnaires/${row.id}`}>{row.code}</Link> },
  { key: "name", header: "Name" },
  { key: "questions", header: "Questions", align: "end", render: (row) => String(row.questions.length) },
  { key: "anonymous", header: "Responses", render: (row) => (row.anonymous ? <Badge tone="info">Anonymous</Badge> : "Named") },
];

function OpenResults() {
  const navigate = useNavigate();
  const [scheduleId, setScheduleId] = useState("");
  const [error, setError] = useState<string | undefined>();
  return (
    <Card title="Open results for a schedule">
      <form
        className={styles.toolbar}
        noValidate
        aria-label="Open results for a schedule"
        onSubmit={(event) => {
          event.preventDefault();
          if (!UUID.test(scheduleId.trim())) {
            setError("Choose the schedule.");
            return;
          }
          navigate(`/hr/questionnaires/schedules/${scheduleId.trim()}/results`);
        }}
      >
        <LookupFormField id="results-schedule" label="Schedule" entity="questionnaireSchedule" hint="Every schedule, newest first, by its questionnaire and window." value={scheduleId} error={error} onChange={setScheduleId} />
        <Button type="submit">Open results</Button>
      </form>
    </Card>
  );
}

/** Questionnaires by code. Scheduling one to a population leads to its results. */
export function QuestionnairesPage() {
  return (
    <ListPage<Questionnaire>
      title="Questionnaires"
      description="Surveys and assessments. An anonymous questionnaire shows its results only after it closes and only for groups of five or more."
      label="Questionnaires"
      noun="questionnaires"
      load={async (_cursor, signal) => ({ items: await listQuestionnaires(signal) })}
      loadKey="questionnaires"
      columns={columns}
      getRowKey={(row) => row.id}
      empty={{ title: "No questionnaires", description: "Design a questionnaire, then schedule it to a population." }}
      actions={
        <Link className={buttonClassName("primary")} to="/hr/questionnaires/new">
          New questionnaire
        </Link>
      }
      banner={<OpenResults />}
    />
  );
}

const questionColumns: DataTableColumn<Question>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "text", header: "Question" },
  { key: "type", header: "Type", render: (row) => humanize(row.type) },
  { key: "mandatory", header: "Required", render: (row) => (row.mandatory ? "Yes" : "No") },
  { key: "resultGroup", header: "Result group", mono: true, render: (row) => row.resultGroup ?? "" },
];

export interface ScheduleValues {
  population: string;
  opensAt: string;
  closesAt: string;
}

export function validateSchedule(questionnaireId: string, v: ScheduleValues): { errors: Record<string, string>; input?: NewSchedule } {
  const errors: Record<string, string> = {};
  const opens = utcFromLocalInput(v.opensAt);
  const closes = utcFromLocalInput(v.closesAt);
  if (!opens) errors.opensAt = "Choose when it opens.";
  if (!closes) errors.closesAt = "Choose when it closes.";
  else if (opens && Date.parse(closes) <= Date.parse(opens)) errors.closesAt = "It must close after it opens.";
  if (v.population.trim().length > 500) errors.population = "Use up to 500 characters.";
  if (Object.keys(errors).length > 0 || !opens || !closes) return { errors };
  const population = v.population.trim();
  return { errors, input: { questionnaireId, opensAt: opens, closesAt: closes, ...(population ? { population } : {}) } };
}

function ScheduleForm({ questionnaire }: { questionnaire: Questionnaire }) {
  const navigate = useNavigate();
  const [values, setValues] = useState<ScheduleValues>({ population: "", opensAt: "", closesAt: "" });
  const form = useSubmit({ "HCM-QST-002": "population" });
  const set = (patch: Partial<ScheduleValues>) => {
    setValues({ ...values, ...patch });
    form.touch();
  };

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors, input } = validateSchedule(questionnaire.id, values);
    form.setErrors(errors);
    form.setFailure(null);
    if (!input) return;
    await form.run((key) => createSchedule(input, key), (schedule) => navigate(`/hr/questionnaires/schedules/${schedule.id}/results`));
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Schedule this questionnaire">
      <TextField id="schedule-opensAt" label="Opens" type="datetime-local" hint="Read as UTC." value={values.opensAt} error={form.errors.opensAt} onChange={(v) => set({ opensAt: v })} />
      <TextField id="schedule-closesAt" label="Closes" type="datetime-local" hint="Read as UTC." value={values.closesAt} error={form.errors.closesAt} onChange={(v) => set({ closesAt: v })} />
      <div className={styles.wide}>
        <TextAreaField
          id="schedule-population"
          label="Population"
          optional
          rows={2}
          hint="A rule over employment_type, fte, service_days and legal_entity. Empty means every worker with a primary employment."
          value={values.population}
          error={form.errors.population}
          onChange={(v) => set({ population: v })}
        />
      </div>
      <FormActions errors={form.errors} busy={form.busy} submit="Schedule" failure={form.failure} failureTitle="Could not schedule the questionnaire" />
    </form>
  );
}

export function QuestionnaireDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getQuestionnaire(id, signal), id);
  return (
    <Loaded state={state} title="Questionnaire" what="questionnaire" backTo="/hr/questionnaires">
      {(questionnaire) => (
        <div className={styles.stack}>
          <PageHeader
            title={questionnaire.name}
            description={`Questionnaire ${questionnaire.code}`}
            meta={questionnaire.anonymous ? <Badge tone="info">Anonymous</Badge> : undefined}
            actions={<BackLink to="/hr/questionnaires">Back to questionnaires</BackLink>}
          />
          <Card title="Questionnaire">
            <Facts>
              <Entry term="Responses">{questionnaire.anonymous ? "Anonymous: no one can see who answered what" : "Named"}</Entry>
              <Entry term="Questions" mono>
                {String(questionnaire.questions.length)}
              </Entry>
            </Facts>
          </Card>
          <Card title="Questions">
            <DataTable label="Questions" columns={questionColumns} rows={questionnaire.questions} getRowKey={(row) => row.code} empty={{ title: "No questions" }} />
          </Card>
          <Card title="Schedule">
            <ScheduleForm questionnaire={questionnaire} />
          </Card>
        </div>
      )}
    </Loaded>
  );
}
