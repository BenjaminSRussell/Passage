import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Textarea } from "../../shell/ui";
import { createCapacityBlock, describeProblem, newKey, PREDICATE_OPS, type NewCapacityBlock, type Predicate, type PredicateOp, type Problem } from "./api/client";
import { clock, fromUtcInput, parseClock, WEEK_ORDER, WEEKDAYS } from "./format";
import { CODE, DATE, mapFields, optionalWhole } from "./forms";
import styles from "./pages.module.css";
import { ResourceGate } from "./ResourceGate";
import { ProblemMessage } from "../../shell/errors";

export interface BlockValues {
  resourceId: string;
  code: string;
  kind: "weekly" | "dated" | "recurring";
  days: number[];
  start: string;
  end: string;
  startAt: string;
  endAt: string;
  /** Recurring blocks: the RRULE subset and the first local date. */
  rule: string;
  startDate: string;
  condition: string;
  priority: string;
  active: boolean;
}

type Errors = Partial<Record<"resourceId" | "code" | "days" | "start" | "end" | "startAt" | "endAt" | "rule" | "startDate" | "condition" | "priority", string>>;

const RULE_PARTS = new Set(["FREQ", "INTERVAL", "BYDAY", "COUNT", "UNTIL"]);
const WEEKDAY_CODES = new Set(["MO", "TU", "WE", "TH", "FR", "SA", "SU"]);

/**
 * Checks the RRULE subset the scheduler expands (FREQ=DAILY|WEEKLY, INTERVAL,
 * BYDAY, COUNT or UNTIL); returns an error message or null. The service
 * re-checks it.
 */
export function ruleError(rule: string): string | null {
  const text = rule.trim().replace(/^RRULE:/, "");
  if (text === "") return "Enter a rule, such as FREQ=WEEKLY;BYDAY=MO.";
  const parts = new Map<string, string>();
  for (const part of text.split(";")) {
    const [name, value] = part.split("=");
    if (!name || !value || !RULE_PARTS.has(name)) return `"${part}" is not supported; use FREQ, INTERVAL, BYDAY, COUNT or UNTIL.`;
    if (parts.has(name)) return `${name} is given twice.`;
    parts.set(name, value);
  }
  const freq = parts.get("FREQ");
  if (freq !== "DAILY" && freq !== "WEEKLY") return "FREQ must be DAILY or WEEKLY.";
  const byDay = parts.get("BYDAY");
  if (byDay !== undefined) {
    if (freq !== "WEEKLY") return "BYDAY needs FREQ=WEEKLY.";
    if (!byDay.split(",").every((d) => WEEKDAY_CODES.has(d))) return "BYDAY takes weekday codes such as MO,WE,FR.";
  }
  if (parts.has("COUNT") && parts.has("UNTIL")) return "Give COUNT or UNTIL, not both.";
  for (const name of ["INTERVAL", "COUNT"]) {
    const v = parts.get(name);
    if (v !== undefined && !/^[1-9]\d*$/.test(v)) return `${name} must be a positive whole number.`;
  }
  const until = parts.get("UNTIL");
  if (until !== undefined && !/^\d{8}/.test(until)) return "UNTIL must start with a date such as 20271231.";
  return null;
}

/** "Every week on MO from 2027-01-04, 06:00–14:00" style summary. */
export function describeRecurrence(r: { rule: string; startDate: string; startMinute: number; endMinute: number }): string {
  return `${r.rule} from ${r.startDate}, ${clock(r.startMinute)}–${clock(r.endMinute)}`;
}

const NUMERIC = /^-?\d+(\.\d+)?$/;
// Longest operators first so ">=" is not read as ">".
const OPS = [...PREDICATE_OPS].sort((a, b) => b.length - a.length);

/** Reads one predicate per line, such as "color = white" or "grade >= 2". */
export function parseCondition(text: string): { condition: Predicate[]; error?: string } {
  const condition: Predicate[] = [];
  for (const [i, raw] of text.split("\n").entries()) {
    const line = raw.trim();
    if (line === "") continue;
    const at = `Line ${i + 1}`;
    const op = OPS.find((o) => line.includes(o)) as PredicateOp | undefined;
    if (!op) return { condition, error: `${at}: use attribute, an operator (${PREDICATE_OPS.join(" ")}) and a value.` };
    const idx = line.indexOf(op);
    const attribute = line.slice(0, idx).trim();
    const value = line.slice(idx + op.length).trim();
    if (attribute === "" || value === "") return { condition, error: `${at}: name the attribute and the value.` };
    if (attribute.length > 64 || value.length > 64) return { condition, error: `${at}: attributes and values are at most 64 characters.` };
    if (op !== "=" && op !== "!=" && !NUMERIC.test(value)) return { condition, error: `${at}: ${op} compares numbers; the value must be numeric.` };
    condition.push({ attribute, op, value });
  }
  if (condition.length > 20) return { condition, error: "Use at most 20 predicates." };
  return { condition };
}

/** Checks that mirror the service, and the request the form sends. */
export function validateBlock(values: BlockValues): { errors: Errors; input?: NewCapacityBlock } {
  const errors: Errors = {};
  if (values.resourceId === "") errors.resourceId = "Choose the resource.";
  if (!CODE.test(values.code)) errors.code = "Use 1 to 32 capital letters, digits, _ or -.";
  const input: NewCapacityBlock = { resourceId: values.resourceId, code: values.code, condition: [], priority: 0, active: values.active };
  if (values.kind === "weekly" || values.kind === "recurring") {
    const start = parseClock(values.start);
    const end = values.end === "00:00" ? 1440 : parseClock(values.end);
    if (start === null) errors.start = "Enter a start time.";
    if (end === null) errors.end = "Enter an end time.";
    else if (start !== null && end <= start) errors.end = "The window must end after it starts.";
    if (values.kind === "weekly") {
      if (values.days.length === 0) errors.days = "Choose at least one weekday.";
      if (start !== null && end !== null) input.windows = values.days.map((weekday) => ({ weekday, startMinute: start, endMinute: end }));
    } else {
      const rule = values.rule.trim().toUpperCase();
      const problem = ruleError(rule);
      if (problem) errors.rule = problem;
      if (!DATE.test(values.startDate)) errors.startDate = "Enter the first date.";
      if (start !== null && end !== null) input.recurrence = { rule, startDate: values.startDate, startMinute: start, endMinute: end };
    }
  } else {
    const startAt = fromUtcInput(values.startAt);
    const endAt = fromUtcInput(values.endAt);
    if (startAt === null) errors.startAt = "Enter a date and time.";
    if (endAt === null) errors.endAt = "Enter a date and time.";
    else if (startAt !== null && Date.parse(endAt) <= Date.parse(startAt)) errors.endAt = "The block must end after it starts.";
    if (startAt !== null && endAt !== null) {
      input.startAt = startAt;
      input.endAt = endAt;
    }
  }
  const parsed = parseCondition(values.condition);
  if (parsed.error) errors.condition = parsed.error;
  input.condition = parsed.condition;
  const priority = optionalWhole(values.priority, 0, 1000);
  if (priority === null) errors.priority = "Enter 0 to 1000.";
  else input.priority = priority ?? 0;
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input };
}

const fieldNames: Record<string, keyof Errors> = { resourceId: "resourceId", code: "code", windows: "days", recurrence: "rule", condition: "condition", priority: "priority" };

/** Reserve weekly or dated time on a resource for matching jobs. */
export function NewCapacityBlockPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<BlockValues>({
    resourceId: "",
    code: "",
    kind: "weekly",
    days: [1],
    start: "06:00",
    end: "14:00",
    startAt: "",
    endAt: "",
    rule: "FREQ=WEEKLY;BYDAY=MO",
    startDate: "",
    condition: "",
    priority: "",
    active: true,
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<BlockValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  function toggleDay(day: number) {
    update({ days: values.days.includes(day) ? values.days.filter((d) => d !== day) : [...values.days, day].sort() });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateBlock(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createCapacityBlock(input, key.current);
      navigate("/scheduling/capacity-blocks");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors = mapFields(problem.fields, fieldNames);
      if (problem.code === "SCHED-0112") fieldErrors.code = "This resource already has a block with that code.";
      if (problem.code === "SCHED-0107") fieldErrors.resourceId = "This resource no longer exists.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New capacity block" description="A job can use a block only when its attributes meet every condition of the block; other jobs use unblocked capacity." />
      <Card>
        <ResourceGate need="A block reserves time on one resource.">
          {(resources) => (
            <form className={styles.form} onSubmit={submit} noValidate>
              <FormField id="block-resource" label="Resource" error={errors.resourceId}>
                {(control) => (
                  <Select {...control} value={values.resourceId} onChange={(event) => update({ resourceId: event.target.value })} invalid={Boolean(errors.resourceId)}>
                    <option value="">Choose a resource</option>
                    {resources.map((r) => (
                      <option key={r.id} value={r.id}>
                        {`${r.code} · ${r.name}`}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="block-code" label="Code" hint="For example MON-WHITE." error={errors.code}>
                {(control) => <Input {...control} maxLength={32} value={values.code} onChange={(event) => update({ code: event.target.value.toUpperCase() })} invalid={Boolean(errors.code)} />}
              </FormField>
              <FormField id="block-kind" label="Repeats">
                {(control) => (
                  <Select {...control} value={values.kind} onChange={(event) => update({ kind: event.target.value as BlockValues["kind"] })}>
                    <option value="weekly">Every week</option>
                    <option value="recurring">By a recurrence rule</option>
                    <option value="dated">Once, on dates</option>
                  </Select>
                )}
              </FormField>
              <FormField id="block-priority" label="Priority" optional hint="0 to 1000; higher wins where blocks overlap." error={errors.priority}>
                {(control) => <Input {...control} inputMode="numeric" value={values.priority} onChange={(event) => update({ priority: event.target.value })} invalid={Boolean(errors.priority)} />}
              </FormField>
              {values.kind === "recurring" ? (
                <>
                  <FormField
                    id="block-rule"
                    label="Rule"
                    hint="FREQ=DAILY or WEEKLY, with INTERVAL, BYDAY and COUNT or UNTIL. For example FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE."
                    error={errors.rule}
                  >
                    {(control) => <Input {...control} spellCheck={false} value={values.rule} onChange={(event) => update({ rule: event.target.value.toUpperCase() })} invalid={Boolean(errors.rule)} />}
                  </FormField>
                  <FormField id="block-start-date" label="First date" hint="Site-local date the rule starts counting from." error={errors.startDate}>
                    {(control) => <Input {...control} type="date" value={values.startDate} onChange={(event) => update({ startDate: event.target.value })} invalid={Boolean(errors.startDate)} />}
                  </FormField>
                </>
              ) : null}
              {values.kind !== "dated" ? (
                <>
                  <FormField id="block-start" label="Window start" hint="Site-local time of the resource." error={errors.start}>
                    {(control) => <Input {...control} type="time" value={values.start} onChange={(event) => update({ start: event.target.value })} invalid={Boolean(errors.start)} />}
                  </FormField>
                  <FormField id="block-end" label="Window end" hint="00:00 means midnight at the end of the day." error={errors.end}>
                    {(control) => <Input {...control} type="time" value={values.end} onChange={(event) => update({ end: event.target.value })} invalid={Boolean(errors.end)} />}
                  </FormField>
                </>
              ) : null}
              {values.kind === "weekly" ? (
                <>
                  <fieldset className={styles.groups} aria-describedby={errors.days ? "block-days-error" : undefined}>
                    <legend className={styles.groupsLegend}>Weekdays</legend>
                    {WEEK_ORDER.map((day) => (
                      <span key={day} className={styles.check}>
                        <Input type="checkbox" id={`block-day-${day}`} checked={values.days.includes(day)} onChange={() => toggleDay(day)} />
                        <label htmlFor={`block-day-${day}`}>{WEEKDAYS[day]}</label>
                      </span>
                    ))}
                    {errors.days ? (
                      <p id="block-days-error" role="alert" className={styles.fieldError}>
                        {errors.days}
                      </p>
                    ) : null}
                  </fieldset>
                </>
              ) : (
                <>
                  <FormField id="block-start-at" label="Starts (UTC)" error={errors.startAt}>
                    {(control) => <Input {...control} type="datetime-local" value={values.startAt} onChange={(event) => update({ startAt: event.target.value })} invalid={Boolean(errors.startAt)} />}
                  </FormField>
                  <FormField id="block-end-at" label="Ends (UTC)" error={errors.endAt}>
                    {(control) => <Input {...control} type="datetime-local" value={values.endAt} onChange={(event) => update({ endAt: event.target.value })} invalid={Boolean(errors.endAt)} />}
                  </FormField>
                </>
              )}
              <div className={styles.wide}>
                <FormField
                  id="block-condition"
                  label="Condition"
                  optional
                  hint="One predicate per line, such as color = white or grade >= 2. Leave blank to admit every job."
                  error={errors.condition}
                >
                  {(control) => <Textarea {...control} rows={4} value={values.condition} onChange={(event) => update({ condition: event.target.value })} invalid={Boolean(errors.condition)} />}
                </FormField>
              </div>
              <div className={styles.formActions}>
                <span className={styles.check}>
                  <Input id="block-active" type="checkbox" checked={values.active} onChange={(event) => update({ active: event.target.checked })} />
                  <label htmlFor="block-active">Active</label>
                </span>
              </div>
              {failure ? (
                <div className={styles.formActions}>
                  <Alert tone="danger" title="Could not create the block" live="alert">
                    <ProblemMessage problem={failure} />
                  </Alert>
                </div>
              ) : null}
              <div className={styles.formActions}>
                <Button type="submit" variant="primary" loading={busy}>
                  Create block
                </Button>
                <Link className={buttonClassName("ghost")} to="/scheduling/capacity-blocks">
                  Cancel
                </Link>
              </div>
            </form>
          )}
        </ResourceGate>
      </Card>
    </div>
  );
}
