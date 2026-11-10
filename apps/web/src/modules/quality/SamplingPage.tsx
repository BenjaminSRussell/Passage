import { useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { describeProblem, sizeSample, type Problem, type SampleDraw } from "./api/client";
import { DECIMAL, qty } from "./format";
import styles from "./pages.module.css";
import { emptyPlan, planFieldErrors, PlanFields, validatePlan, type PlanErrors, type PlanValues } from "./PlanFields";
import { ProblemMessage } from "../../shell/errors";

/** Sampling calculator (FR-33.2, BR-33.1): sizes a sample for a lot without writing anything. */
export function SamplingPage() {
  const [lotQty, setLotQty] = useState("");
  const [plan, setPlan] = useState<PlanValues>(emptyPlan);
  const [lotError, setLotError] = useState<string | undefined>();
  const [planErrors, setPlanErrors] = useState<PlanErrors>({});
  const [draw, setDraw] = useState<SampleDraw | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setDraw(null);
    const lot = lotQty.trim();
    const checked = validatePlan(plan);
    const badLot = !DECIMAL.test(lot) || Number(lot) <= 0 ? "Enter the lot quantity, above 0." : undefined;
    setLotError(badLot);
    setPlanErrors(checked.errors);
    if (badLot || !checked.plan) return;
    setBusy(true);
    try {
      setDraw(await sizeSample(lot, checked.plan));
    } catch (error) {
      const problem = describeProblem(error);
      const fromPlan = planFieldErrors(problem.fields, "");
      if (problem.fields.lotQty) setLotError(problem.fields.lotQty);
      if (Object.keys(fromPlan).length > 0) setPlanErrors(fromPlan);
      if (!problem.fields.lotQty && Object.keys(fromPlan).length === 0) setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Sampling calculator"
        description="Size a sample the way a quality order would: ANSI/ASQ Z1.4 single sampling at general inspection level II, a fixed quantity, or a percentage. Nothing is saved."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Sampling calculator">
          <FormField id="sampling-lot" label="Lot quantity" error={lotError}>
            {(control) => <Input {...control} inputMode="decimal" value={lotQty} invalid={Boolean(lotError)} onChange={(event) => setLotQty(event.target.value)} />}
          </FormField>
          <PlanFields idPrefix="sampling" values={plan} errors={planErrors} onChange={(patch) => setPlan({ ...plan, ...patch })} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not size the sample" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Size sample
            </Button>
          </div>
        </form>
      </Card>
      {draw ? (
        <Card title="Sample draw">
          <dl className={styles.details} aria-label="Sample draw">
            <div className={styles.entry}>
              <dt className={styles.term}>Code letter</dt>
              <dd className={`${styles.value} ${styles.mono}`}>{draw.codeLetter || "—"}</dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>Sample quantity</dt>
              <dd className={`${styles.value} ${styles.mono}`}>{qty(draw.sampleQty)}</dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>Accept / reject</dt>
              <dd className={`${styles.value} ${styles.mono}`}>{`${draw.acceptNo} / ${draw.rejectNo}`}</dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>Severity</dt>
              <dd className={styles.value}>{draw.severity.toLowerCase()}</dd>
            </div>
          </dl>
        </Card>
      ) : null}
    </div>
  );
}
