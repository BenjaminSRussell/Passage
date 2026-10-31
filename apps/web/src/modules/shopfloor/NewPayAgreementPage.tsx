import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createPayAgreement, describeProblem, newKey, type NewPayAgreement, type Problem, type ShiftPremium } from "./api/client";
import { WEEKDAYS } from "./format";
import styles from "./pages.module.css";

interface Values {
  code: string;
  name: string;
  dailyThreshold: string;
  weeklyThreshold: string;
  overtimeMultiplier: string;
  sundayMultiplier: string;
  weekStart: string;
  /** Premium windows; absent means none. */
  premiums?: ShiftPremium[];
}

const MAX_PREMIUMS = 6;
const CLOCK = /^([01]\d|2[0-3]):[0-5]\d$/;

type Field = Exclude<keyof Values, "premiums">;
type Errors = Partial<Record<Field, string>>;
/** Problems of premium rows, keyed "<row>.<name|from|to|premium>". */
export type PremiumErrors = Record<string, string>;

/** The windows overlap when one starts inside the other, on the 24 hour circle. */
function clockMinutes(value: string): number {
  return Number(value.slice(0, 2)) * 60 + Number(value.slice(3));
}

function premiumLength(w: ShiftPremium): number {
  const length = clockMinutes(w.to) - clockMinutes(w.from);
  return length <= 0 ? length + 1440 : length;
}

/** Checks that mirror the service for the premium windows. */
export function validatePremiums(rows: ShiftPremium[]): { errors: PremiumErrors; input: ShiftPremium[] } {
  const errors: PremiumErrors = {};
  const input = rows.map((w) => ({ name: w.name.trim(), from: w.from.trim(), to: w.to.trim(), premium: w.premium.trim() }));
  input.forEach((w, i) => {
    if ([...w.name].length < 1 || [...w.name].length > 60) errors[`${i}.name`] = "Enter a name of up to 60 characters.";
    if (!CLOCK.test(w.from)) errors[`${i}.from`] = "Enter a time as HH:MM.";
    if (!CLOCK.test(w.to)) errors[`${i}.to`] = "Enter a time as HH:MM.";
    if (CLOCK.test(w.from) && CLOCK.test(w.to) && w.from === w.to) errors[`${i}.to`] = "The end must differ from the start.";
    if (!decimalWithin(w.premium, 0, 2, 4) || Number(w.premium) === 0) errors[`${i}.premium`] = "Enter a premium above 0 up to 2, such as 0.15.";
  });
  if (Object.keys(errors).length === 0) {
    for (let i = 0; i < input.length; i++) {
      for (let j = i + 1; j < input.length; j++) {
        const a = input[i];
        const b = input[j];
        const inside = (x: string, w: ShiftPremium) => (clockMinutes(x) - clockMinutes(w.from) + 1440) % 1440 < premiumLength(w);
        if (inside(a.from, b) || inside(b.from, a)) errors[`${j}.from`] = "Premium windows must not overlap.";
      }
    }
  }
  return { errors, input };
}

const FIELDS: Field[] = ["code", "name", "dailyThreshold", "weeklyThreshold", "overtimeMultiplier", "sundayMultiplier", "weekStart"];

/** A decimal of at most `scale` places within [lo, hi]. */
function decimalWithin(raw: string, lo: number, hi: number, scale: number): boolean {
  const value = raw.trim();
  if (!/^\d{1,3}(\.\d+)?$/.test(value)) return false;
  const places = value.split(".")[1]?.length ?? 0;
  const n = Number(value);
  return places <= scale && n >= lo && n <= hi;
}

/** Checks that mirror the service, and the request the form sends. */
export function validateAgreement(values: Values): { errors: Errors; premiumErrors: PremiumErrors; input?: NewPayAgreement } {
  const errors: Errors = {};
  const code = values.code.trim();
  const name = values.name.trim();
  if (code.length < 1 || code.length > 40) errors.code = "Enter a code of up to 40 characters.";
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!decimalWithin(values.dailyThreshold, 0, 24, 2)) errors.dailyThreshold = "Enter hours from 0 to 24, such as 8.";
  if (!decimalWithin(values.weeklyThreshold, 0, 168, 2)) errors.weeklyThreshold = "Enter hours from 0 to 168, such as 40.";
  if (!decimalWithin(values.overtimeMultiplier, 1, 5, 4)) errors.overtimeMultiplier = "Enter a multiplier from 1 to 5, such as 1.5.";
  if (!decimalWithin(values.sundayMultiplier, 1, 5, 4)) errors.sundayMultiplier = "Enter a multiplier from 1 to 5, such as 2.";
  const weekStart = Number(values.weekStart);
  if (!Number.isInteger(weekStart) || weekStart < 0 || weekStart > 6) errors.weekStart = "Choose a day.";
  const premiums = validatePremiums(values.premiums ?? []);
  if (Object.keys(errors).length > 0 || Object.keys(premiums.errors).length > 0) return { errors, premiumErrors: premiums.errors };
  return {
    premiumErrors: {},
    errors,
    input: {
      code,
      name,
      dailyThreshold: values.dailyThreshold.trim(),
      weeklyThreshold: values.weeklyThreshold.trim(),
      overtimeMultiplier: values.overtimeMultiplier.trim(),
      sundayMultiplier: values.sundayMultiplier.trim(),
      weekStart,
      shiftPremiums: premiums.input,
    },
  };
}

/** Create a pay agreement (FR-25.7). */
export function NewPayAgreementPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Values>({
    code: "",
    name: "",
    dailyThreshold: "8",
    weeklyThreshold: "40",
    overtimeMultiplier: "1.5",
    sundayMultiplier: "2",
    weekStart: "1",
    premiums: [],
  });
  const [errors, setErrors] = useState<Errors>({});
  const [premiumErrors, setPremiumErrors] = useState<PremiumErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, premiumErrors: foundPremiums, input } = validateAgreement(values);
    setErrors(found);
    setPremiumErrors(foundPremiums);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createPayAgreement(input, key.current);
      navigate("/shopfloor/pay-agreements");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of FIELDS) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.code === "SF-0142") fieldErrors.code = "This code is already used.";
      const rowErrors: PremiumErrors = {};
      for (const [name, message] of Object.entries(problem.fields)) {
        const match = /^shiftPremiums\[(\d+)\]\.(\w+)$/.exec(name);
        if (match) rowErrors[`${match[1]}.${match[2]}`] = message;
      }
      if (Object.keys(rowErrors).length > 0) setPremiumErrors(rowErrors);
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else if (Object.keys(rowErrors).length > 0) setFailure(null);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const premiums = values.premiums ?? [];
  function setPremium(index: number, patch: Partial<ShiftPremium>) {
    update({ premiums: premiums.map((w, i) => (i === index ? { ...w, ...patch } : w)) });
  }

  const field = (id: Exclude<Field, "weekStart">, label: string, hint?: string, extra?: Record<string, unknown>) => (
    <FormField id={`agreement-${id}`} label={label} hint={hint} error={errors[id]}>
      {(control) => (
        <Input {...control} {...extra} value={values[id]} onChange={(event) => update({ [id]: event.target.value } as Partial<Values>)} invalid={Boolean(errors[id])} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New pay agreement" description="Overtime applies to base hours over the thresholds; hours on Sunday are paid at the Sunday multiplier and do not count toward the weekly threshold." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {field("code", "Code", "Unique, for example US-HOURLY.", { maxLength: 40 })}
          {field("name", "Name", undefined, { maxLength: 120 })}
          {field("dailyThreshold", "Daily overtime after (hours)", "0 to 24.", { inputMode: "decimal" })}
          {field("weeklyThreshold", "Weekly overtime after (hours)", "0 to 168; base hours only.", { inputMode: "decimal" })}
          {field("overtimeMultiplier", "Overtime multiplier", "1 to 5.", { inputMode: "decimal" })}
          {field("sundayMultiplier", "Sunday multiplier", "1 to 5.", { inputMode: "decimal" })}
          <FormField id="agreement-weekStart" label="Pay week starts on" error={errors.weekStart}>
            {(control) => (
              <Select {...control} value={values.weekStart} onChange={(event) => update({ weekStart: event.target.value })} invalid={Boolean(errors.weekStart)}>
                {WEEKDAYS.map((day, index) => (
                  <option key={day} value={String(index)}>
                    {day}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <fieldset className={styles.fieldset}>
            <legend>Shift premiums</legend>
            <p className={styles.hint}>Hours inside a window earn the premium on top of the pay type (0.15 pays 15% extra). A window that ends before it starts crosses midnight.</p>
            {premiums.length === 0 ? <p className={styles.hint}>No premium windows.</p> : null}
            {premiums.map((w, i) => (
              <div key={i} className={styles.premiumRow} role="group" aria-label={`Premium window ${i + 1}`}>
                <FormField id={`premium-${i}-name`} label="Window name" error={premiumErrors[`${i}.name`]}>
                  {(control) => <Input {...control} maxLength={60} value={w.name} onChange={(event) => setPremium(i, { name: event.target.value })} invalid={Boolean(premiumErrors[`${i}.name`])} />}
                </FormField>
                <FormField id={`premium-${i}-from`} label="From (HH:MM)" error={premiumErrors[`${i}.from`]}>
                  {(control) => <Input {...control} placeholder="22:00" value={w.from} onChange={(event) => setPremium(i, { from: event.target.value })} invalid={Boolean(premiumErrors[`${i}.from`])} />}
                </FormField>
                <FormField id={`premium-${i}-to`} label="To (HH:MM)" error={premiumErrors[`${i}.to`]}>
                  {(control) => <Input {...control} placeholder="06:00" value={w.to} onChange={(event) => setPremium(i, { to: event.target.value })} invalid={Boolean(premiumErrors[`${i}.to`])} />}
                </FormField>
                <FormField id={`premium-${i}-premium`} label="Premium" error={premiumErrors[`${i}.premium`]}>
                  {(control) => <Input {...control} inputMode="decimal" placeholder="0.15" value={w.premium} onChange={(event) => setPremium(i, { premium: event.target.value })} invalid={Boolean(premiumErrors[`${i}.premium`])} />}
                </FormField>
                <Button type="button" variant="ghost" onClick={() => update({ premiums: premiums.filter((_, k) => k !== i) })}>
                  Remove window {i + 1}
                </Button>
              </div>
            ))}
            <Button type="button" onClick={() => update({ premiums: [...premiums, { name: "", from: "", to: "", premium: "" }] })} disabled={premiums.length >= MAX_PREMIUMS}>
              Add premium window
            </Button>
          </fieldset>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the pay agreement" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create pay agreement
            </Button>
            <Link className={buttonClassName("ghost")} to="/shopfloor/pay-agreements">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
