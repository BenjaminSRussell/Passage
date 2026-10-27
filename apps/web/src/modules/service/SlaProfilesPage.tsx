import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Textarea, type DataTableColumn } from "../../shell/ui";
import { createSlaProfile, describeProblem, listSlaProfiles, newKey, PRIORITIES, type Problem, type SlaProfile, type SlaProfileInput, type SlaTarget } from "./api/client";
import { clock, DATE, duration, minuteOf } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const targetsText = (targets: SlaTarget[]) =>
  [...targets]
    .sort((a, b) => a.priority - b.priority)
    .map((t) => `P${t.priority} ${duration(t.responseMinutes)} / ${duration(t.resolutionMinutes)}`)
    .join(", ");

const columns: DataTableColumn<SlaProfile>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "timeZone", header: "Time zone", mono: true },
  { key: "hours", header: "Business hours", mono: true, render: (row) => (row.allDay ? "24x7" : `Mon–Fri ${clock(row.openMinute)}–${clock(row.closeMinute)}`) },
  { key: "holidays", header: "Holidays", align: "end", render: (row) => row.holidays.length },
  { key: "targets", header: "Response / resolution", render: (row) => targetsText(row.targets) },
];

/** SLA profiles: business-hour calendars and per-priority targets (FR-45.4). */
export function SlaProfilesPage() {
  const state = useLoad((signal) => listSlaProfiles(signal), "profiles");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="SLA profiles"
        description="Each profile sets response and resolution targets by priority, counted in its business hours and time zone."
        actions={
          <Link className={buttonClassName("primary")} to="/service/sla-profiles/new">
            New SLA profile
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load SLA profiles" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="SLA profiles"
          columns={columns}
          rows={state.data?.items ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No SLA profiles yet", description: "Create a profile before you create agreements." }}
        />
      )}
    </div>
  );
}

export interface TargetValues {
  response: string;
  resolution: string;
}

export interface ProfileValues {
  code: string;
  name: string;
  timeZone: string;
  allDay: boolean;
  open: string;
  close: string;
  pauseOnParts: boolean;
  holidays: string;
  targets: Record<number, TargetValues>;
}

type Errors = Record<string, string>;

/** Checks that mirror the service (api.go createSLAProfile). A priority with both targets empty is left out. */
export function validateProfile(values: ProfileValues): { errors: Errors; input?: SlaProfileInput } {
  const errors: Errors = {};
  if (!/^[A-Z0-9][A-Z0-9_-]{1,19}$/.test(values.code)) errors.code = "Use 2 to 20 characters of A–Z, 0–9, _ or -.";
  const name = values.name.trim();
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!values.timeZone.trim() || values.timeZone.trim() === "Local") errors.timeZone = "Enter an IANA time zone, such as America/Chicago.";
  const open = minuteOf(values.open);
  const close = minuteOf(values.close);
  if (!values.allDay && !(open < close)) errors.openMinute = "Opening must be before closing, as HH:MM.";
  const holidays = values.holidays
    .split(/[\s,]+/)
    .map((h) => h.trim())
    .filter(Boolean);
  if (holidays.some((h) => !DATE.test(h))) errors.holidays = "List dates as YYYY-MM-DD, separated by commas or new lines.";
  const targets: SlaTarget[] = [];
  for (const p of PRIORITIES) {
    const t = values.targets[p] ?? { response: "", resolution: "" };
    if (!t.response && !t.resolution) continue;
    const response = Number(t.response);
    const resolution = Number(t.resolution);
    if (!Number.isInteger(response) || !Number.isInteger(resolution) || response < 1 || resolution < response) {
      errors[`targets.${p}`] = "Enter whole minutes; resolution must be at least the response target.";
    } else {
      targets.push({ priority: p, responseMinutes: response, resolutionMinutes: resolution });
    }
  }
  if (targets.length === 0 && !Object.keys(errors).some((k) => k.startsWith("targets."))) errors.targets = "Set targets for at least one priority.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      code: values.code,
      name,
      timeZone: values.timeZone.trim(),
      allDay: values.allDay,
      openMinute: values.allDay ? 0 : open,
      closeMinute: values.allDay ? 1440 : close,
      pauseOnParts: values.pauseOnParts,
      holidays,
      targets,
    },
  };
}

/** Create an SLA profile (FR-45.4, BR-45.2). */
export function NewSlaProfilePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<ProfileValues>({
    code: "",
    name: "",
    timeZone: "UTC",
    allDay: false,
    open: "08:00",
    close: "18:00",
    pauseOnParts: false,
    holidays: "",
    targets: { 1: { response: "60", resolution: "240" }, 2: { response: "120", resolution: "480" }, 3: { response: "", resolution: "" }, 4: { response: "", resolution: "" } },
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<ProfileValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }
  const updateTarget = (p: number, patch: Partial<TargetValues>) => update({ targets: { ...values.targets, [p]: { ...values.targets[p], ...patch } } });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateProfile(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createSlaProfile(input, key.current);
      navigate("/service/sla-profiles");
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (name: "code" | "name" | "timeZone" | "open" | "close", label: string, hint?: string, errorKey: string = name, type = "text") => (
    <FormField id={`profile-${name}`} label={label} hint={hint} error={errors[errorKey]}>
      {(control) => (
        <Input
          {...control}
          type={type}
          value={values[name]}
          disabled={(name === "open" || name === "close") && values.allDay}
          onChange={(e) => update({ [name]: name === "code" ? e.target.value.toUpperCase() : e.target.value })}
          invalid={Boolean(errors[errorKey])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New SLA profile" description="Clocks count only business minutes Monday to Friday between opening and closing, holidays excluded, unless the profile is 24x7." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New SLA profile">
          {text("code", "Code", "For example GOLD or STD-24X7.")}
          {text("name", "Name")}
          {text("timeZone", "Time zone", "IANA zone of the service site.")}
          {text("open", "Opens", undefined, "openMinute", "time")}
          {text("close", "Closes", undefined, "openMinute", "time")}
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input id="profile-allDay" type="checkbox" checked={values.allDay} onChange={(e) => update({ allDay: e.target.checked })} />
              <label htmlFor="profile-allDay">24x7</label>
            </span>
            <span className={styles.check}>
              <Input id="profile-pauseOnParts" type="checkbox" checked={values.pauseOnParts} onChange={(e) => update({ pauseOnParts: e.target.checked })} />
              <label htmlFor="profile-pauseOnParts">Pause resolution while waiting for parts</label>
            </span>
          </div>
          <div className={styles.wide}>
            <FormField id="profile-holidays" label="Holidays" hint="Dates as YYYY-MM-DD, separated by commas or new lines." error={errors.holidays} optional>
              {(control) => <Textarea {...control} value={values.holidays} onChange={(e) => update({ holidays: e.target.value })} invalid={Boolean(errors.holidays)} />}
            </FormField>
          </div>
          <div className={`${styles.wide} ${styles.lines}`}>
            {PRIORITIES.map((p) => (
              <fieldset key={p} className={styles.lineCard}>
                <legend className={styles.lineLegend}>{`Priority ${p} targets`}</legend>
                <FormField id={`target-${p}-response`} label="Response minutes" optional>
                  {(control) => <Input {...control} inputMode="numeric" value={values.targets[p].response} onChange={(e) => updateTarget(p, { response: e.target.value })} />}
                </FormField>
                <FormField id={`target-${p}-resolution`} label="Resolution minutes" optional>
                  {(control) => <Input {...control} inputMode="numeric" value={values.targets[p].resolution} onChange={(e) => updateTarget(p, { resolution: e.target.value })} />}
                </FormField>
                {errors[`targets.${p}`] ? (
                  <p role="alert" className={styles.fieldError}>
                    {errors[`targets.${p}`]}
                  </p>
                ) : null}
              </fieldset>
            ))}
            {errors.targets ? (
              <p role="alert" className={styles.fieldError}>
                {errors.targets}
              </p>
            ) : null}
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "SVC-PROF-001" ? "That code is taken" : "Could not create the profile"} live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create SLA profile
            </Button>
            <Link className={buttonClassName("ghost")} to="/service/sla-profiles">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
