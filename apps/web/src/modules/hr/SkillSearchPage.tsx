import { useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { MAX_SKILL_SCALE, searchSkills, SKILL_CODE, type SkillGapRow, type SkillMatch, type SkillSearchQuery } from "./api/client";
import { DATE, todayIso } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export interface SearchValues {
  skills: string;
  minRating: string;
  certs: string;
  asOf: string;
}
type Field = keyof SearchValues;
type Errors = Partial<Record<Field, string>>;

const TERM = /^([A-Z0-9][A-Z0-9_-]{0,19})(:(\d{1,2}))?$/;

const split = (raw: string) =>
  raw
    .split(",")
    .map((t) => t.trim().toUpperCase())
    .filter((t) => t !== "");

/** The service's search rules (FR-42.18) checked before the call: terms are CODE or CODE:min, at most 20. */
export function validateSearch(values: SearchValues): { errors: Errors; query?: SkillSearchQuery } {
  const errors: Errors = {};
  const terms = split(values.skills);
  const certs = split(values.certs);
  const badTerm = terms.find((t) => {
    const m = TERM.exec(t);
    return !m || (m[3] !== undefined && (Number(m[3]) < 1 || Number(m[3]) > MAX_SKILL_SCALE));
  });
  if (badTerm) errors.skills = `"${badTerm}" isn't CODE or CODE:rating with a rating from 1 to ${MAX_SKILL_SCALE}.`;
  else if (terms.length > 20) errors.skills = "Search on at most 20 skills.";
  const min = values.minRating.trim();
  if (min !== "" && !(/^\d{1,2}$/.test(min) && Number(min) >= 1 && Number(min) <= MAX_SKILL_SCALE))
    errors.minRating = `Enter a rating from 1 to ${MAX_SKILL_SCALE}, or leave it empty for 1.`;
  const badCert = certs.find((c) => !SKILL_CODE.test(c));
  if (badCert) errors.certs = `"${badCert}" isn't a certificate code.`;
  else if (certs.length > 20) errors.certs = "Search on at most 20 certificates.";
  if (!DATE.test(values.asOf)) errors.asOf = "Enter a date.";
  if (!errors.skills && !errors.certs && terms.length === 0 && certs.length === 0) errors.skills = "Enter at least one skill or certificate.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, query: { skills: terms.join(","), minRating: min, certs: certs.join(","), asOf: values.asOf } };
}

function SkillCell({ row }: { row: SkillGapRow }) {
  return (
    <span>
      <span className={styles.mono}>{row.skill}</span> {row.actual}
      <span className={styles.note}> (needs {row.required})</span>
    </span>
  );
}

const columns: DataTableColumn<SkillMatch>[] = [
  { key: "personnelNumber", header: "Worker", mono: true, render: (row) => <Link to={`/hr/workers/${row.workerId}`}>{row.personnelNumber}</Link> },
  { key: "legalName", header: "Name" },
  {
    key: "skills",
    header: "Ratings",
    render: (row) =>
      row.skills.length === 0 ? (
        <Badge tone="neutral">Certificates only</Badge>
      ) : (
        <ul className={styles.warnings}>
          {row.skills.map((s) => (
            <li key={s.skill}>
              <SkillCell row={s} />
            </li>
          ))}
        </ul>
      ),
  },
];

/** Skill mapping search (FR-42.18): workers employed on a date with the ratings and valid certificates asked for. */
export function SkillSearchPage() {
  const [params, setParams] = useSearchParams();
  const current: SearchValues = {
    skills: params.get("skills") ?? "",
    minRating: params.get("minRating") ?? "",
    certs: params.get("certs") ?? "",
    asOf: params.get("asOf") ?? todayIso(),
  };
  const [values, setValues] = useState<SearchValues>(current);
  const [errors, setErrors] = useState<Errors>({});
  const applied = validateSearch(current).query;
  const key = applied ? JSON.stringify(applied) : "";
  const state = useLoad((signal) => (applied ? searchSkills(applied, signal) : Promise.resolve(null)), key);

  function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, query } = validateSearch(values);
    setErrors(found);
    if (!query) return;
    const next = new URLSearchParams();
    for (const [name, value] of Object.entries(query)) if (value) next.set(name, value);
    setParams(next);
  }

  const update = (patch: Partial<SearchValues>) => setValues({ ...values, ...patch });
  const result = state.data;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Find workers by skill"
        description="Lists the workers employed on the date who are rated at least the minimum on every skill and hold every certificate valid on that date."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Skill search">
          <FormField id="search-skills" label="Skills" hint="Comma-separated, CODE or CODE:rating, for example WELDING:3, PLC." error={errors.skills}>
            {(control) => <Input {...control} value={values.skills} invalid={Boolean(errors.skills)} onChange={(event) => update({ skills: event.target.value })} />}
          </FormField>
          <FormField id="search-minRating" label="Minimum rating" optional hint="For skills given without a rating. Default 1." error={errors.minRating}>
            {(control) => (
              <Input {...control} inputMode="numeric" value={values.minRating} invalid={Boolean(errors.minRating)} onChange={(event) => update({ minRating: event.target.value })} />
            )}
          </FormField>
          <FormField id="search-certs" label="Certificates" optional hint="Comma-separated codes, for example OSHA10." error={errors.certs}>
            {(control) => <Input {...control} value={values.certs} invalid={Boolean(errors.certs)} onChange={(event) => update({ certs: event.target.value })} />}
          </FormField>
          <FormField id="search-asOf" label="On" error={errors.asOf}>
            {(control) => <Input {...control} type="date" value={values.asOf} invalid={Boolean(errors.asOf)} onChange={(event) => update({ asOf: event.target.value })} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={Boolean(applied) && state.loading}>
              Search
            </Button>
          </div>
        </form>
      </Card>
      {!applied ? (
        <Card>
          <EmptyState headingLevel={3} title="Search for a skill profile" description="Enter the skills or certificates a job needs to see who has them." />
        </Card>
      ) : state.error ? (
        <Alert tone="danger" title="Could not search" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <>
          {result?.truncated ? (
            <Alert tone="warning" title="Showing the first 500 matches">
              Narrow the search with a higher rating or another skill.
            </Alert>
          ) : null}
          <DataTable
            label="Matching workers"
            columns={columns}
            rows={result?.items ?? []}
            getRowKey={(row) => row.workerId}
            loading={state.loading}
            empty={{ title: "Nobody matches", description: "No worker employed on this date has every skill and certificate. Try a lower rating." }}
          />
        </>
      )}
    </div>
  );
}
