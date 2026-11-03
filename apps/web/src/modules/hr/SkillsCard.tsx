import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  listAllSkills,
  listPositions,
  listWorkerSkills,
  rateWorkerSkill,
  skillGap,
  skillGapForJob,
  type NewRating,
  type Problem,
  type Skill,
  type SkillGapRow,
  type WorkerSkill,
} from "./api/client";
import { listJobs } from "./api/personnel";
import { DATE, todayIso } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export interface RatingValues {
  skillCode: string;
  rating: string;
  assessedOn: string;
}
type RatingField = keyof RatingValues;
type RatingErrors = Partial<Record<RatingField, string>>;
const RATING_FIELDS: RatingField[] = ["skillCode", "rating", "assessedOn"];

/** The rating rules (FR-42.10) that need no database, and the request the form sends. */
export function validateRating(values: RatingValues, skills: Skill[], today: string = todayIso()): { errors: RatingErrors; input?: NewRating } {
  const errors: RatingErrors = {};
  const skill = skills.find((s) => s.code === values.skillCode);
  if (!skill) errors.skillCode = "Choose a skill.";
  const rating = Number(values.rating);
  if (!Number.isInteger(rating) || rating < 1 || (skill && rating > skill.scaleMax)) errors.rating = skill ? `Choose 1 to ${skill.scaleMax}.` : "Choose a rating.";
  if (!DATE.test(values.assessedOn)) errors.assessedOn = "Enter the day of the assessment.";
  else if (values.assessedOn > today) errors.assessedOn = "The assessment can't be in the future.";
  if (Object.keys(errors).length > 0 || !skill) return { errors };
  return { errors, input: { skillCode: skill.code, rating, assessedOn: values.assessedOn } };
}

const profileColumns: DataTableColumn<WorkerSkill>[] = [
  { key: "skillCode", header: "Skill", mono: true },
  { key: "skillName", header: "Name" },
  { key: "rating", header: "Rating", mono: true, align: "end", render: (row) => `${row.rating} of ${row.scaleMax}` },
  { key: "assessedOn", header: "Assessed", mono: true },
];

const gapColumns: DataTableColumn<SkillGapRow>[] = [
  { key: "skill", header: "Skill", mono: true },
  { key: "required", header: "Required", mono: true, align: "end" },
  { key: "actual", header: "Rating", mono: true, align: "end" },
  {
    key: "delta",
    header: "Gap",
    align: "end",
    render: (row) => (row.delta < 0 ? <Badge tone="danger">{row.delta}</Badge> : <Badge tone="success">{row.delta === 0 ? "Met" : `+${row.delta}`}</Badge>),
  },
];

function RateForm({ workerId, skills, onDone, onCancel }: { workerId: string; skills: Skill[]; onDone(r: WorkerSkill): void; onCancel(): void }) {
  const [values, setValues] = useState<RatingValues>({ skillCode: skills[0]?.code ?? "", rating: "", assessedOn: todayIso() });
  const [errors, setErrors] = useState<RatingErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const skill = skills.find((s) => s.code === values.skillCode);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateRating(values, skills);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      onDone(await rateWorkerSkill(workerId, input));
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: RatingErrors = {};
      for (const name of RATING_FIELDS) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.code === "HCM-SKL-STALE") fieldErrors.assessedOn = problem.message;
      if (problem.code === "HCM-SKL-RATING") fieldErrors.rating = problem.message;
      if (problem.code === "HCM-SKL-NOT-FOUND") fieldErrors.skillCode = problem.message;
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Rate a skill">
      <FormField id="rate-skillCode" label="Skill" error={errors.skillCode}>
        {(control) => (
          <Select {...control} value={values.skillCode} invalid={Boolean(errors.skillCode)} onChange={(event) => setValues({ ...values, skillCode: event.target.value, rating: "" })}>
            {skills.map((s) => (
              <option key={s.id} value={s.code}>
                {s.code} – {s.name}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="rate-rating" label="Rating" hint={skill ? `1 to ${skill.scaleMax}` : undefined} error={errors.rating}>
        {(control) => (
          <Select {...control} value={values.rating} invalid={Boolean(errors.rating)} onChange={(event) => setValues({ ...values, rating: event.target.value })}>
            <option value="">Choose…</option>
            {Array.from({ length: skill?.scaleMax ?? 0 }, (_, i) => String(i + 1)).map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="rate-assessedOn" label="Assessed on" error={errors.assessedOn}>
        {(control) => (
          <Input {...control} type="date" value={values.assessedOn} invalid={Boolean(errors.assessedOn)} onChange={(event) => setValues({ ...values, assessedOn: event.target.value })} />
        )}
      </FormField>
      {failure ? (
        <div className={styles.formActions}>
          <Alert tone="danger" title="Could not save the rating" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          Save rating
        </Button>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

/** The worker against one position's or job's skill profile (FR-42.18, AC-42.16). */
function GapSection({ workerId, kind }: { workerId: string; kind: "position" | "job" }) {
  const options = useLoad(
    async (signal) =>
      kind === "position"
        ? (await listPositions({}, signal)).items.map((p) => ({ id: p.id, label: `${p.positionNumber} (${p.jobCode})` }))
        : (await listJobs(undefined, signal)).items.map((j) => ({ id: j.id, label: `${j.code} – ${j.title}` })),
    `gap-${kind}s`,
  );
  const [targetId, setTargetId] = useState("");
  const gap = useLoad(
    (signal) => (targetId ? (kind === "position" ? skillGap(workerId, targetId, signal) : skillGapForJob(workerId, targetId, signal)) : Promise.resolve(null)),
    `${workerId}|${kind}|${targetId}`,
  );
  const fieldId = `gap-${kind}`;

  if (options.error) {
    return (
      <Alert tone="danger" title={`Could not load ${kind}s to compare with`} live="alert" action={<Button size="sm" onClick={options.reload}>Try again</Button>}>
        {options.error.message} ({options.error.code})
      </Alert>
    );
  }
  return (
    <div className={styles.stack}>
      <FormField id={fieldId} label={`Compare with ${kind}`} hint={`Lists every skill the ${kind} requires next to this worker's rating.`}>
        {(control) => (
          <Select {...control} value={targetId} disabled={options.loading} onChange={(event) => setTargetId(event.target.value)}>
            <option value="">{`Choose a ${kind}…`}</option>
            {(options.data ?? []).map((o) => (
              <option key={o.id} value={o.id}>
                {o.label}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      {!targetId ? null : gap.error ? (
        <Alert tone="danger" title="Could not compare" live="alert" action={<Button size="sm" onClick={gap.reload}>Try again</Button>}>
          {gap.error.message} ({gap.error.code})
        </Alert>
      ) : (
        <>
          {gap.data && !gap.loading ? (
            gap.data.met ? (
              <Badge tone="success">{gap.data.comparison.length === 0 ? `The ${kind} requires no skills` : "Meets every requirement"}</Badge>
            ) : (
              <Badge tone="danger">{`${gap.data.gaps.length} skill${gap.data.gaps.length === 1 ? "" : "s"} short`}</Badge>
            )
          ) : null}
          <DataTable
            label={kind === "position" ? "Skill gap" : "Job skill gap"}
            columns={gapColumns}
            rows={gap.data?.comparison ?? []}
            getRowKey={(row) => row.skill}
            loading={gap.loading}
            empty={{ title: "No requirements", description: `Set the skills this ${kind} needs on the ${kind}'s page.` }}
          />
        </>
      )}
    </div>
  );
}

/** The worker's skill profile with a rating form and the gap against a position (FR-42.10, FR-42.18). */
export function SkillsCard({ workerId }: { workerId: string }) {
  const profile = useLoad((signal) => listWorkerSkills(workerId, signal), `skills|${workerId}`);
  const catalog = useLoad((signal) => listAllSkills(signal), "skill-catalog");
  const [rating, setRating] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const skills = catalog.data ?? [];

  return (
    <Card
      title="Skills"
      actions={
        rating || catalog.loading || skills.length === 0 ? null : (
          <Button size="sm" onClick={() => setRating(true)}>
            Rate a skill
          </Button>
        )
      }
    >
      <div className={styles.stack}>
        <Alert tone="success" live="status">
          {notice}
        </Alert>
        {catalog.error ? (
          <Alert tone="danger" title="Could not load the skill catalog" live="alert" action={<Button size="sm" onClick={catalog.reload}>Try again</Button>}>
            {catalog.error.message} ({catalog.error.code})
          </Alert>
        ) : null}
        {!catalog.loading && !catalog.error && skills.length === 0 ? <p className={styles.note}>The skill catalog is empty. Add skills before rating workers.</p> : null}
        {rating ? (
          <RateForm
            workerId={workerId}
            skills={skills}
            onCancel={() => setRating(false)}
            onDone={(r) => {
              setRating(false);
              setNotice(`${r.skillCode} rated ${r.rating} of ${r.scaleMax}.`);
              profile.reload();
            }}
          />
        ) : null}
        {profile.error ? (
          <Alert tone="danger" title="Could not load the skill profile" live="alert" action={<Button size="sm" onClick={profile.reload}>Try again</Button>}>
            {profile.error.message} ({profile.error.code})
          </Alert>
        ) : profile.loading && !profile.data ? (
          <Skeleton width="3/4" />
        ) : (
          <DataTable
            label="Skill profile"
            columns={profileColumns}
            rows={profile.data ?? []}
            getRowKey={(row) => row.skillId}
            empty={{ title: "Not rated yet", description: "Rate the worker on a skill from the catalog." }}
          />
        )}
        <GapSection workerId={workerId} kind="position" />
        <GapSection workerId={workerId} kind="job" />
      </div>
    </Card>
  );
}
