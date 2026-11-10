import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, Select } from "../../shell/ui";
import {
  describeProblem,
  FREE_GOODS_CALC_RULES,
  newKey,
  PricingApiError,
  reviseFreeGoodsRule,
  type FreeGoodsCalcRule,
  type FreeGoodsRule,
  type FreeGoodsRuleRevision,
  type Problem,
} from "./api/client";
import { CALC_LABELS } from "./freeGoodsFormat";
import { addDays, DAY, DECIMAL, today } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Errors = Partial<Record<"everyQuantity" | "freeQuantity" | "minQuantity" | "validFrom" | "validTo" | "form", string>>;

/** What a refused revision tells the planner, by service code. */
function failureTitle(code: string): string {
  if (code === "conflict_version") return "The rule changed since you opened it";
  if (code === "PRC-0002") return "Another rule covers those dates";
  if (code === "PRC-0031") return "The rule cannot be revised";
  if (code === "PRC-0047") return "The rule no longer exists";
  return "The change failed";
}

/**
 * Client checks that mirror the service; the service has the last word. `now` is today
 * (YYYY-MM-DD): a revision never changes what a day before today proposed, so a started rule
 * keeps its offer and start and its end cannot drop days it already applied to, and a rule that
 * has not started cannot start in the past.
 */
export function validateRevision(
  rule: Pick<FreeGoodsRule, "kind" | "calcRule" | "everyQuantity" | "freeQuantity" | "minQuantity" | "validFrom" | "validTo">,
  v: FreeGoodsRuleRevision,
  now: string,
): Errors {
  const errors: Errors = {};
  const started = rule.validFrom < now;
  const ended = rule.validTo < now;
  const yesterday = addDays(now, -1);
  const every = Number(v.everyQuantity);
  const free = Number(v.freeQuantity);
  if (!DECIMAL.test(v.everyQuantity.trim()) || every <= 0) errors.everyQuantity = "Enter a quantity above 0.";
  if (!DECIMAL.test(v.freeQuantity.trim()) || free <= 0) errors.freeQuantity = "Enter a quantity above 0.";
  else if (rule.kind === "INCLUSIVE" && !errors.everyQuantity && free >= every) errors.freeQuantity = "An inclusive rule must give fewer free units than the quantity ordered.";
  if (v.minQuantity.trim() !== "" && !DECIMAL.test(v.minQuantity.trim())) errors.minQuantity = "Enter the minimum as a number, or 0.";
  if (!DAY.test(v.validFrom)) errors.validFrom = "Enter the first day as YYYY-MM-DD.";
  if (!DAY.test(v.validTo)) errors.validTo = "Enter the last day as YYYY-MM-DD.";
  else if (DAY.test(v.validFrom) && v.validTo < v.validFrom) errors.validTo = "The last day cannot be before the first.";
  else if (started && !ended && v.validTo < yesterday) errors.validTo = `End it on ${yesterday} or later: the days already priced keep the rule.`;
  else if (ended && v.validTo !== rule.validTo) errors.validTo = `The rule ended on ${rule.validTo}; create a new rule instead.`;
  if (!started && DAY.test(v.validFrom) && v.validFrom !== rule.validFrom && v.validFrom < now) errors.validFrom = "The first day cannot be in the past.";
  if (started) {
    const fixed = `Fixed: the rule has applied since ${rule.validFrom}. End it and create a new rule to change the offer.`;
    if (v.calcRule !== rule.calcRule) errors.form = fixed;
    if (!errors.everyQuantity && Number(v.everyQuantity) !== Number(rule.everyQuantity)) errors.everyQuantity = fixed;
    if (!errors.freeQuantity && Number(v.freeQuantity) !== Number(rule.freeQuantity)) errors.freeQuantity = fixed;
    if (!errors.minQuantity && Number(v.minQuantity || "0") !== Number(rule.minQuantity)) errors.minQuantity = fixed;
    if (!errors.validFrom && v.validFrom !== rule.validFrom) errors.validFrom = fixed;
  }
  return errors;
}

/**
 * Change a live free goods rule's offer, calculation and validity (FR-26.22). The customers, product,
 * unit and variant identify the rule and stay as they are: withdraw it and create another to change those.
 * The next price resolution uses the new values; documents already priced keep theirs. An edit never
 * changes what a day before today proposed (the service refuses it): once the rule has started its offer
 * and first day are read-only and its last day cannot drop days already priced; an ended rule keeps its
 * last day.
 */
export function FreeGoodsEditCard({ rule, onChange }: { rule: FreeGoodsRule; onChange: (next: FreeGoodsRule) => void }) {
  const [calc, setCalc] = useState<FreeGoodsCalcRule>(rule.calcRule);
  const [every, setEvery] = useState(rule.everyQuantity);
  const [free, setFree] = useState(rule.freeQuantity);
  const [min, setMin] = useState(rule.minQuantity);
  const [validFrom, setValidFrom] = useState(rule.validFrom);
  const [validTo, setValidTo] = useState(rule.validTo);
  const [errors, setErrors] = useState<Errors>({});
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState("");
  const key = useRef(newKey());
  const now = today();
  const started = rule.validFrom < now;
  const ended = rule.validTo < now;
  const fixedHint = started ? `Fixed: the rule has applied since ${rule.validFrom}.` : undefined;

  function change(set: (value: string) => void) {
    return (value: string) => {
      set(value);
      key.current = newKey();
    };
  }

  function revision(): FreeGoodsRuleRevision {
    return {
      calcRule: calc,
      everyQuantity: every.trim(),
      freeQuantity: free.trim(),
      minQuantity: min.trim() === "" ? "0" : min.trim(),
      validFrom,
      validTo,
    };
  }

  function unchanged(r: FreeGoodsRuleRevision): boolean {
    return (
      r.calcRule === rule.calcRule &&
      Number(r.everyQuantity) === Number(rule.everyQuantity) &&
      Number(r.freeQuantity) === Number(rule.freeQuantity) &&
      Number(r.minQuantity) === Number(rule.minQuantity) &&
      r.validFrom === rule.validFrom &&
      r.validTo === rule.validTo
    );
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setDone("");
    const next = revision();
    const found = validateRevision(rule, next, now);
    if (Object.keys(found).length === 0 && unchanged(next)) found.form = "Change at least one term.";
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const changed = await reviseFreeGoodsRule(rule.id, rule.version, next, key.current);
      onChange(changed);
      setCalc(changed.calcRule);
      setEvery(changed.everyQuantity);
      setFree(changed.freeQuantity);
      setMin(changed.minQuantity);
      setValidFrom(changed.validFrom);
      setValidTo(changed.validTo);
      setDone("The rule is updated. Price resolution uses the new terms from the next request.");
      key.current = newKey();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      const fieldErrors: Errors = {};
      for (const name of ["everyQuantity", "freeQuantity", "minQuantity", "validFrom", "validTo"] as const) {
        if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      }
      if (problem.fields.validity) fieldErrors.validTo = problem.fields.validity;
      if (problem.fields.calcRule) fieldErrors.form = problem.fields.calcRule;
      setErrors(fieldErrors);
      // A refused command is final; only a network or server failure may be retried with the same key.
      if (error instanceof PricingApiError && error.status < 500) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Edit rule">
      <div className={styles.stack}>
        <Alert tone="success" live="status">
          {done}
        </Alert>
        {failure ? (
          <Alert tone="danger" title={failureTitle(failure.code)} live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="fg-edit-calc" label="Calculation" hint={fixedHint}>
            {(c) => (
              <Select {...c} value={calc} disabled={started} onChange={(e) => change((v) => setCalc(v as FreeGoodsCalcRule))(e.target.value)}>
                {FREE_GOODS_CALC_RULES.map((k) => (
                  <option key={k} value={k}>
                    {CALC_LABELS[k]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="fg-edit-every" label="For every quantity ordered" hint={fixedHint} error={errors.everyQuantity}>
            {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={every} readOnly={started} onChange={(e) => change(setEvery)(e.target.value)} invalid={Boolean(errors.everyQuantity)} />}
          </FormField>
          <FormField id="fg-edit-free" label="Free quantity" hint={fixedHint} error={errors.freeQuantity}>
            {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={free} readOnly={started} onChange={(e) => change(setFree)(e.target.value)} invalid={Boolean(errors.freeQuantity)} />}
          </FormField>
          <FormField id="fg-edit-min" label="Minimum quantity" optional hint={fixedHint ?? "No free goods below this ordered quantity."} error={errors.minQuantity}>
            {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={min} readOnly={started} onChange={(e) => change(setMin)(e.target.value)} invalid={Boolean(errors.minQuantity)} />}
          </FormField>
          <FormField id="fg-edit-from" label="Valid from" hint={fixedHint} error={errors.validFrom}>
            {(c) => <Input {...c} type="date" className={styles.mono} value={validFrom} readOnly={started} onChange={(e) => change(setValidFrom)(e.target.value)} invalid={Boolean(errors.validFrom)} />}
          </FormField>
          <FormField id="fg-edit-to" label="Valid to" hint={ended ? `Fixed: the rule ended on ${rule.validTo}.` : started ? "Inclusive. End it yesterday to stop it now, then create a new rule from today to change the offer." : "Inclusive."} error={errors.validTo}>
            {(c) => <Input {...c} type="date" className={styles.mono} value={validTo} readOnly={ended} onChange={(e) => change(setValidTo)(e.target.value)} invalid={Boolean(errors.validTo)} />}
          </FormField>
          {errors.form ? (
            <Alert tone="warning" live="alert">
              {errors.form}
            </Alert>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save changes
            </Button>
          </div>
        </form>
      </div>
    </Card>
  );
}
