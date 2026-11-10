import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import { describeProblem, getResource, newKey, updateResource, type AffectedJob, type Problem, type Resource, type ResourcePatch, type Shift } from "./api/client";
import { clock, formatDateTime, parseClock, shortId, WEEK_ORDER, WEEKDAYS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export interface ShiftRow {
  weekday: number;
  start: string;
  end: string;
}

export interface EditValues {
  name: string;
  timeZone: string;
  rows: ShiftRow[];
  /** The engineering resource id this resource stands for; empty when unmapped. */
  engineeringResourceId: string;
  /** Efficiency in whole percent, as typed. */
  efficiencyPct: string;
}

type Errors = Partial<Record<"name" | "timeZone" | "shifts" | "engineeringResourceId" | "efficiencyPct" | "form", string>>;

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

const MAX_SHIFTS = 14;
const ORDER: readonly number[] = WEEK_ORDER;

export function rowsOf(shifts: Shift[]): ShiftRow[] {
  return [...shifts]
    .sort((a, b) => ORDER.indexOf(a.weekday) - ORDER.indexOf(b.weekday) || a.startMinute - b.startMinute)
    .map((s) => ({ weekday: s.weekday, start: clock(s.startMinute), end: s.endMinute === 1440 ? "24:00" : clock(s.endMinute) }));
}

/** Checks that mirror the service, and the patch the form sends: only what changed. */
export function validateEdit(values: EditValues, current: Resource): { errors: Errors; patch?: ResourcePatch } {
  const errors: Errors = {};
  const name = values.name.trim();
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  const timeZone = values.timeZone.trim();
  if (!/^[A-Za-z_]+(\/[A-Za-z0-9_+-]+)*$/.test(timeZone)) errors.timeZone = "Enter an IANA time zone, such as Europe/Berlin or UTC.";
  const shifts: Shift[] = [];
  if (values.rows.length < 1 || values.rows.length > MAX_SHIFTS) errors.shifts = `Keep 1 to ${MAX_SHIFTS} shifts.`;
  for (const row of values.rows) {
    const start = parseClock(row.start);
    const end = row.end === "00:00" ? 1440 : parseClock(row.end);
    if (start === null || end === null || end <= start) {
      errors.shifts = "Every shift needs a start and an end after it (24:00 or 00:00 is midnight).";
      break;
    }
    shifts.push({ weekday: row.weekday, startMinute: start, endMinute: end });
  }
  const engineering = values.engineeringResourceId.trim().toLowerCase();
  if (engineering !== "" && !UUID.test(engineering)) errors.engineeringResourceId = "Enter the engineering resource id (a UUID), or leave it empty.";
  const efficiency = Number(values.efficiencyPct.trim());
  if (values.efficiencyPct.trim() === "" || !Number.isInteger(efficiency) || efficiency < 1 || efficiency > 500) errors.efficiencyPct = "Enter a whole percent from 1 to 500.";
  if (Object.keys(errors).length > 0) return { errors };
  const patch: ResourcePatch = {};
  if (name !== current.name) patch.name = name;
  if (timeZone !== current.timeZone) patch.timeZone = timeZone;
  if (efficiency !== (current.efficiencyPct ?? 100)) patch.efficiencyPct = efficiency;
  if (engineering !== (current.engineeringResourceId ?? "")) patch.engineeringResourceId = engineering;
  if (JSON.stringify(rowsOf(shifts)) !== JSON.stringify(rowsOf(current.shifts))) patch.shifts = shifts;
  if (Object.keys(patch).length === 0) {
    errors.form = "Nothing changed.";
    return { errors };
  }
  return { errors, patch };
}

function Form({ initial, onReload }: { initial: Resource; onReload: () => void }) {
  const [resource, setResource] = useState<Resource>(initial);
  const [values, setValues] = useState<EditValues>({ name: resource.name, timeZone: resource.timeZone, rows: rowsOf(resource.shifts), engineeringResourceId: resource.engineeringResourceId ?? "", efficiencyPct: String(resource.efficiencyPct ?? 100) });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState<{ version: number; jobs: AffectedJob[] } | null>(null);
  const key = useRef(newKey());

  function update(patch: Partial<EditValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  function setRow(index: number, patch: Partial<ShiftRow>) {
    update({ rows: values.rows.map((r, i) => (i === index ? { ...r, ...patch } : r)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, patch } = validateEdit(values, resource);
    setErrors(found);
    setFailure(null);
    setSaved(null);
    if (!patch) return;
    setBusy(true);
    try {
      const result = await updateResource(resource.id, resource.version, patch, key.current);
      const { affectedJobs, ...next } = result;
      setResource(next);
      setSaved({ version: result.version, jobs: affectedJobs });
      key.current = newKey();
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of ["name", "timeZone", "shifts", "engineeringResourceId", "efficiencyPct"] as const) {
        if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      }
      if (problem.code === "SCHED-0132") fieldErrors.engineeringResourceId = problem.message;
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="edit-name" label="Name" error={errors.name}>
          {(control) => <Input {...control} value={values.name} maxLength={120} onChange={(event) => update({ name: event.target.value })} invalid={Boolean(errors.name)} />}
        </FormField>
        <FormField id="edit-tz" label="Time zone" hint="IANA name of the site's zone." error={errors.timeZone}>
          {(control) => <Input {...control} value={values.timeZone} onChange={(event) => update({ timeZone: event.target.value })} invalid={Boolean(errors.timeZone)} />}
        </FormField>
        <FormField id="edit-efficiency" label="Efficiency (%)" hint="Applies to schedule runs and moves after the change; jobs already placed keep their length." error={errors.efficiencyPct}>
          {(control) => <Input {...control} inputMode="numeric" value={values.efficiencyPct} onChange={(event) => update({ efficiencyPct: event.target.value })} invalid={Boolean(errors.efficiencyPct)} />}
        </FormField>
        <FormField
          id="edit-engineering"
          label="Engineering resource id"
          hint="The id production orders use for this resource. Leave empty to remove the mapping."
          error={errors.engineeringResourceId}
        >
          {(control) => (
            <Input
              {...control}
              value={values.engineeringResourceId}
              onChange={(event) => update({ engineeringResourceId: event.target.value })}
              invalid={Boolean(errors.engineeringResourceId)}
            />
          )}
        </FormField>
        <fieldset className={styles.groups} aria-describedby={errors.shifts ? "edit-shifts-error" : undefined}>
          <legend className={styles.groupsLegend}>Shifts (site-local times)</legend>
          {values.rows.map((row, index) => (
            <div key={index} className={styles.formActions}>
              <Select aria-label={`Shift ${index + 1} weekday`} value={row.weekday} onChange={(event) => setRow(index, { weekday: Number(event.target.value) })}>
                {WEEK_ORDER.map((day) => (
                  <option key={day} value={day}>
                    {WEEKDAYS[day]}
                  </option>
                ))}
              </Select>
              <Input aria-label={`Shift ${index + 1} start`} type="time" value={row.start} onChange={(event) => setRow(index, { start: event.target.value })} />
              <Input aria-label={`Shift ${index + 1} end`} type="time" value={row.end === "24:00" ? "00:00" : row.end} onChange={(event) => setRow(index, { end: event.target.value })} />
              <Button type="button" size="sm" variant="ghost" onClick={() => update({ rows: values.rows.filter((_, i) => i !== index) })}>
                {`Remove shift ${index + 1}`}
              </Button>
            </div>
          ))}
          <Button
            type="button"
            size="sm"
            variant="secondary"
            disabled={values.rows.length >= MAX_SHIFTS}
            onClick={() => update({ rows: [...values.rows, { weekday: 1, start: "08:00", end: "16:00" }] })}
          >
            Add a shift
          </Button>
          {errors.shifts ? (
            <p id="edit-shifts-error" role="alert" className={styles.fieldError}>
              {errors.shifts}
            </p>
          ) : null}
        </fieldset>
        {errors.form ? (
          <p role="alert" className={styles.fieldError}>
            {errors.form}
          </p>
        ) : null}
        {failure ? (
          <div className={styles.formActions}>
            {failure.code === "SCHED-0124" ? (
              <Alert
                tone="danger"
                title="The resource changed since you opened it"
                live="alert"
                action={
                  <Button size="sm" onClick={onReload}>
                    Load the latest version
                  </Button>
                }
              >
                {failure.message} ({failure.code}). Loading the latest version discards your changes on this screen.
              </Alert>
            ) : (
              <Alert tone="danger" title="Could not save the resource" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            )}
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save changes
          </Button>
          <Link className={buttonClassName("ghost")} to={`/scheduling/resources/${resource.id}`}>
            Back to the resource
          </Link>
        </div>
      </form>
      <Alert tone="success" live="status">
        {saved ? `The resource was saved (version ${saved.version}).` : ""}
      </Alert>
      {saved ? (
        saved.jobs.length === 0 ? (
          <p className={styles.note}>No booked job lies outside the new calendar.</p>
        ) : (
          <div className={styles.stack}>
            <Alert tone="warning" title={`${saved.jobs.length} booked job(s) are outside the new calendar`}>
              They stay where they are and show as outside open capacity on the workspace. Move them there or run the schedule again; a firmed job
              must be unfirmed first.
            </Alert>
            <DataTable
              label="Jobs outside the calendar"
              columns={[
                { key: "orderId", header: "Order", mono: true, render: (j: AffectedJob) => <span title={j.orderId}>{shortId(j.orderId)}</span> },
                { key: "operNumber", header: "Operation", align: "end" },
                { key: "start", header: "From", render: (j: AffectedJob) => formatDateTime(j.start) },
                { key: "end", header: "To", render: (j: AffectedJob) => formatDateTime(j.end) },
                { key: "firmed", header: "Firmed", render: (j: AffectedJob) => (j.firmed ? "Yes" : "No") },
              ]}
              rows={saved.jobs}
              getRowKey={(j) => j.jobId}
            />
          </div>
        )
      ) : null}
    </>
  );
}

/** Change a resource's name, time zone, weekly calendar and engineering resource mapping (FR-23.1, AC-23.7, FR-23.10). */
export function EditResourcePage() {
  const { id = "" } = useParams();
  const state = useLoad<Resource>((signal) => getResource(id, signal), id);
  return (
    <div className={styles.stack}>
      <PageHeader title="Edit resource" description="Shifts replace the whole weekly calendar. Booked jobs the new calendar no longer covers are listed after saving." />
      {state.error ? (
        <Alert tone="danger" title="Could not load the resource" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : !state.data ? (
        <div aria-busy="true">
          <span className="visually-hidden">Loading the resource…</span>
          <Skeleton height="tile" />
        </div>
      ) : (
        <Card title={state.data.code}>
          <Form key={`${state.data.id}:${state.data.version}`} initial={state.data} onReload={state.reload} />
        </Card>
      )}
    </div>
  );
}
