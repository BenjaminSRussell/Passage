import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, StatGroup, StatTile } from "../../shell/ui";
import { checkRuleCount, describeProblem, type CountCheck, type Problem } from "./api/client";
import { QUANTITY } from "./format";
import styles from "./pages.module.css";

/** The service's safety percentage must stay below this. */
export const MAX_SAFETY_PCT = 1000;

type CountErrors = Partial<Record<"dailyDemand" | "safetyPct", string>>;

/** Checks that mirror the service before a count check is sent. */
export function validateCountCheck(values: { dailyDemand: string; safetyPct: string }): CountErrors {
  const errors: CountErrors = {};
  if (!QUANTITY.test(values.dailyDemand)) errors.dailyDemand = "Enter the daily demand: 0 or more, with at most 6 decimals.";
  if (values.safetyPct !== "" && (!QUANTITY.test(values.safetyPct) || Number(values.safetyPct) >= MAX_SAFETY_PCT)) {
    errors.safetyPct = `Enter a safety percentage from 0 to below ${MAX_SAFETY_PCT}, or leave it empty for 0.`;
  }
  return errors;
}

/**
 * Fixed-kanban count check (PRD 4.24.16, BR-24.7) on a fixed rule's page: the
 * suggested number of kanbans for a daily demand against the rule's own, with a
 * deviation above the limit flagged. Advisory: the rule is never changed.
 */
export function RuleCountCheckCard({ ruleId }: { ruleId: string }) {
  const [values, setValues] = useState({ dailyDemand: "", safetyPct: "" });
  const [errors, setErrors] = useState<CountErrors>({});
  const [result, setResult] = useState<CountCheck | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const latest = useRef(0);

  async function check(event: FormEvent) {
    event.preventDefault();
    const found = validateCountCheck(values);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    const request = ++latest.current;
    setBusy(true);
    setFailure(null);
    try {
      const answer = await checkRuleCount(ruleId, { dailyDemand: values.dailyDemand, safetyPct: values.safetyPct || undefined });
      if (request === latest.current) setResult(answer);
    } catch (error) {
      if (request === latest.current) {
        setResult(null);
        setFailure(describeProblem(error));
      }
    } finally {
      if (request === latest.current) setBusy(false);
    }
  }

  return (
    <Card title="Kanban count check">
      <div className={styles.stack}>
        <form className={styles.toolbar} onSubmit={check} noValidate>
          <FormField id="count-demand" label="Daily demand" error={errors.dailyDemand}>
            {(c) => (
              <Input
                {...c}
                inputMode="decimal"
                value={values.dailyDemand}
                onChange={(e) => setValues({ ...values, dailyDemand: e.target.value.trim() })}
                invalid={Boolean(errors.dailyDemand)}
              />
            )}
          </FormField>
          <FormField id="count-safety" label="Safety (%)" optional error={errors.safetyPct}>
            {(c) => (
              <Input
                {...c}
                inputMode="decimal"
                value={values.safetyPct}
                onChange={(e) => setValues({ ...values, safetyPct: e.target.value.trim() })}
                invalid={Boolean(errors.safetyPct)}
              />
            )}
          </FormField>
          <Button type="submit" variant="secondary" loading={busy}>
            Check count
          </Button>
        </form>
        {failure ? (
          <Alert tone="danger" title="Could not check the count" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : result ? (
          <>
            <StatGroup label="Kanban count check">
              <StatTile label="Configured cards" value={result.configured} />
              <StatTile label="Suggested cards" value={result.suggested} />
              <StatTile label="Deviation" value={`${result.deviationPct} %`} hint={`Reported above ${result.limitPct} %`} />
            </StatGroup>
            <Alert
              tone={result.reportable ? "warning" : "success"}
              live="status"
              title={
                result.reportable
                  ? `The rule's ${result.configured} cards are ${result.deviationPct} % away from the ${result.suggested} suggested. Review the number of kanbans.`
                  : `The rule's ${result.configured} cards are within ${result.limitPct} % of the ${result.suggested} suggested.`
              }
            />
            <p className={styles.muted}>
              {`⌈${result.dailyDemand} a day × ${result.leadTimeH} h lead time ÷ 24 × (1 + ${result.safetyPct} %) ÷ ${result.fixedQty} per card⌉. A suggestion only: the rule is not changed.`}
            </p>
          </>
        ) : (
          <p className={styles.muted}>Enter the expected daily demand to compare the rule's number of kanbans with the suggestion.</p>
        )}
      </div>
    </Card>
  );
}
