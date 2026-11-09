import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  createTechnician,
  describeProblem,
  getTechnician,
  listTechnicians,
  newKey,
  updateTechnician,
  type Problem,
  type Technician,
  type TechnicianInput,
  type Versioned,
} from "./api/client";
import { DUTY_DISPATCH } from "./DispatchBoardPage";
import { Details } from "./Details";
import { PagedTable } from "./PagedTable";
import { clock, dash, duration, minuteOf, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const DAYS = [
  { value: 1, label: "Mon" },
  { value: 2, label: "Tue" },
  { value: 3, label: "Wed" },
  { value: 4, label: "Thu" },
  { value: 5, label: "Fri" },
  { value: 6, label: "Sat" },
  { value: 7, label: "Sun" },
] as const;

const SKILL = /^[A-Z0-9][A-Z0-9_.-]{0,39}$/;
const CODE = /^[A-Z0-9][A-Z0-9_-]{1,19}$/;

const daysText = (days: number[]) => (days.length === 7 ? "Every day" : DAYS.filter((d) => days.includes(d.value)).map((d) => d.label).join(", "));

const columns: DataTableColumn<Technician>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "skills", header: "Skills", render: (row) => (row.skills.length > 0 ? row.skills.join(", ") : dash) },
  { key: "territories", header: "Territories", align: "end", render: (row) => row.territoryIds.length },
  { key: "shift", header: "Shift", render: (row) => `${daysText(row.shiftDays)} ${clock(row.shiftStartMinute)}–${clock(row.shiftEndMinute)}` },
  { key: "max", header: "Daily maximum", align: "end", render: (row) => duration(row.maxDailyMinutes) },
  { key: "active", header: "Status", render: (row) => <Badge tone={row.active ? "success" : "neutral"}>{row.active ? "Active" : "Inactive"}</Badge> },
];

/** Field technicians: skills, territories, shift and daily maximum (FR-45.7, PRD 8.45.4). */
export function TechniciansPage() {
  const [active, setActive] = useState<"" | "true" | "false">("");
  const [territory, setTerritory] = useState("");
  const navigate = useNavigate();
  const canDispatch = holdsDuty(usePrincipalDuties(), DUTY_DISPATCH);
  const territoryId = UUID.test(territory.trim()) ? territory.trim() : "";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Technicians"
        description="The people the dispatch board schedules. A technician takes only visits that match their skills, territories and shift."
        actions={
          canDispatch ? (
            <Link className={buttonClassName("primary")} to="/service/technicians/new">
              New technician
            </Link>
          ) : undefined
        }
      />
      <div className={styles.toolbar}>
        <FormField id="tech-filter-active" label="Status">
          {(control) => (
            <Select {...control} value={active} onChange={(e) => setActive(e.target.value as "" | "true" | "false")}>
              <option value="">All technicians</option>
              <option value="true">Active</option>
              <option value="false">Inactive</option>
            </Select>
          )}
        </FormField>
        <FormField id="tech-filter-territory" label="Territory" hint="UUID; empty for all." optional>
          {(control) => <Input {...control} className={styles.mono} value={territory} onChange={(e) => setTerritory(e.target.value)} />}
        </FormField>
      </div>
      <PagedTable
        label="Technicians"
        columns={columns}
        load={(cursor, signal) => listTechnicians({ active, territoryId, cursor }, signal)}
        loadKey={`${active}|${territoryId}`}
        empty={{ title: "No technicians yet", description: "Register a technician before you schedule work orders." }}
        onRowOpen={(row) => navigate(`/service/technicians/${row.id}`)}
      />
    </div>
  );
}

export interface TechnicianValues {
  code: string;
  name: string;
  legalEntityId: string;
  territoryIds: string;
  skills: string;
  timeZone: string;
  days: number[];
  shiftStart: string;
  shiftEnd: string;
  maxDailyMinutes: string;
  overtimeAllowed: boolean;
  homeLatitude: string;
  homeLongitude: string;
  active: boolean;
}

type Errors = Record<string, string>;

export function emptyTechnician(): TechnicianValues {
  return {
    code: "",
    name: "",
    legalEntityId: "",
    territoryIds: "",
    skills: "",
    timeZone: "UTC",
    days: [1, 2, 3, 4, 5],
    shiftStart: "08:00",
    shiftEnd: "17:00",
    maxDailyMinutes: "480",
    overtimeAllowed: false,
    homeLatitude: "",
    homeLongitude: "",
    active: true,
  };
}

export function valuesOf(t: Technician): TechnicianValues {
  return {
    code: t.code,
    name: t.name,
    legalEntityId: t.legalEntityId,
    territoryIds: t.territoryIds.join("\n"),
    skills: t.skills.join(", "),
    timeZone: t.timeZone,
    days: t.shiftDays,
    shiftStart: clock(t.shiftStartMinute),
    shiftEnd: clock(t.shiftEndMinute),
    maxDailyMinutes: String(t.maxDailyMinutes),
    overtimeAllowed: t.overtimeAllowed,
    homeLatitude: t.homeLatitude == null ? "" : String(t.homeLatitude),
    homeLongitude: t.homeLongitude == null ? "" : String(t.homeLongitude),
    active: t.active,
  };
}

const list = (text: string) =>
  text
    .split(/[\s,]+/)
    .map((v) => v.trim())
    .filter(Boolean);

/** Checks that mirror the service (api/dispatch.go technicianOf). */
export function validateTechnician(values: TechnicianValues, creating: boolean): { errors: Errors; input?: TechnicianInput } {
  const errors: Errors = {};
  if (creating && !CODE.test(values.code)) errors.code = "Use 2 to 20 characters of A–Z, 0–9, _ or -.";
  const name = values.name.trim();
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter a UUID.";
  const territories = [...new Set(list(values.territoryIds))];
  if (territories.length < 1 || territories.length > 50) errors.territoryIds = "List 1 to 50 territory UUIDs.";
  else if (territories.some((t) => !UUID.test(t))) errors.territoryIds = "Every territory must be a UUID.";
  const skills = [...new Set(list(values.skills).map((s) => s.toUpperCase()))];
  if (skills.length > 40) errors.skills = "Use at most 40 skills.";
  else if (skills.some((s) => !SKILL.test(s))) errors.skills = "A skill is 1 to 40 characters of A–Z, 0–9, _, . or -.";
  if (!values.timeZone.trim() || values.timeZone.trim() === "Local") errors.timeZone = "Enter an IANA time zone, such as America/Chicago.";
  if (values.days.length === 0) errors.shiftDays = "Pick at least one working day.";
  const start = minuteOf(values.shiftStart);
  const end = minuteOf(values.shiftEnd);
  if (!(start >= 0 && start < end)) errors.shiftStartMinute = "The shift must start before it ends, as HH:MM.";
  const max = Number(values.maxDailyMinutes);
  if (!Number.isInteger(max) || max < 60 || max > 1440) errors.maxDailyMinutes = "Enter whole minutes from 60 to 1440.";
  const lat = values.homeLatitude.trim();
  const lon = values.homeLongitude.trim();
  if ((lat === "") !== (lon === "")) errors.homeLatitude = "Give both latitude and longitude, or neither.";
  else if (lat !== "" && !(Math.abs(Number(lat)) <= 90 && Math.abs(Number(lon)) <= 180 && !Number.isNaN(Number(lat)) && !Number.isNaN(Number(lon)))) {
    errors.homeLatitude = "Latitude is -90 to 90 and longitude -180 to 180.";
  }
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      ...(creating ? { code: values.code } : {}),
      name,
      legalEntityId: values.legalEntityId.trim(),
      territoryIds: territories,
      skills,
      timeZone: values.timeZone.trim(),
      shiftDays: [...values.days].sort((a, b) => a - b),
      shiftStartMinute: start,
      shiftEndMinute: end,
      homeLatitude: lat === "" ? null : Number(lat),
      homeLongitude: lon === "" ? null : Number(lon),
      maxDailyMinutes: max,
      overtimeAllowed: values.overtimeAllowed,
      active: values.active,
    },
  };
}

interface FormProps {
  creating: boolean;
  initial: TechnicianValues;
  submitLabel: string;
  onSubmit(input: TechnicianInput, key: string): Promise<void>;
  failure: Problem | null;
  fieldErrors: Errors;
  cancelTo: string;
}

function TechnicianForm({ creating, initial, submitLabel, onSubmit, failure, fieldErrors, cancelTo }: FormProps) {
  const [values, setValues] = useState<TechnicianValues>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const shown = { ...fieldErrors, ...errors };

  function update(patch: Partial<TechnicianValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateTechnician(values, creating);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    try {
      await onSubmit(input, key.current);
    } finally {
      setBusy(false);
    }
  }

  const text = (name: Exclude<keyof TechnicianValues, "days" | "overtimeAllowed" | "active">, label: string, hint?: string, extra: { mono?: boolean; optional?: boolean; errorKey?: string; type?: string; disabled?: boolean } = {}) => {
    const errorKey = extra.errorKey ?? name;
    return (
      <FormField id={`tech-${name}`} label={label} hint={hint} error={shown[errorKey]} optional={extra.optional}>
        {(control) => (
          <Input
            {...control}
            type={extra.type}
            className={extra.mono ? styles.mono : undefined}
            disabled={extra.disabled}
            value={values[name]}
            onChange={(e) => update({ [name]: name === "code" ? e.target.value.toUpperCase() : e.target.value })}
            invalid={Boolean(shown[errorKey])}
          />
        )}
      </FormField>
    );
  };

  return (
    <Card>
      <form className={styles.form} onSubmit={submit} noValidate aria-label={creating ? "New technician" : "Technician"}>
        {text("code", "Code", creating ? "For example ANA or FIELD-07." : "The code does not change.", { disabled: !creating })}
        {text("name", "Name")}
        {text("legalEntityId", "Legal entity", creating ? "UUID; visits are scheduled only within one legal entity." : "The legal entity does not change.", { mono: true, disabled: !creating })}
        <div className={styles.wide}>
          <FormField id="tech-territoryIds" label="Territories" hint="UUIDs, one per line or separated by commas." error={shown.territoryIds}>
            {(control) => (
              <Textarea {...control} className={styles.mono} rows={3} value={values.territoryIds} onChange={(e) => update({ territoryIds: e.target.value })} invalid={Boolean(shown.territoryIds)} />
            )}
          </FormField>
        </div>
        {text("skills", "Skills", "Codes such as HVAC, ELECTRICAL; separated by commas.", { optional: true })}
        {text("timeZone", "Time zone", "IANA zone the shift is read in.")}
        {text("shiftStart", "Shift starts", undefined, { type: "time", errorKey: "shiftStartMinute" })}
        {text("shiftEnd", "Shift ends", undefined, { type: "time", errorKey: "shiftStartMinute" })}
        {text("maxDailyMinutes", "Daily maximum minutes", "Work plus buffers per local day.")}
        {text("homeLatitude", "Home latitude", undefined, { optional: true })}
        {text("homeLongitude", "Home longitude", undefined, { optional: true })}
        <fieldset className={`${styles.groups} ${styles.wide}`}>
          <legend className={styles.groupsLegend}>Working days</legend>
          {DAYS.map((d) => (
            <span key={d.value} className={styles.check}>
              <Input
                id={`tech-day-${d.value}`}
                type="checkbox"
                checked={values.days.includes(d.value)}
                onChange={(e) => update({ days: e.target.checked ? [...values.days, d.value] : values.days.filter((v) => v !== d.value) })}
              />
              <label htmlFor={`tech-day-${d.value}`}>{d.label}</label>
            </span>
          ))}
          {shown.shiftDays ? (
            <p role="alert" className={styles.fieldError}>
              {shown.shiftDays}
            </p>
          ) : null}
        </fieldset>
        <div className={styles.formActions}>
          <span className={styles.check}>
            <Input id="tech-overtime" type="checkbox" checked={values.overtimeAllowed} onChange={(e) => update({ overtimeAllowed: e.target.checked })} />
            <label htmlFor="tech-overtime">Overtime allowed (up to 120 minutes)</label>
          </span>
          {!creating ? (
            <span className={styles.check}>
              <Input id="tech-active" type="checkbox" checked={values.active} onChange={(e) => update({ active: e.target.checked })} />
              <label htmlFor="tech-active">Active</label>
            </span>
          ) : null}
        </div>
        {failure ? (
          <div className={styles.formActions}>
            <Alert
              tone="danger"
              title={failure.code === "SVC-TECH-001" ? "That code is taken" : failure.code === "conflict_version" ? "Someone else changed this technician" : "Could not save the technician"}
              live="alert"
            >
              <ProblemMessage problem={failure} />
              {failure.code === "SVC-TECH-005" ? (
                <ul>
                  {Object.entries(failure.fields).map(([number, rules]) => (
                    <li key={number}>{`${number}: ${rules}`}</li>
                  ))}
                </ul>
              ) : null}
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            {submitLabel}
          </Button>
          <Link className={buttonClassName("ghost")} to={cancelTo}>
            Cancel
          </Link>
        </div>
      </form>
    </Card>
  );
}

/** Register a technician (FR-45.7). */
export function NewTechnicianPage() {
  const navigate = useNavigate();
  const canDispatch = holdsDuty(usePrincipalDuties(), DUTY_DISPATCH);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [fieldErrors, setFieldErrors] = useState<Errors>({});

  async function submit(input: TechnicianInput, key: string) {
    setFailure(null);
    setFieldErrors({});
    try {
      const created = await createTechnician(input, key);
      navigate(`/service/technicians/${created.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFieldErrors(problem.fields);
      setFailure(problem);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New technician" description="Skills, territories and the shift limit which visits the technician can take." />
      {!canDispatch ? (
        <Alert tone="info" title="You cannot register technicians">
          Registering a technician needs the {DUTY_DISPATCH} duty. <Link to="/service/technicians">Back to technicians</Link>
        </Alert>
      ) : (
      <TechnicianForm creating initial={emptyTechnician()} submitLabel="Create technician" onSubmit={submit} failure={failure} fieldErrors={fieldErrors} cancelTo="/service/technicians" />
      )}
    </div>
  );
}

/** One technician, editable at the version read; deactivating is refused while work orders are ahead. */
export function TechnicianDetailPage() {
  const { id = "" } = useParams();
  const canDispatch = holdsDuty(usePrincipalDuties(), DUTY_DISPATCH);
  const state = useLoad((signal) => getTechnician(id, signal), id);
  const [latest, setLatest] = useState<Versioned<Technician> | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [fieldErrors, setFieldErrors] = useState<Errors>({});
  const [notice, setNotice] = useState("");
  const current = latest && latest.value.id === id ? latest : state.data;

  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Technician" />
        <Alert tone="danger" title="Could not load the technician" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Technician" />
        <span className="visually-hidden">Loading the technician…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: tech, etag } = current;

  async function save(input: TechnicianInput) {
    setFailure(null);
    setFieldErrors({});
    setNotice("");
    try {
      // Editing never carries the code: validateTechnician(values, false) leaves it out.
      const next = await updateTechnician(tech.id, etag, input);
      setLatest(next);
      setNotice("Technician saved.");
    } catch (error) {
      const problem = describeProblem(error);
      setFieldErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "conflict_version") state.reload();
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Technician ${tech.code}`}
        meta={<Badge tone={tech.active ? "success" : "neutral"}>{tech.active ? "Active" : "Inactive"}</Badge>}
        description={`Legal entity ${shortId(tech.legalEntityId)}. Open the dispatch board to schedule visits.`}
        actions={
          <Link className={buttonClassName("secondary")} to="/service/dispatch">
            Dispatch board
          </Link>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {!canDispatch ? (
        <Card title="Technician" headingLevel={3}>
          <Details
            label="Technician"
            items={[
              ["Name", tech.name],
              ["Skills", tech.skills.length > 0 ? tech.skills.join(", ") : dash],
              ["Territories", tech.territoryIds.join(", "), true],
              ["Time zone", tech.timeZone],
              ["Shift", `${daysText(tech.shiftDays)} ${clock(tech.shiftStartMinute)}–${clock(tech.shiftEndMinute)}`],
              ["Daily maximum", duration(tech.maxDailyMinutes)],
              ["Overtime", tech.overtimeAllowed ? "Allowed (up to 120 minutes)" : "Not allowed"],
            ]}
          />
        </Card>
      ) : (
      <TechnicianForm
        key={`${tech.id}-${tech.version}`}
        creating={false}
        initial={valuesOf(tech)}
        submitLabel="Save technician"
        onSubmit={save}
        failure={failure}
        fieldErrors={fieldErrors}
        cancelTo="/service/technicians"
      />
      )}
    </div>
  );
}
