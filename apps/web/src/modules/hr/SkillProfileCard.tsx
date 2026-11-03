import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  listAllSkills,
  listJobSkillRequirements,
  listSkillRequirements,
  removeJobSkillRequirement,
  removeSkillRequirement,
  setJobSkillRequirement,
  setSkillRequirement,
  type Problem,
  type SkillRequirement,
} from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Row = Pick<SkillRequirement, "skillId" | "skillCode" | "skillName" | "scaleMax" | "minRating">;
type Subject = { positionId: string; jobId?: undefined } | { jobId: string; positionId?: undefined };

/** The skills a position or a job requires at a minimum rating (FR-42.18), with add, change and remove. */
export function SkillProfileCard(props: Subject) {
  const job = props.jobId !== undefined;
  const subjectId = props.jobId ?? props.positionId;
  const noun = job ? "job" : "position";
  const load = job ? listJobSkillRequirements : listSkillRequirements;
  const set = job ? setJobSkillRequirement : setSkillRequirement;
  const unset = job ? removeJobSkillRequirement : removeSkillRequirement;
  const state = useLoad((signal): Promise<Row[]> => load(subjectId, signal), `requirements|${noun}|${subjectId}`);
  const catalog = useLoad((signal) => listAllSkills(signal), "skill-catalog");
  const [skillCode, setSkillCode] = useState("");
  const [minRating, setMinRating] = useState("");
  const [errors, setErrors] = useState<{ skillCode?: string; minRating?: string }>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const skills = catalog.data ?? [];
  const skill = skills.find((s) => s.code === skillCode);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: typeof errors = {};
    if (!skill) found.skillCode = "Choose a skill.";
    const n = Number(minRating);
    if (!Number.isInteger(n) || n < 1 || (skill && n > skill.scaleMax)) found.minRating = skill ? `Choose 1 to ${skill.scaleMax}.` : "Choose a rating.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0 || !skill) return;
    setBusy("save");
    try {
      const saved = await set(subjectId, skill.code, n);
      setNotice(`${saved.skillCode} now requires at least ${saved.minRating}.`);
      setSkillCode("");
      setMinRating("");
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "HCM-SKL-RATING" || problem.fields.minRating) setErrors({ minRating: problem.fields.minRating ?? problem.message });
      else if (problem.code === "HCM-SKL-NOT-FOUND") setErrors({ skillCode: problem.message });
      else setFailure(problem);
    } finally {
      setBusy(null);
    }
  }

  async function remove(row: Row) {
    setBusy(row.skillCode);
    setFailure(null);
    try {
      await unset(subjectId, row.skillCode);
      setNotice(`${row.skillCode} is no longer required.`);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const columns: DataTableColumn<Row>[] = [
    { key: "skillCode", header: "Skill", mono: true },
    { key: "skillName", header: "Name" },
    { key: "minRating", header: "Minimum", mono: true, align: "end", render: (row) => `${row.minRating} of ${row.scaleMax}` },
    {
      key: "actions",
      header: "Actions",
      render: (row) => (
        <Button size="sm" variant="ghost" loading={busy === row.skillCode} onClick={() => remove(row)} aria-label={`Remove ${row.skillCode}`}>
          Remove
        </Button>
      ),
    },
  ];

  return (
    <Card title="Skill profile">
      <div className={styles.stack}>
        <Alert tone="success" live="status">
          {notice}
        </Alert>
        {failure ? (
          <Alert tone="danger" title="Could not change the skill profile" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        {state.error ? (
          <Alert tone="danger" title="Could not load the skill profile" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        ) : state.loading && !state.data ? (
          <Skeleton width="3/4" />
        ) : (
          <DataTable
            label="Required skills"
            columns={columns}
            rows={state.data ?? []}
            getRowKey={(row) => row.skillId}
            empty={{ title: "No skills required", description: `Add the skills this ${noun} needs; gap analysis compares workers with them.` }}
          />
        )}
        {catalog.error ? (
          <Alert tone="danger" title="Could not load the skill catalog" live="alert" action={<Button size="sm" onClick={catalog.reload}>Try again</Button>}>
            {catalog.error.message} ({catalog.error.code})
          </Alert>
        ) : (
          <form className={styles.form} onSubmit={submit} noValidate aria-label="Require a skill">
            <FormField id="req-skillCode" label="Skill" hint="Choosing a skill already listed changes its minimum." error={errors.skillCode}>
              {(control) => (
                <Select
                  {...control}
                  value={skillCode}
                  disabled={catalog.loading}
                  invalid={Boolean(errors.skillCode)}
                  onChange={(event) => {
                    setSkillCode(event.target.value);
                    setMinRating("");
                  }}
                >
                  <option value="">Choose…</option>
                  {skills.map((s) => (
                    <option key={s.id} value={s.code}>
                      {s.code} – {s.name}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="req-minRating" label="Minimum rating" error={errors.minRating}>
              {(control) => (
                <Select {...control} value={minRating} invalid={Boolean(errors.minRating)} onChange={(event) => setMinRating(event.target.value)}>
                  <option value="">Choose…</option>
                  {Array.from({ length: skill?.scaleMax ?? 0 }, (_, i) => String(i + 1)).map((v) => (
                    <option key={v} value={v}>
                      {v}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "save"}>
                Save requirement
              </Button>
            </div>
          </form>
        )}
      </div>
    </Card>
  );
}
