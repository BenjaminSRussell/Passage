import { useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Textarea } from "../../shell/ui";
import { runPlan, type PlanVersion, type WorkCalendar } from "./api/client";
import { describeError, PLAN_CODE, usePlanCode, withPlan, type LoadError } from "./hooks";
import styles from "./pages.module.css";

const UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** A one-item snapshot (BR-22.2 FG-100) so the form can be tried as is. */
export const SAMPLE_ITEMS = JSON.stringify(
  [
    {
      itemId: "00000000-0000-0000-0000-000000000100",
      coverage: { code: "requirement" },
      onHand: "20",
      safetyStock: "10",
      orderType: "production",
      leadTimeDays: 7,
      demand: [{ ref: "SO-1", refType: "sales-line", date: "2026-11-30", qty: "120" }],
    },
  ],
  null,
  2,
);

function newKey(): string {
  const c = globalThis.crypto as Crypto | undefined;
  return c?.randomUUID ? c.randomUUID() : `run-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

interface Draft {
  plan: string;
  asOf: string;
  siteId: string;
  /** Optional forecast model whose reduced forecast the run nets. */
  forecastModel?: string;
  /** Closed weekdays of the site, 0 Sunday to 6 Saturday (BR-22.8). */
  closedWeekdays?: number[];
  /** Closed dates (holidays) separated by commas, spaces or new lines. */
  closedDates?: string;
  items: string;
}

type Errors = Partial<Record<keyof Draft, string>>;

/** Weekdays in the order a working week reads, with the service's numbers. */
export const WEEKDAYS: [number, string][] = [
  [1, "Monday"],
  [2, "Tuesday"],
  [3, "Wednesday"],
  [4, "Thursday"],
  [5, "Friday"],
  [6, "Saturday"],
  [0, "Sunday"],
];

const MAX_CLOSED_DATES = 1000;

/**
 * Checks the form; returns field errors, the parsed items array and the work
 * calendar (undefined when no day is closed, so the run counts calendar days).
 */
export function validateRun(d: Draft): { errors: Errors; items: unknown[]; calendar?: WorkCalendar } {
  const errors: Errors = {};
  if (!PLAN_CODE.test(d.plan.trim())) errors.plan = "Use 2 to 20 capital letters, digits, _ or -.";
  if (!DATE.test(d.asOf)) errors.asOf = "Enter the plan date.";
  if (!UUID.test(d.siteId.trim())) errors.siteId = "Enter the site id as a UUID.";
  const model = (d.forecastModel ?? "").trim();
  if (model !== "" && !PLAN_CODE.test(model)) errors.forecastModel = "Use 2 to 20 capital letters, digits, _ or -, or leave empty.";
  let items: unknown[] = [];
  try {
    const parsed: unknown = JSON.parse(d.items);
    if (Array.isArray(parsed) && parsed.length > 0) items = parsed;
    else errors.items = "Items must be a non-empty JSON array.";
  } catch {
    errors.items = "Items are not valid JSON.";
  }
  const closedWeekdays = [...new Set(d.closedWeekdays ?? [])].sort();
  if (closedWeekdays.length === 7) errors.closedWeekdays = "Keep at least one weekday open.";
  const closedDates = (d.closedDates ?? "").split(/[\s,]+/).filter((x) => x !== "");
  if (closedDates.some((x) => !DATE.test(x))) errors.closedDates = "Enter dates as YYYY-MM-DD, separated by commas or spaces.";
  else if (closedDates.length > MAX_CLOSED_DATES) errors.closedDates = `Enter at most ${MAX_CLOSED_DATES} dates.`;
  const calendar =
    closedWeekdays.length > 0 || closedDates.length > 0
      ? { ...(closedWeekdays.length > 0 ? { closedWeekdays } : {}), ...(closedDates.length > 0 ? { closedDates } : {}) }
      : undefined;
  return { errors, items, calendar };
}

/**
 * Run plan (FR-22.1): sends a snapshot to POST /plans/{code}:run. The
 * Idempotency-Key is kept while the inputs are unchanged, so resubmitting
 * after a network error replays the same run instead of starting another.
 */
export function RunPlanPage({ today = new Date().toISOString().slice(0, 10) }: { today?: string }) {
  const [plan] = usePlanCode();
  const [draft, setDraft] = useState<Draft>({
    plan,
    asOf: today,
    siteId: "",
    forecastModel: "",
    closedWeekdays: [],
    closedDates: "",
    items: SAMPLE_ITEMS,
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<LoadError | null>(null);
  const [result, setResult] = useState<PlanVersion | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  const set = (field: keyof Draft) => (value: string) => {
    key.current = newKey();
    setDraft((d) => ({ ...d, [field]: value }));
  };
  const toggleWeekday = (day: number, closed: boolean) => {
    key.current = newKey();
    setDraft((d) => {
      const rest = (d.closedWeekdays ?? []).filter((x) => x !== day);
      return { ...d, closedWeekdays: closed ? [...rest, day] : rest };
    });
  };

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    const { errors: found, items, calendar } = validateRun(draft);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    setFailure(null);
    setResult(null);
    const code = draft.plan.trim();
    try {
      const forecastModel = (draft.forecastModel ?? "").trim();
      setResult(
        await runPlan(code, key.current, {
          asOf: draft.asOf,
          siteId: draft.siteId.trim(),
          ...(forecastModel ? { forecastModel } : {}),
          ...(calendar ? { calendar } : {}),
          items,
        }),
      );
      key.current = newKey();
    } catch (err) {
      setFailure(describeError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Run plan"
        description="Net the snapshot's demand against its supply level by level, and make the result the plan's current version."
      />
      <Alert tone="danger" title={failure ? "The plan did not run" : undefined} live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      <Alert tone="success" title={result ? `Version ${result.number} of plan ${result.planCode} is current` : undefined} live="status">
        {result ? (
          <>
            {`${result.plannedOrders} planned orders and ${result.messages} messages from ${result.itemsPlanned} items. `}
            <Link to={withPlan("/planning/planned-orders", result.planCode)}>View planned orders</Link>
          </>
        ) : null}
      </Alert>
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Run plan">
          <FormField id="run-plan" label="Plan" error={errors.plan}>
            {(a11y) => <Input {...a11y} value={draft.plan} maxLength={20} onChange={(e) => set("plan")(e.target.value.toUpperCase())} />}
          </FormField>
          <FormField id="run-as-of" label="Plan date" hint="Day 0 of the plan; earlier demand is due today." error={errors.asOf}>
            {(a11y) => <Input {...a11y} type="date" value={draft.asOf} onChange={(e) => set("asOf")(e.target.value)} />}
          </FormField>
          <FormField id="run-site" label="Site" hint="Site id (UUID) the snapshot belongs to." error={errors.siteId}>
            {(a11y) => <Input {...a11y} value={draft.siteId} onChange={(e) => set("siteId")(e.target.value)} />}
          </FormField>
          <FormField
            id="run-forecast"
            label="Forecast model"
            optional
            hint="Adds the model's reduced forecast at the site as demand of the items below."
            error={errors.forecastModel}
          >
            {(a11y) => (
              <Input {...a11y} value={draft.forecastModel ?? ""} maxLength={20} onChange={(e) => set("forecastModel")(e.target.value.toUpperCase())} />
            )}
          </FormField>
          <fieldset className={`${styles.fieldset} ${styles.wide}`} aria-describedby="run-weekdays-hint">
            <legend className={styles.legend}>Closed weekdays</legend>
            <p id="run-weekdays-hint" className={styles.summary}>
              With closed days, lead times and margins count working days; demand on a closed day moves to the next open day and
              supply to the previous one. Leave all open to count calendar days.
            </p>
            <div className={styles.checks}>
              {WEEKDAYS.map(([day, name]) => (
                <label key={day} className={styles.check} htmlFor={`run-closed-${day}`}>
                  <Input
                    id={`run-closed-${day}`}
                    type="checkbox"
                    checked={(draft.closedWeekdays ?? []).includes(day)}
                    onChange={(e) => toggleWeekday(day, e.target.checked)}
                  />
                  {name}
                </label>
              ))}
            </div>
            {errors.closedWeekdays ? (
              <p className={styles.formError} role="alert">
                {errors.closedWeekdays}
              </p>
            ) : null}
          </fieldset>
          <div className={styles.wide}>
            <FormField
              id="run-closed-dates"
              label="Closed dates"
              optional
              hint="Holidays and shutdown days of the site, as YYYY-MM-DD separated by commas or spaces."
              error={errors.closedDates}
            >
              {(a11y) => <Input {...a11y} value={draft.closedDates ?? ""} onChange={(e) => set("closedDates")(e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.wide}>
            <FormField
              id="run-items"
              label="Items"
              hint="JSON array of items with coverage, on-hand, lot sizes, demand, receipts and BOM components (RunItem in the planning OpenAPI)."
              error={errors.items}
            >
              {(a11y) => (
                <Textarea {...a11y} className={styles.mono} rows={14} value={draft.items} onChange={(e) => set("items")(e.target.value)} />
              )}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run plan
            </Button>
            <Link className={buttonClassName("ghost")} to={withPlan("/planning/runs", draft.plan.trim() || plan)}>
              Run history
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
