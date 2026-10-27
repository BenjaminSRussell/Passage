import { useState, type FormEvent } from "react";
import { Alert, Button, Card, EmptyState, FormField, Input, PageHeader, Select, Skeleton, StatGroup, StatTile } from "../../shell/ui";
import { computeMileage, describeProblem, type MileageResult, type Problem } from "./api/client";
import { DECIMAL, splitProblem } from "./format";
import styles from "./pages.module.css";

type Errors = Record<string, string>;
type Unit = "mile" | "km";

export interface MileageValues {
  distance: string;
  rate: string;
  unit: Unit;
}

export const emptyMileage: MileageValues = { distance: "", rate: "", unit: "mile" };

export function validateMileage(v: MileageValues): Errors {
  const errors: Errors = {};
  if (!DECIMAL.test(v.distance.trim()) || Number(v.distance) > 100_000) errors.distance = "Enter the distance, up to 100000, such as 120.5.";
  if (!DECIMAL.test(v.rate.trim()) || Number(v.rate) > 100) errors.rate = "Enter the rate per unit from the mileage table, such as 0.70.";
  return errors;
}

const SHOWN = ["distance", "rate"];

/** BR-43.5 mileage: distance times the table rate, rounded to the cent. Stores nothing. */
export function MileagePage() {
  const [values, setValues] = useState<MileageValues>(emptyMileage);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Pick<Problem, "code" | "message"> | null>(null);
  const [result, setResult] = useState<(MileageResult & { distance: string; unit: Unit }) | null>(null);
  const [busy, setBusy] = useState(false);

  function update(patch: Partial<MileageValues>) {
    setValues((prev) => ({ ...prev, ...patch }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateMileage(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const distance = values.distance.trim();
      const res = await computeMileage({ distance, rate: values.rate.trim() });
      setResult({ ...res, distance, unit: values.unit });
    } catch (error) {
      const split = splitProblem(describeProblem(error), SHOWN);
      setResult(null);
      setErrors(split.fields);
      setFailure(split.failure);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Mileage"
        description="Work out a mileage claim from the distance and the rate in the mileage table for the vehicle and date (for example IRS 0.70 USD a mile or BMF 0.30 EUR a km in 2025). Nothing is saved."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="mileage-distance" label="Distance" error={errors.distance}>
            {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={values.distance} onChange={(e) => update({ distance: e.target.value })} invalid={Boolean(errors.distance)} />}
          </FormField>
          <FormField id="mileage-unit" label="Unit">
            {(c) => (
              <Select {...c} value={values.unit} onChange={(e) => update({ unit: e.target.value as Unit })}>
                <option value="mile">Miles</option>
                <option value="km">Kilometres</option>
              </Select>
            )}
          </FormField>
          <FormField id="mileage-rate" label="Rate per unit" error={errors.rate}>
            {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={values.rate} onChange={(e) => update({ rate: e.target.value })} invalid={Boolean(errors.rate)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not work out the mileage" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Compute mileage
            </Button>
          </div>
        </form>
      </Card>
      <Card title="Result">
        {result ? (
          <StatGroup label="Mileage claim">
            <StatTile label="Distance" value={`${result.distance} ${result.unit === "km" ? "km" : "miles"}`} />
            <StatTile label="Amount" value={result.amount} />
          </StatGroup>
        ) : busy ? (
          <div className={styles.stack} aria-busy="true">
            <Skeleton height="tile" width="1/2" />
          </div>
        ) : (
          <EmptyState icon="receipt" title="No mileage worked out yet" description="Enter the distance and the rate, then compute." compact />
        )}
      </Card>
    </div>
  );
}
