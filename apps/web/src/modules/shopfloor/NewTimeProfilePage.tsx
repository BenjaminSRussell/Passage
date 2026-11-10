import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Textarea } from "../../shell/ui";
import { createTimeProfile, DAY_TYPES, describeProblem, newKey, type BreakWindow, type DayType, type NewTimeProfile, type Problem, type ProfileDay } from "./api/client";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const LABEL: Record<DayType, string> = { weekday: "Weekday", saturday: "Saturday", sunday: "Sunday" };
const CLOCK = /^([01]\d|2[0-3]):[0-5]\d$/;

export interface DayValues {
  defined: boolean;
  shiftStart: string;
  shiftEnd: string;
  /** One break per line as HH:MM-HH:MM. */
  breaks: string;
  roundingMinutes: string;
  toleranceBeforeMin: string;
  toleranceAfterMin: string;
  flexAllowed: boolean;
}

export interface ProfileValues {
  code: string;
  name: string;
  days: Record<DayType, DayValues>;
}

export type ProfileErrors = Partial<Record<string, string>>;

const emptyDay = (defined: boolean): DayValues => ({
  defined,
  shiftStart: "",
  shiftEnd: "",
  breaks: "",
  roundingMinutes: "1",
  toleranceBeforeMin: "10",
  toleranceAfterMin: "5",
  flexAllowed: false,
});

const minutes = (clock: string) => Number(clock.slice(0, 2)) * 60 + Number(clock.slice(3));

function parseBreaks(raw: string): { breaks: BreakWindow[]; error?: string } {
  const breaks: BreakWindow[] = [];
  for (const line of raw.split("\n").map((l) => l.trim()).filter((l) => l !== "")) {
    const [from = "", to = "", ...rest] = line.split("-").map((part) => part.trim());
    if (rest.length > 0 || !CLOCK.test(from) || !CLOCK.test(to) || minutes(to) <= minutes(from)) {
      return { breaks, error: `"${line}" is not a break. Write each break as HH:MM-HH:MM, with the end after the start.` };
    }
    breaks.push({ from, to });
  }
  breaks.sort((a, b) => minutes(a.from) - minutes(b.from));
  for (let i = 1; i < breaks.length; i += 1) {
    const prev = breaks[i - 1]!;
    if (minutes(breaks[i]!.from) < minutes(prev.to)) return { breaks, error: "Breaks must not overlap." };
  }
  if (breaks.length > 6) return { breaks, error: "At most 6 breaks." };
  return { breaks };
}

function whole(raw: string, lo: number, hi: number): number | null {
  return /^\d{1,2}$/.test(raw.trim()) && Number(raw) >= lo && Number(raw) <= hi ? Number(raw) : null;
}

/** Checks that mirror the service, and the request the form sends. */
export function validateProfile(values: ProfileValues): { errors: ProfileErrors; input?: NewTimeProfile } {
  const errors: ProfileErrors = {};
  const code = values.code.trim();
  const name = values.name.trim();
  if (code.length < 1 || code.length > 40) errors.code = "Enter a code of up to 40 characters.";
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  const days: ProfileDay[] = [];
  for (const dayType of DAY_TYPES) {
    const d = values.days[dayType];
    if (!d.defined) continue;
    const start = d.shiftStart.trim();
    const end = d.shiftEnd.trim();
    if ((start === "") !== (end === "")) errors[`${dayType}.shiftStart`] = "Give both shift start and shift end, or neither.";
    else if (start !== "" && (!CLOCK.test(start) || !CLOCK.test(end))) errors[`${dayType}.shiftStart`] = "Enter times as HH:MM, such as 07:00.";
    else if (start !== "" && minutes(end) <= minutes(start)) errors[`${dayType}.shiftEnd`] = "The shift must end after it starts.";
    const parsed = parseBreaks(d.breaks);
    if (parsed.error) errors[`${dayType}.breaks`] = parsed.error;
    const rounding = whole(d.roundingMinutes, 1, 15);
    const before = whole(d.toleranceBeforeMin, 0, 60);
    const after = whole(d.toleranceAfterMin, 0, 60);
    if (rounding === null) errors[`${dayType}.roundingMinutes`] = "Enter whole minutes from 1 to 15.";
    if (before === null) errors[`${dayType}.toleranceBeforeMin`] = "Enter whole minutes from 0 to 60.";
    if (after === null) errors[`${dayType}.toleranceAfterMin`] = "Enter whole minutes from 0 to 60.";
    if (rounding !== null && before !== null && after !== null) {
      days.push({
        dayType,
        shiftStart: start === "" ? null : start,
        shiftEnd: end === "" ? null : end,
        breaks: parsed.breaks,
        roundingMinutes: rounding,
        toleranceBeforeMin: before,
        toleranceAfterMin: after,
        flexAllowed: d.flexAllowed,
      });
    }
  }
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { code, name, days } };
}

/** Maps the service's `days[i].field` problems back to the form's `dayType.field` keys. */
function serviceErrors(fields: Record<string, string>, defined: DayType[]): ProfileErrors {
  const out: ProfileErrors = {};
  for (const [key, message] of Object.entries(fields)) {
    const match = /^days\[(\d+)\]\.(\w+)/.exec(key);
    if (match) {
      const dayType = defined[Number(match[1])];
      if (dayType) out[`${dayType}.${match[2]}`] = message;
    } else if (key === "code" || key === "name") out[key] = message;
  }
  return out;
}

/** Create a time profile with day rules per day type (FR-25.7, BR-25.2). */
export function NewTimeProfilePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<ProfileValues>({
    code: "",
    name: "",
    days: { weekday: emptyDay(true), saturday: emptyDay(false), sunday: emptyDay(false) },
  });
  const [errors, setErrors] = useState<ProfileErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(next: ProfileValues) {
    setValues(next);
    key.current = newKey();
  }
  const updateDay = (dayType: DayType, patch: Partial<DayValues>) =>
    update({ ...values, days: { ...values.days, [dayType]: { ...values.days[dayType], ...patch } } });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateProfile(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createTimeProfile(input, key.current);
      navigate("/shopfloor/time-profiles");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors = serviceErrors(problem.fields, input.days.map((d) => d.dayType));
      if (problem.code === "SF-0142") fieldErrors.code = "This code is already used.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const dayField = (dayType: DayType, name: keyof DayValues & string, label: string, hint?: string, extra?: Record<string, unknown>) => (
    <FormField id={`profile-${dayType}-${name}`} label={`${LABEL[dayType]} ${label.toLowerCase()}`} hint={hint} error={errors[`${dayType}.${name}`]}>
      {(control) => (
        <Input
          {...control}
          {...extra}
          value={String(values.days[dayType][name])}
          onChange={(event) => updateDay(dayType, { [name]: event.target.value } as Partial<DayValues>)}
          invalid={Boolean(errors[`${dayType}.${name}`])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New time profile" description="Define the day types that differ from the defaults. A clock-in just before the shift start counts from the shift start; unpaid breaks come off the paid hours." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="profile-code" label="Code" hint="Unique, for example DAY-SHIFT." error={errors.code}>
            {(control) => <Input {...control} maxLength={40} value={values.code} onChange={(event) => update({ ...values, code: event.target.value })} invalid={Boolean(errors.code)} />}
          </FormField>
          <FormField id="profile-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} maxLength={120} value={values.name} onChange={(event) => update({ ...values, name: event.target.value })} invalid={Boolean(errors.name)} />}
          </FormField>
          {DAY_TYPES.map((dayType) => {
            const d = values.days[dayType];
            return (
              <fieldset key={dayType} className={styles.dayGroup}>
                <legend className={styles.legend}>{LABEL[dayType]}</legend>
                <label className={styles.checkRow} htmlFor={`profile-${dayType}-defined`}>
                  <Input id={`profile-${dayType}-defined`} type="checkbox" checked={d.defined} onChange={(event) => updateDay(dayType, { defined: event.target.checked })} />
                  {`Define ${LABEL[dayType].toLowerCase()} rules`}
                </label>
                {d.defined ? (
                  <>
                    {dayField(dayType, "shiftStart", "Shift start", "HH:MM, or empty for no shift window.", { inputMode: "numeric", placeholder: "07:00" })}
                    {dayField(dayType, "shiftEnd", "Shift end", "HH:MM, after the start.", { inputMode: "numeric", placeholder: "15:30" })}
                    {dayField(dayType, "roundingMinutes", "Rounding minutes", "1 to 15.", { inputMode: "numeric" })}
                    {dayField(dayType, "toleranceBeforeMin", "Tolerance before shift", "0 to 60 minutes.", { inputMode: "numeric" })}
                    {dayField(dayType, "toleranceAfterMin", "Tolerance after shift", "0 to 60 minutes.", { inputMode: "numeric" })}
                    <FormField id={`profile-${dayType}-breaks`} label={`${LABEL[dayType]} unpaid breaks`} hint="One per line as HH:MM-HH:MM, for example 12:00-12:30." error={errors[`${dayType}.breaks`]} optional>
                      {(control) => <Textarea {...control} rows={3} value={d.breaks} onChange={(event) => updateDay(dayType, { breaks: event.target.value })} invalid={Boolean(errors[`${dayType}.breaks`])} />}
                    </FormField>
                  </>
                ) : null}
              </fieldset>
            );
          })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the time profile" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create time profile
            </Button>
            <Link className={buttonClassName("ghost")} to="/shopfloor/time-profiles">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
