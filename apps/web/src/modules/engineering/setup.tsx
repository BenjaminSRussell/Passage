import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Alert, Button, Card, FormField, Input, PageHeader, Select, type FormFieldControlProps } from "../../shell/ui";
import {
  ApiError,
  createCalendar,
  createCapability,
  createResource,
  createResourceGroup,
  generateCalendar,
  type CalendarGeneration,
  type ResourceCapability,
  type Shift,
} from "./api/client";
import { errorText } from "./screens";
import styles from "./pages.module.css";
import { EntityLookup } from "../../shell/lookup/EntityLookup";

const forbiddenStatus = 403;
const weekdays = ["Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"];
const resourceTypes = ["machine", "tool", "labor", "vendor", "location"];

interface Failure {
  form: string;
  fields: Record<string, string>;
}

const noFailure: Failure = { form: "", fields: {} };

/** Service problems that belong to one field of the form rather than the whole request. */
const codeFields: Record<string, string> = {
  "ENG-DUPLICATE": "code",
  "ENG-RES-VENDOR": "vendorPartyId",
  "ENG-RES-SITE": "groupId",
  "ENG-CAL-NOTFOUND": "calendarId",
};
const duplicateText = "Another record already uses this code, or this request is still being saved.";

/**
 * Turns a failed call into form and field messages. A field message for a key the form does not
 * show (such as template) is added to the form message so it is never lost.
 */
function failure(error: unknown, action: string, shown: readonly string[]): Failure {
  if (!(error instanceof ApiError)) return { form: errorText(error), fields: {} };
  if (error.status === forbiddenStatus) return { form: `You do not have permission to ${action}.`, fields: {} };
  const fields: Record<string, string> = {};
  const hidden: string[] = [];
  const target = error.code ? codeFields[error.code] : undefined;
  const all = target ? { ...error.errors, [target]: error.code === "ENG-DUPLICATE" ? duplicateText : error.message } : error.errors;
  for (const [key, text] of Object.entries(all)) {
    if (shown.includes(key) || /^capabilities\[\d+\]$/.test(key)) fields[key] = text;
    else hidden.push(`${key} ${text}`);
  }
  const moved = target !== undefined && shown.includes(target);
  return { form: [moved ? "" : error.message, ...hidden].filter(Boolean).join("; "), fields };
}

/**
 * Submit state shared by the create forms: one Idempotency-Key per request body, so a retry of the
 * same body after a lost response replays instead of creating a second record, and a changed body
 * gets a new key instead of an idempotency_key_reuse rejection. After a failed submit, focus moves
 * to the first field marked invalid so keyboard and screen reader users land on it; a form-level
 * reason is announced by the alert.
 */
function useCreate<B, T>(action: string, shown: readonly string[], create: (body: B, key: string) => Promise<T>, onDone?: (created: T) => void) {
  const formRef = useRef<HTMLFormElement>(null);
  const attempt = useRef({ body: "", key: "" });
  const [submitting, setSubmitting] = useState(false);
  const [failed, setFailed] = useState<Failure>(noFailure);

  useEffect(() => {
    if (Object.keys(failed.fields).length > 0) formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
  }, [failed]);

  async function submit(body: B) {
    const text = JSON.stringify(body);
    if (attempt.current.body !== text) attempt.current = { body: text, key: crypto.randomUUID() };
    setSubmitting(true);
    setFailed(noFailure);
    try {
      const created = await create(body, attempt.current.key);
      setSubmitting(false);
      onDone?.(created);
    } catch (e) {
      setSubmitting(false);
      setFailed(failure(e, action, shown));
    }
  }

  /** Shows messages found before calling the service; returns true when there were any. */
  function reject(fields: Record<string, string | undefined>): boolean {
    const found = Object.fromEntries(Object.entries(fields).filter((entry): entry is [string, string] => Boolean(entry[1])));
    if (Object.keys(found).length === 0) return false;
    setFailed({ form: "", fields: found });
    return true;
  }
  return { formRef, submit, reject, submitting, failed };
}

const decimalPattern = (digits: number, decimals: number) => new RegExp(`^\\d{1,${digits}}(\\.\\d{1,${decimals}})?$`);
const efficiencyPattern = decimalPattern(3, 2);
const capacityPattern = decimalPattern(6, 4);
const datePattern = /^\d{4}-\d{2}-\d{2}$/;

/** The service's efficiency rule: above 0 and at most 100 with at most two decimals. */
function efficiencyError(text: string, low = 0): string | undefined {
  if (!text) return undefined;
  const value = Number(text);
  return efficiencyPattern.test(text) && value > low && value <= 100 ? undefined : `Enter a number ${low === 0 ? "above 0" : `from ${low}`} to 100 with at most two decimals.`;
}

function isDate(text: string): boolean {
  return datePattern.test(text) && !Number.isNaN(Date.parse(`${text}T00:00:00Z`)) && new Date(`${text}T00:00:00Z`).toISOString().startsWith(text);
}

function Actions({ label, submitting, onCancel, error }: { label: string; submitting: boolean; onCancel?: () => void; error: string }) {
  return (
    <>
      <div className={styles.toolbar}>
        <Button type="submit" variant="primary" loading={submitting}>
          {label}
        </Button>
        {onCancel ? <Button onClick={onCancel}>Cancel</Button> : null}
      </div>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert">
          {error || null}
        </Alert>
      </div>
    </>
  );
}

function Field({ id, label, error, optional, hint, children }: { id: string; label: string; error?: string; optional?: boolean; hint?: string; children: (control: FormFieldControlProps) => ReactNode }) {
  return (
    <div className={styles.field}>
      <FormField id={id} label={label} error={error} optional={optional} hint={hint}>
        {children}
      </FormField>
    </div>
  );
}


type Props = { onCreated?: (id: string) => void; onCancel?: () => void };

const capabilityFields = ["code", "name"] as const;

export function CapabilityCreatePage({ onCreated, onCancel }: Props) {
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const { formRef, submit, submitting, failed } = useCreate("create capabilities", capabilityFields, createCapability, (created) => onCreated?.(created.id));
  function save(event: FormEvent) {
    event.preventDefault();
    void submit({ code: code.trim(), name: name.trim() });
  }
  return (
    <>
      <PageHeader title="New capability" description="A skill or ability that resources can hold and routings can require." />
      <Card>
        <form ref={formRef} className={styles.form} onSubmit={save}>
          <Field id="new-cap-code" label="Code" error={failed.fields.code}>
            {(control) => <Input {...control} value={code} onChange={(e) => setCode(e.target.value)} required maxLength={64} />}
          </Field>
          <Field id="new-cap-name" label="Name" error={failed.fields.name}>
            {(control) => <Input {...control} value={name} onChange={(e) => setName(e.target.value)} required maxLength={200} />}
          </Field>
          <Actions label="Create capability" submitting={submitting} onCancel={onCancel} error={failed.form} />
        </form>
      </Card>
    </>
  );
}

const groupFields = ["code", "name", "siteId", "calendarId"] as const;

export function ResourceGroupCreatePage({ onCreated, onCancel }: Props) {
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [siteId, setSiteId] = useState("");
  const [calendarId, setCalendarId] = useState("");
  const { formRef, submit, submitting, failed } = useCreate("create resource groups", groupFields, createResourceGroup, (created) => onCreated?.(created.id));
  function save(event: FormEvent) {
    event.preventDefault();
    void submit({ code: code.trim(), name: name.trim(), siteId: siteId.trim(), ...(calendarId.trim() ? { calendarId: calendarId.trim() } : {}) });
  }
  return (
    <>
      <PageHeader title="New resource group" description="Resources at one site that share a calendar, such as a machining cell." />
      <Card>
        <form ref={formRef} className={styles.form} onSubmit={save}>
          <Field id="new-group-code" label="Code" error={failed.fields.code}>
            {(control) => <Input {...control} value={code} onChange={(e) => setCode(e.target.value)} required maxLength={64} />}
          </Field>
          <Field id="new-group-name" label="Name" error={failed.fields.name}>
            {(control) => <Input {...control} value={name} onChange={(e) => setName(e.target.value)} required maxLength={200} />}
          </Field>
          <Field id="new-group-site" label="Site" error={failed.fields.siteId}>
            {(control) => <Input {...control} value={siteId} onChange={(e) => setSiteId(e.target.value)} required maxLength={64} />}
          </Field>
          <Field id="new-group-calendar" label="Calendar" optional error={failed.fields.calendarId} hint="The group's resources use this calendar unless they name their own.">
            {(control) => <EntityLookup {...control} entity="calendar" value={calendarId} invalid={Boolean(failed.fields.calendarId)} onChange={(e) => setCalendarId(e.target.value)} />}
          </Field>
          <Actions label="Create resource group" submitting={submitting} onCancel={onCancel} error={failed.form} />
        </form>
      </Card>
    </>
  );
}

interface CapabilityRow {
  key: number;
  capabilityId: string;
  level: string;
  effectiveFrom: string;
  effectiveTo: string;
}

const resourceFields = ["code", "name", "type", "siteId", "groupId", "calendarId", "efficiencyPct", "capacityPerDayH"] as const;

export function ResourceCreatePage({ onCreated, onCancel }: Props) {
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [type, setType] = useState("machine");
  const [siteId, setSiteId] = useState("");
  const [groupId, setGroupId] = useState("");
  const [calendarId, setCalendarId] = useState("");
  const [efficiency, setEfficiency] = useState("");
  const [capacity, setCapacity] = useState("");
  const [vendor, setVendor] = useState("");
  const [exclusive, setExclusive] = useState(false);
  const [rows, setRows] = useState<CapabilityRow[]>([]);
  const nextKey = useRef(0);
  const shown = type === "vendor" ? [...resourceFields, "vendorPartyId"] : resourceFields;
  const { formRef, submit, reject, submitting, failed } = useCreate("create resources", shown, createResource, (created) => onCreated?.(created.id));

  const update = (key: number, patch: Partial<CapabilityRow>) => setRows((existing) => existing.map((row) => (row.key === key ? { ...row, ...patch } : row)));

  function save(event: FormEvent) {
    event.preventDefault();
    const efficiencyPct = efficiency.trim();
    const capacityPerDayH = capacity.trim();
    const levels = Object.fromEntries(rows.map((row, i) => [`level-${i}`, /^[1-9]\d{0,8}$/.test(row.level.trim()) ? undefined : "Enter a whole number of at least 1."]));
    if (
      reject({
        efficiencyPct: efficiencyError(efficiencyPct),
        capacityPerDayH: capacityPerDayH && !capacityPattern.test(capacityPerDayH) ? "Enter hours from 0 to below 1000000 with at most four decimals." : undefined,
        ...levels,
      })
    )
      return;
    const capabilities: ResourceCapability[] = rows.map((row) => ({
      capabilityId: row.capabilityId.trim(),
      level: Number(row.level.trim()),
      effectiveFrom: row.effectiveFrom,
      ...(row.effectiveTo ? { effectiveTo: row.effectiveTo } : {}),
    }));
    void submit({
      code: code.trim(),
      name: name.trim(),
      type,
      siteId: siteId.trim(),
      exclusive,
      capabilities,
      ...(groupId.trim() ? { groupId: groupId.trim() } : {}),
      ...(calendarId.trim() ? { calendarId: calendarId.trim() } : {}),
      ...(efficiencyPct ? { efficiencyPct } : {}),
      ...(capacityPerDayH ? { capacityPerDayH } : {}),
      ...(type === "vendor" ? { vendorPartyId: vendor.trim() } : {}),
    });
  }

  return (
    <>
      <PageHeader title="New resource" description="A machine, tool, labor pool, vendor or location that routings can load." />
      <Card>
        <form ref={formRef} className={styles.form} onSubmit={save}>
          <Field id="new-res-code" label="Code" error={failed.fields.code}>
            {(control) => <Input {...control} value={code} onChange={(e) => setCode(e.target.value)} required maxLength={64} />}
          </Field>
          <Field id="new-res-name" label="Name" error={failed.fields.name}>
            {(control) => <Input {...control} value={name} onChange={(e) => setName(e.target.value)} required maxLength={200} />}
          </Field>
          <Field id="new-res-type" label="Type" error={failed.fields.type}>
            {(control) => (
              <Select {...control} value={type} onChange={(e) => setType(e.target.value)}>
                {resourceTypes.map((t) => (
                  <option key={t} value={t}>
                    {t}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field id="new-res-site" label="Site" error={failed.fields.siteId}>
            {(control) => <Input {...control} value={siteId} onChange={(e) => setSiteId(e.target.value)} required maxLength={64} />}
          </Field>
          {type === "vendor" ? (
            <Field id="new-res-vendor" label="Vendor party" error={failed.fields.vendorPartyId} hint="The party the work is subcontracted to.">
              {(control) => <Input {...control} value={vendor} onChange={(e) => setVendor(e.target.value)} required maxLength={64} />}
            </Field>
          ) : null}
          <Field id="new-res-efficiency" label="Efficiency %" optional error={failed.fields.efficiencyPct} hint="Above 0 to 100; 100 when empty.">
            {(control) => <Input {...control} inputMode="decimal" value={efficiency} onChange={(e) => setEfficiency(e.target.value)} />}
          </Field>
          <Field id="new-res-capacity" label="Capacity per day (h)" optional error={failed.fields.capacityPerDayH} hint="0 when empty.">
            {(control) => <Input {...control} inputMode="decimal" value={capacity} onChange={(e) => setCapacity(e.target.value)} />}
          </Field>
          <Field id="new-res-group" label="Resource group" optional error={failed.fields.groupId} hint="A resource group at the same site.">
            {(control) => <EntityLookup {...control} entity="resourceGroup" value={groupId} invalid={Boolean(failed.fields.groupId)} onChange={(e) => setGroupId(e.target.value)} />}
          </Field>
          <Field id="new-res-calendar" label="Calendar" optional error={failed.fields.calendarId} hint="Leave empty to use the group's calendar.">
            {(control) => <EntityLookup {...control} entity="calendar" value={calendarId} invalid={Boolean(failed.fields.calendarId)} onChange={(e) => setCalendarId(e.target.value)} />}
          </Field>
          <span className={styles.check}>
            <Input id="new-res-exclusive" type="checkbox" checked={exclusive} onChange={(e) => setExclusive(e.target.checked)} />
            <label htmlFor="new-res-exclusive">Exclusive</label>
          </span>
          {rows.map((row, index) => (
            <fieldset key={row.key} className={styles.fieldset}>
              <legend className={styles.legend}>Capability {index + 1}</legend>
              <Field id={`new-res-cap-${row.key}`} label={`Capability id ${index + 1}`} error={failed.fields[`capabilities[${index}]`]}>
                {(control) => <Input {...control} value={row.capabilityId} onChange={(e) => update(row.key, { capabilityId: e.target.value })} required />}
              </Field>
              <Field id={`new-res-level-${row.key}`} label={`Level ${index + 1}`} error={failed.fields[`level-${index}`]}>
                {(control) => <Input {...control} type="number" min="1" step="1" value={row.level} onChange={(e) => update(row.key, { level: e.target.value })} required />}
              </Field>
              <Field id={`new-res-from-${row.key}`} label={`Effective from ${index + 1}`}>
                {(control) => <Input {...control} type="date" value={row.effectiveFrom} onChange={(e) => update(row.key, { effectiveFrom: e.target.value })} required />}
              </Field>
              <Field id={`new-res-to-${row.key}`} label={`Effective to ${index + 1}`} optional>
                {(control) => <Input {...control} type="date" value={row.effectiveTo} onChange={(e) => update(row.key, { effectiveTo: e.target.value })} />}
              </Field>
              <Button size="sm" onClick={() => setRows((existing) => existing.filter((r) => r.key !== row.key))}>
                Remove capability {index + 1}
              </Button>
            </fieldset>
          ))}
          <div className={styles.toolbar}>
            <Button onClick={() => setRows((existing) => [...existing, { key: nextKey.current++, capabilityId: "", level: "1", effectiveFrom: "", effectiveTo: "" }])}>Add capability</Button>
          </div>
          <Actions label="Create resource" submitting={submitting} onCancel={onCancel} error={failed.form} />
        </form>
      </Card>
    </>
  );
}

const minutesPerDay = 1440;
const maxShiftsPerDay = 5;
const rangePattern = /^(\d{2}:\d{2})\s*-\s*(\d{2}:\d{2})(?:\s*@\s*(\S+))?$/;
const clockPattern = /^([0-2]\d):([0-5]\d)$/;

function clockMinutes(text: string): number | null {
  const match = clockPattern.exec(text);
  const value = match ? Number(match[1]) * 60 + Number(match[2]) : Number.NaN;
  return value <= minutesPerDay ? value : null;
}

/** A shift as minutes from the day's midnight; an end before the start runs past midnight. */
function span(shift: Shift): [number, number] {
  const start = clockMinutes(shift.start) ?? 0;
  const end = clockMinutes(shift.end) ?? 0;
  return [start, end < start ? end + minutesPerDay : end];
}

const clock = (minutes: number) => `${String(Math.floor(minutes / 60)).padStart(2, "0")}:${String(minutes % 60).padStart(2, "0")}`;

export type ParsedDay = { shifts: Shift[]; error?: undefined } | { shifts?: undefined; error: string };

/**
 * Reads one day pattern such as "08:00-12:00, 13:00-17:00 @ 90" into shifts, with the service's
 * rules (ENG-CAL-SHIFT): hh:mm times, 24:00 only as an end, an end before the start runs past
 * midnight, no zero-length or overlapping shifts, at most five, and an optional efficiency of 1 to
 * 100 after @. Empty text is a closed day.
 */
export function parseDay(text: string): ParsedDay {
  const parts = text.split(",").map((part) => part.trim()).filter(Boolean);
  if (parts.length > maxShiftsPerDay) return { error: `A day holds at most ${maxShiftsPerDay} shifts.` };
  const shifts: Shift[] = [];
  for (const part of parts) {
    const match = rangePattern.exec(part);
    if (!match) return { error: `"${part}" is not a range such as 08:00-12:00.` };
    const [, start, end, efficiency] = match;
    const startMinutes = clockMinutes(start);
    const endMinutes = clockMinutes(end);
    if (startMinutes === null || endMinutes === null) return { error: `${part} uses a time that does not exist; use 00:00 to 24:00.` };
    if (startMinutes === minutesPerDay) return { error: `${part} starts at 24:00; start it at 00:00 instead.` };
    if (startMinutes === endMinutes) return { error: `${part} has no length.` };
    const efficiencyProblem = efficiency === undefined ? undefined : efficiencyError(efficiency, 1);
    if (efficiencyProblem) return { error: `${part}: efficiency after @ must be 1 to 100 with at most two decimals.` };
    shifts.push({ start, end, ...(efficiency === undefined ? {} : { efficiencyPct: efficiency }) });
  }
  const spans = shifts.map(span).sort((a, b) => a[0] - b[0]);
  for (let i = 1; i < spans.length; i++) {
    if (spans[i - 1][1] > spans[i][0]) return { error: `Shifts overlap at ${clock(spans[i][0])}.` };
  }
  return { shifts };
}

/** Parses the seven day patterns (Monday first) and rejects a shift that runs past midnight into the next day's shifts. */
export function parseWeek(days: readonly string[]): { template: Shift[][]; errors: Record<string, string> } {
  const errors: Record<string, string> = {};
  const template = days.map((text, i) => {
    const parsed = parseDay(text);
    if (parsed.error !== undefined) errors[`day-${i}`] = parsed.error;
    return parsed.shifts ?? [];
  });
  template.forEach((shifts, i) => {
    const spill = Math.max(0, ...shifts.map((shift) => span(shift)[1] - minutesPerDay));
    const next = (i + 1) % template.length;
    const hit = template[next].find((shift) => span(shift)[0] < spill);
    if (hit && !errors[`day-${i}`]) errors[`day-${i}`] = `A shift running to ${clock(spill)} the next morning overlaps the ${weekdays[next]} shift ${hit.start}-${hit.end}.`;
  });
  return { template, errors };
}

const calendarFields = ["code", "name", "closedDates", ...weekdays.map((_, i) => `day-${i}`)];

export function CalendarCreatePage({ onCreated, onCancel }: Props) {
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [days, setDays] = useState<string[]>(["08:00-16:00", "08:00-16:00", "08:00-16:00", "08:00-16:00", "08:00-16:00", "", ""]);
  const [closed, setClosed] = useState("");
  const { formRef, submit, reject, submitting, failed } = useCreate("create calendars", calendarFields, createCalendar, (created) => onCreated?.(created.id));

  function save(event: FormEvent) {
    event.preventDefault();
    const { template, errors } = parseWeek(days);
    const closedDates = closed.split(/[\s,]+/).filter(Boolean);
    const badDate = closedDates.find((d) => !isDate(d));
    if (reject({ ...errors, closedDates: badDate ? `${badDate} is not a date as YYYY-MM-DD.` : undefined })) return;
    void submit({ code: code.trim(), name: name.trim(), template, closedDates });
  }

  return (
    <>
      <PageHeader title="New work calendar" description="A weekly shift template and closed dates. Generate its days from the calendar screen." />
      <Card>
        <form ref={formRef} className={styles.form} onSubmit={save}>
          <Field id="new-cal-code" label="Code" error={failed.fields.code}>
            {(control) => <Input {...control} value={code} onChange={(e) => setCode(e.target.value)} required maxLength={64} />}
          </Field>
          <Field id="new-cal-name" label="Name" error={failed.fields.name}>
            {(control) => <Input {...control} value={name} onChange={(e) => setName(e.target.value)} required maxLength={200} />}
          </Field>
          {weekdays.map((day, i) => (
            <Field
              key={day}
              id={`new-cal-${day.toLowerCase()}`}
              label={day}
              optional
              error={failed.fields[`day-${i}`]}
              hint={i === 0 ? "Shifts as start-end, comma separated, with an optional efficiency such as 22:00-06:00 @ 90; an end before the start runs past midnight; empty is a closed day." : undefined}
            >
              {(control) => <Input {...control} value={days[i]} onChange={(e) => setDays((existing) => existing.map((d, j) => (j === i ? e.target.value : d)))} />}
            </Field>
          ))}
          <Field id="new-cal-closed" label="Closed dates" optional error={failed.fields.closedDates} hint="Dates as YYYY-MM-DD separated by commas or spaces.">
            {(control) => <Input {...control} value={closed} onChange={(e) => setClosed(e.target.value)} />}
          </Field>
          <Actions label="Create calendar" submitting={submitting} onCancel={onCancel} error={failed.form} />
        </form>
      </Card>
    </>
  );
}

/** Generates a calendar's days for a date range and reports how many open days and hours it produced. */
export function GenerateDays({ calendarId, onGenerated }: { calendarId: string; onGenerated?: () => void }) {
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<CalendarGeneration | null>(null);

  async function generate(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    setResult(null);
    try {
      setResult(await generateCalendar(calendarId, { from, to }));
      onGenerated?.();
    } catch (e) {
      const failed = failure(e, "generate calendar days", []);
      setError(failed.form);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Generate days">
      <form className={styles.form} onSubmit={generate}>
        <Field id="gen-from" label="From">
          {(control) => <Input {...control} type="date" value={from} onChange={(e) => setFrom(e.target.value)} required />}
        </Field>
        <Field id="gen-to" label="To" hint="Up to 60 months after From.">
          {(control) => <Input {...control} type="date" value={to} min={from || undefined} onChange={(e) => setTo(e.target.value)} required />}
        </Field>
        <Button type="submit" variant="primary" loading={busy}>
          Generate days
        </Button>
      </form>
      <div className={styles.summary} role="status">
        {result ? `Generated ${result.openDays} open days, ${result.totalHours} hours.` : null}
      </div>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert">
          {error || null}
        </Alert>
      </div>
    </Card>
  );
}
