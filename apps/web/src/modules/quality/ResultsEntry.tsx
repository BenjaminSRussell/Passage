import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, Select } from "../../shell/ui";
import { describeProblem, newKey, recordResults, type Instrument, type OrderView, type Problem, type ResultInput, type TestLine } from "./api/client";
import { CALIBRATION_LABELS, SIGNED_DECIMAL, shortId } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const OTHER = "__other__";

interface Row {
  value: string;
  outcome: string;
  otherOutcome: string;
}

/** Outside the test's limits, for the immediate highlight (BR-33.6 decides on the server after rounding). */
export function outsideLimits(test: TestLine, value: string): boolean {
  if (!SIGNED_DECIMAL.test(value.trim())) return false;
  const v = Number(value);
  return (test.min !== undefined && v < Number(test.min)) || (test.max !== undefined && v > Number(test.max));
}

function limits(test: TestLine): string {
  if (test.min !== undefined && test.max !== undefined) return `Limits ${test.min} to ${test.max}`;
  if (test.min !== undefined) return `At least ${test.min}`;
  if (test.max !== undefined) return `At most ${test.max}`;
  return "No limits";
}

/**
 * Results recording (FR-33.4): one sample at a time, a row per test of the order's
 * test group. Empty rows are skipped. A retest number above 0 re-records a sample;
 * the latest retest counts.
 */
export function ResultsEntry({ order, instruments, onRecorded }: { order: OrderView; instruments: Instrument[]; onRecorded(): void }) {
  const tests = order.testGroup.tests;
  const samples = Math.max(1, Math.ceil(Number(order.sampleQty)));
  const [sampleNo, setSampleNo] = useState("1");
  const [retestNo, setRetestNo] = useState("0");
  const [instrumentId, setInstrumentId] = useState("");
  const [rows, setRows] = useState<Record<string, Row>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const pending = order.status === "PENDING_DECISION";

  function row(testId: string): Row {
    return rows[testId] ?? { value: "", outcome: "", otherOutcome: "" };
  }

  function update(testId: string, patch: Partial<Row>) {
    setRows({ ...rows, [testId]: { ...row(testId), ...patch } });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setNotice(null);
    const found: Record<string, string> = {};
    const sample = Number(sampleNo);
    const retest = Number(retestNo);
    if (!/^\d+$/.test(sampleNo) || sample < 1 || sample > samples) found.sampleNo = `Enter a sample number from 1 to ${samples}.`;
    if (!/^\d+$/.test(retestNo)) found.retestNo = "Enter 0 for a first result, or the retest number.";
    else if (pending && retest < 1) found.retestNo = "The order waits for its decision; only retests (1 or more) can be recorded.";
    const batch: ResultInput[] = [];
    for (const test of tests) {
      const r = row(test.testId);
      if (test.kind === "VARIABLE") {
        if (r.value.trim() === "") continue;
        if (!SIGNED_DECIMAL.test(r.value.trim())) {
          found[test.testId] = "Enter a number with up to 6 decimals.";
          continue;
        }
        batch.push({ testId: test.testId, sampleNo: sample, retestNo: retest, value: r.value.trim(), instrumentId: instrumentId || undefined });
      } else {
        const outcome = r.outcome === OTHER ? r.otherOutcome.trim() : r.outcome;
        if (outcome === "") continue;
        if (!/^[0-9a-f-]{36}$/i.test(outcome)) {
          found[test.testId] = "Enter the outcome id (a UUID).";
          continue;
        }
        batch.push({ testId: test.testId, sampleNo: sample, retestNo: retest, outcomeId: outcome, instrumentId: instrumentId || undefined });
      }
    }
    if (Object.keys(found).length === 0 && batch.length === 0) found.batch = "Enter at least one result.";
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await recordResults(order.id, batch, key.current);
      key.current = newKey();
      setRows({});
      setNotice(`Recorded ${batch.length} ${batch.length === 1 ? "result" : "results"} for sample ${sample}.`);
      onRecorded();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Record results">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Record results">
        <FormField id="results-sample" label="Sample" hint={`1 to ${samples}`} error={errors.sampleNo}>
          {(control) => <Input {...control} inputMode="numeric" value={sampleNo} invalid={Boolean(errors.sampleNo)} onChange={(event) => setSampleNo(event.target.value)} />}
        </FormField>
        <FormField id="results-retest" label="Retest number" hint="0 for the first result" error={errors.retestNo}>
          {(control) => <Input {...control} inputMode="numeric" value={retestNo} invalid={Boolean(errors.retestNo)} onChange={(event) => setRetestNo(event.target.value)} />}
        </FormField>
        <FormField id="results-instrument" label="Instrument" optional hint="Results from an overdue instrument are refused.">
          {(control) => (
            <Select {...control} value={instrumentId} onChange={(event) => setInstrumentId(event.target.value)}>
              <option value="">No instrument</option>
              {instruments.map((i) => (
                <option key={i.id} value={i.id}>
                  {`${i.code}, ${i.name}${i.calibrationStatus ? ` (${CALIBRATION_LABELS[i.calibrationStatus].label.toLowerCase()})` : ""}`}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        {tests.map((test) => {
          const r = row(test.testId);
          const label = `${test.name || shortId(test.testId)}${test.required ? "" : " (optional)"}`;
          if (test.kind === "VARIABLE") {
            const out = outsideLimits(test, r.value);
            return (
              <FormField
                key={test.testId}
                id={`results-${test.testId}`}
                label={label}
                hint={out ? `Outside the limits: ${limits(test).toLowerCase()}.` : `${limits(test)}, ${test.decimals} decimals.`}
                error={errors[test.testId]}
              >
                {(control) => (
                  <Input
                    {...control}
                    inputMode="decimal"
                    value={r.value}
                    invalid={Boolean(errors[test.testId]) || out}
                    onChange={(event) => update(test.testId, { value: event.target.value })}
                  />
                )}
              </FormField>
            );
          }
          return (
            <div key={test.testId} className={styles.entry}>
              <FormField id={`results-${test.testId}`} label={label} error={errors[test.testId]}>
                {(control) => (
                  <Select {...control} value={r.outcome} invalid={Boolean(errors[test.testId])} onChange={(event) => update(test.testId, { outcome: event.target.value })}>
                    <option value="">No result</option>
                    {(test.passingOutcomeIds ?? []).map((o) => (
                      <option key={o} value={o}>{`Passing outcome ${shortId(o)}`}</option>
                    ))}
                    <option value={OTHER}>Another outcome (fails)</option>
                  </Select>
                )}
              </FormField>
              {r.outcome === OTHER ? (
                <FormField id={`results-${test.testId}-other`} label="Outcome id">
                  {(control) => <Input {...control} value={r.otherOutcome} onChange={(event) => update(test.testId, { otherOutcome: event.target.value })} />}
                </FormField>
              ) : null}
            </div>
          );
        })}
        {errors.batch || failure || notice ? (
          <div className={styles.formActions}>
            {errors.batch ? (
              <Alert tone="warning" live="alert">
                {errors.batch}
              </Alert>
            ) : null}
            {failure ? (
              <Alert tone="danger" title="Could not record the results" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            ) : null}
            {notice ? (
              <Alert tone="success" live="status">
                {notice}
              </Alert>
            ) : null}
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Record results
          </Button>
        </div>
      </form>
    </Card>
  );
}
