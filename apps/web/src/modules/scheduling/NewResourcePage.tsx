import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { createResource, describeProblem, newKey, type NewResource, type Problem } from "./api/client";
import { parseClock, WEEK_ORDER, WEEKDAYS } from "./format";
import styles from "./pages.module.css";

interface Values {
  code: string;
  name: string;
  timeZone: string;
  finite: boolean;
  days: number[];
  start: string;
  end: string;
}

type Errors = Partial<Record<"code" | "name" | "timeZone" | "days" | "start" | "end", string>>;

const CODE = /^[A-Z0-9][A-Z0-9_-]{0,31}$/;

/** Checks that mirror the service, and the shifts the form produces. */
export function validateResource(values: Values): { errors: Errors; input?: NewResource } {
  const errors: Errors = {};
  if (!CODE.test(values.code)) errors.code = "Use 1 to 32 capital letters, digits, _ or -, starting with a letter or digit.";
  if (values.name.trim().length < 1 || values.name.trim().length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!/^[A-Za-z_]+(\/[A-Za-z0-9_+-]+)*$/.test(values.timeZone.trim())) errors.timeZone = "Enter an IANA time zone, such as Europe/Berlin or UTC.";
  if (values.days.length === 0) errors.days = "Choose at least one working day.";
  const start = parseClock(values.start);
  // An end of 00:00 means midnight at the end of the day.
  const end = values.end === "00:00" ? 1440 : parseClock(values.end);
  if (start === null) errors.start = "Enter a start time.";
  if (end === null) errors.end = "Enter an end time.";
  else if (start !== null && end <= start) errors.end = "The shift must end after it starts.";
  if (Object.keys(errors).length > 0 || start === null || end === null) return { errors };
  return {
    errors,
    input: {
      code: values.code,
      name: values.name.trim(),
      timeZone: values.timeZone.trim(),
      finite: values.finite,
      shifts: values.days.map((weekday) => ({ weekday, startMinute: start, endMinute: end })),
    },
  };
}

/** Create a resource with one shift on the chosen weekdays (site-local times). */
export function NewResourcePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Values>({ code: "", name: "", timeZone: "UTC", finite: true, days: [1, 2, 3, 4, 5], start: "08:00", end: "16:00" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  function toggleDay(day: number) {
    update({ days: values.days.includes(day) ? values.days.filter((d) => d !== day) : [...values.days, day].sort() });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateResource(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const resource = await createResource(input, key.current);
      navigate(`/scheduling/resources/${resource.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of ["code", "name", "timeZone"] as const) {
        if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      }
      if (problem.fields.shifts) fieldErrors.days = problem.fields.shifts;
      if (problem.code === "SCHED-0101") fieldErrors.code = "A resource with this code already exists.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New resource" description="The calendar is in the site's time zone; the scheduler converts it to UTC, so DST days have 23 or 25 hours." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="resource-code" label="Code" hint="For example MIX-1." error={errors.code}>
            {(control) => <Input {...control} value={values.code} maxLength={32} onChange={(event) => update({ code: event.target.value.toUpperCase() })} invalid={Boolean(errors.code)} />}
          </FormField>
          <FormField id="resource-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} value={values.name} maxLength={120} onChange={(event) => update({ name: event.target.value })} invalid={Boolean(errors.name)} />}
          </FormField>
          <FormField id="resource-tz" label="Time zone" hint="IANA name of the site's zone." error={errors.timeZone}>
            {(control) => <Input {...control} value={values.timeZone} onChange={(event) => update({ timeZone: event.target.value })} invalid={Boolean(errors.timeZone)} />}
          </FormField>
          <FormField id="resource-start" label="Shift start" error={errors.start}>
            {(control) => <Input {...control} type="time" value={values.start} onChange={(event) => update({ start: event.target.value })} invalid={Boolean(errors.start)} />}
          </FormField>
          <FormField id="resource-end" label="Shift end" hint="00:00 means midnight at the end of the day." error={errors.end}>
            {(control) => <Input {...control} type="time" value={values.end} onChange={(event) => update({ end: event.target.value })} invalid={Boolean(errors.end)} />}
          </FormField>
          <fieldset className={styles.groups} aria-describedby={errors.days ? "resource-days-error" : undefined}>
            <legend className={styles.groupsLegend}>Working days</legend>
            {WEEK_ORDER.map((day) => (
              <span key={day} className={styles.check}>
                <Input type="checkbox" id={`resource-day-${day}`} checked={values.days.includes(day)} onChange={() => toggleDay(day)} />
                <label htmlFor={`resource-day-${day}`}>{WEEKDAYS[day]}</label>
              </span>
            ))}
            {errors.days ? (
              <p id="resource-days-error" role="alert" className={styles.fieldError}>
                {errors.days}
              </p>
            ) : null}
          </fieldset>
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input id="resource-finite" type="checkbox" checked={values.finite} onChange={(event) => update({ finite: event.target.checked })} />
              <label htmlFor="resource-finite">Finite capacity</label>
            </span>
            <span className={styles.note}>A finite resource runs one job at a time.</span>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the resource" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create resource
            </Button>
            <Link className={buttonClassName("ghost")} to="/scheduling/resources">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
