import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, Card, FormField, Input, Textarea } from "../../shell/ui";
import { describeProblem, updateScheduling, type Problem, type SchedulingInput, type Versioned, type WorkOrder } from "./api/client";
import { Details } from "./Details";
import { dash, duration, formatDateTime, shortId, utcInstant, utcLocal } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export interface SchedulingValues {
  skills: string;
  windowStart: string;
  windowEnd: string;
  address: string;
  latitude: string;
  longitude: string;
  estDurationMinutes: string;
}

type Errors = Record<string, string>;

const SKILL = /^[A-Z0-9][A-Z0-9_.-]{0,39}$/;

export function schedulingValues(wo: WorkOrder): SchedulingValues {
  return {
    skills: (wo.requiredSkills ?? []).join(", "),
    windowStart: utcLocal(wo.windowStart),
    windowEnd: utcLocal(wo.windowEnd),
    address: wo.address ?? "",
    latitude: wo.latitude == null ? "" : String(wo.latitude),
    longitude: wo.longitude == null ? "" : String(wo.longitude),
    estDurationMinutes: String(wo.estDurationMinutes),
  };
}

/** Checks that mirror the service (api/dispatch.go schedulingOf). Window times are read as UTC. */
export function validateScheduling(values: SchedulingValues): { errors: Errors; input?: SchedulingInput } {
  const errors: Errors = {};
  const skills = [...new Set(values.skills.split(/[\s,]+/).map((s) => s.trim().toUpperCase()).filter(Boolean))];
  if (skills.length > 30) errors.requiredSkills = "Use at most 30 skills.";
  else if (skills.some((s) => !SKILL.test(s))) errors.requiredSkills = "A skill is 1 to 40 characters of A–Z, 0–9, _, . or -.";
  const start = values.windowStart ? utcInstant(values.windowStart) : null;
  const end = values.windowEnd ? utcInstant(values.windowEnd) : null;
  if (Boolean(values.windowStart) !== Boolean(values.windowEnd)) errors.windowStart = "Give both ends of the window, or neither.";
  else if (values.windowStart && (!start || !end)) errors.windowStart = "Enter the window as date and time.";
  else if (start && end && Date.parse(end) <= Date.parse(start)) errors.windowEnd = "The window must end after it starts.";
  if (values.address.length > 500) errors.address = "Use at most 500 characters.";
  const lat = values.latitude.trim();
  const lon = values.longitude.trim();
  if ((lat === "") !== (lon === "")) errors.latitude = "Give both latitude and longitude, or neither.";
  else if (lat !== "" && !(Math.abs(Number(lat)) <= 90 && Math.abs(Number(lon)) <= 180 && !Number.isNaN(Number(lat)) && !Number.isNaN(Number(lon)))) {
    errors.latitude = "Latitude is -90 to 90 and longitude -180 to 180.";
  }
  const minutes = Number(values.estDurationMinutes);
  if (!Number.isInteger(minutes) || minutes < 1 || minutes > 1440) errors.estDurationMinutes = "Enter whole minutes from 1 to 1440.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      requiredSkills: skills,
      windowStart: start,
      windowEnd: end,
      address: values.address.trim(),
      latitude: lat === "" ? null : Number(lat),
      longitude: lon === "" ? null : Number(lon),
      estDurationMinutes: minutes,
    },
  };
}

interface Props {
  workOrder: WorkOrder;
  etag: string;
  onSaved(next: Versioned<WorkOrder>): void;
  onConflict(): void;
}

/** Scheduling data and the assignment of a work order; editable while it is unscheduled (FR-45.6, FR-45.7). */
export function SchedulingPanel({ workOrder: wo, etag, onSaved, onConflict }: Props) {
  const [values, setValues] = useState<SchedulingValues>(() => schedulingValues(wo));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState("");
  const editable = wo.status === "UNSCHEDULED";

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateScheduling(values);
    setErrors(found);
    setFailure(null);
    setSaved("");
    if (!input) return;
    setBusy(true);
    try {
      const next = await updateScheduling(wo.id, etag, input);
      onSaved(next);
      setSaved("Scheduling data saved.");
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "conflict_version") onConflict();
    } finally {
      setBusy(false);
    }
  }

  const text = (name: keyof SchedulingValues, label: string, hint?: string, extra: { type?: string; errorKey?: string; optional?: boolean } = {}) => {
    const errorKey = extra.errorKey ?? name;
    return (
      <FormField id={`sched-${name}`} label={label} hint={hint} error={errors[errorKey]} optional={extra.optional}>
        {(control) => <Input {...control} type={extra.type} value={values[name]} onChange={(e) => setValues({ ...values, [name]: e.target.value })} invalid={Boolean(errors[errorKey])} />}
      </FormField>
    );
  };

  return (
    <Card title="Scheduling" headingLevel={3}>
      <Details
        label="Assignment"
        items={[
          ["Technician", wo.technicianId ? <Link to={`/service/technicians/${wo.technicianId}`}>{shortId(wo.technicianId)}</Link> : "Not assigned"],
          ["Visit start", formatDateTime(wo.assignedStart), true],
          ["Visit end", formatDateTime(wo.assignedEnd), true],
          ["Pinned", wo.technicianId ? (wo.assignmentPinned ? "Yes" : "No") : dash],
          ["Required skills", (wo.requiredSkills ?? []).length > 0 ? (wo.requiredSkills ?? []).join(", ") : "None"],
          ["Customer window", wo.windowStart ? `${formatDateTime(wo.windowStart)} to ${formatDateTime(wo.windowEnd)}` : "None", true],
          ["Address", wo.address || dash],
          ["Estimate", duration(wo.estDurationMinutes)],
        ]}
      />
      {editable ? (
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Scheduling data">
          {text("skills", "Required skills", "Codes such as HVAC; separated by commas.", { errorKey: "requiredSkills", optional: true })}
          {text("estDurationMinutes", "Estimated minutes")}
          {text("windowStart", "Customer window starts", "UTC", { type: "datetime-local", optional: true })}
          {text("windowEnd", "Customer window ends", "UTC", { type: "datetime-local", optional: true })}
          <div className={styles.wide}>
            <FormField id="sched-address" label="Address" error={errors.address} optional>
              {(control) => <Textarea {...control} rows={2} value={values.address} onChange={(e) => setValues({ ...values, address: e.target.value })} invalid={Boolean(errors.address)} />}
            </FormField>
          </div>
          {text("latitude", "Latitude", undefined, { optional: true })}
          {text("longitude", "Longitude", undefined, { optional: true })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the scheduling data" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save scheduling data
            </Button>
          </div>
          <Alert tone="success" live="status">
            {saved}
          </Alert>
        </form>
      ) : (
        <p className={styles.note}>Scheduling data changes only while the work order is unscheduled; take it off the schedule on the dispatch board first.</p>
      )}
    </Card>
  );
}
