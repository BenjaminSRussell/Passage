import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, Select } from "../../shell/ui";
import { describeProblem, newKey, postDecision, type DecisionKind, type DecisionLine, type OrderView, type Problem } from "./api/client";
import { DECIMAL, DECISION_LABELS, qty } from "./format";
import styles from "./pages.module.css";

interface LineValues {
  decision: DecisionKind;
  quantity: string;
  reason: string;
}

/** Sum of decimal strings with up to 6 places, exact (no float drift). */
export function sumQuantities(values: string[]): string {
  let micro = 0n;
  for (const v of values) {
    if (!DECIMAL.test(v.trim())) continue;
    const [whole, frac = ""] = v.trim().split(".");
    micro += BigInt(whole) * 1_000_000n + BigInt(frac.padEnd(6, "0"));
  }
  const whole = micro / 1_000_000n;
  const frac = (micro % 1_000_000n).toString().padStart(6, "0").replace(/0+$/, "");
  return frac ? `${whole}.${frac}` : `${whole}`;
}

/** Checks that mirror the service's SplitDecision (FR-33.6, QM-0104). */
export function validateDecision(lines: LineValues[], lotQty: string): { errors: Record<string, string>; input?: DecisionLine[] } {
  const errors: Record<string, string> = {};
  lines.forEach((l, i) => {
    const q = l.quantity.trim();
    if (!DECIMAL.test(q) || /^0+(\.0+)?$/.test(q)) errors[`${i}.quantity`] = "Enter a quantity above 0.";
    if (l.decision === "ACCEPT_WITH_DEVIATION" && l.reason.trim() === "") errors[`${i}.reason`] = "A deviation needs a reason.";
    if (l.reason.length > 500) errors[`${i}.reason`] = "Keep the reason to 500 characters.";
  });
  if (Object.keys(errors).length > 0) return { errors };
  const total = sumQuantities(lines.map((l) => l.quantity));
  if (total !== qty(lotQty)) {
    return { errors: { total: `The lines add up to ${total}; they must equal the lot quantity ${qty(lotQty)}.` } };
  }
  return {
    errors,
    input: lines.map((l) => ({ decision: l.decision, quantity: l.quantity.trim(), ...(l.reason.trim() ? { reason: l.reason.trim() } : {}) })),
  };
}

/** Usage decision (FR-33.6, BR-33.7): split the lot over decisions whose quantities sum to the lot. */
export function UsageDecisionForm({ order, onPosted }: { order: OrderView; onPosted(): void }) {
  const [lines, setLines] = useState<LineValues[]>([{ decision: order.evaluation.passed ? "ACCEPT" : "REJECT", quantity: qty(order.lotQty), reason: "" }]);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const missing = order.evaluation.missingRequired.length > 0;

  function update(i: number, patch: Partial<LineValues>) {
    setLines(lines.map((l, j) => (j === i ? { ...l, ...patch } : l)));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const checked = validateDecision(lines, order.lotQty);
    setErrors(checked.errors);
    if (!checked.input) return;
    setBusy(true);
    try {
      await postDecision(order.id, order.version, checked.input, key.current);
      onPosted();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "conflict_version" || problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Usage decision">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Usage decision">
        <p className={`${styles.note} ${styles.formActions}`}>
          {`Lot ${qty(order.lotQty)} ${order.uom}. Accepted quantity is released; rejected, rework, return and scrap quantities stay blocked for their follow-up.`}
        </p>
        {lines.map((line, i) => (
          <fieldset key={i} className={styles.valueGroup}>
            <legend className={styles.legend}>{`Line ${i + 1}`}</legend>
            <FormField id={`decision-${i}-kind`} label="Decision">
              {(control) => (
                <Select {...control} value={line.decision} onChange={(event) => update(i, { decision: event.target.value as DecisionKind })}>
                  {(Object.keys(DECISION_LABELS) as DecisionKind[]).map((d) => (
                    <option key={d} value={d}>
                      {DECISION_LABELS[d]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id={`decision-${i}-quantity`} label="Quantity" error={errors[`${i}.quantity`]}>
              {(control) => (
                <Input {...control} inputMode="decimal" value={line.quantity} invalid={Boolean(errors[`${i}.quantity`])} onChange={(event) => update(i, { quantity: event.target.value })} />
              )}
            </FormField>
            <FormField id={`decision-${i}-reason`} label="Reason" optional={line.decision !== "ACCEPT_WITH_DEVIATION"} error={errors[`${i}.reason`]}>
              {(control) => <Input {...control} value={line.reason} invalid={Boolean(errors[`${i}.reason`])} onChange={(event) => update(i, { reason: event.target.value })} />}
            </FormField>
            {lines.length > 1 ? (
              <Button variant="ghost" size="sm" onClick={() => setLines(lines.filter((_, j) => j !== i))}>
                {`Remove line ${i + 1}`}
              </Button>
            ) : null}
          </fieldset>
        ))}
        {errors.total || failure || missing ? (
          <div className={styles.formActions}>
            {missing ? (
              <Alert tone="info">Record a result for every required test on every sample before deciding.</Alert>
            ) : null}
            {errors.total ? (
              <Alert tone="danger" live="alert">
                {errors.total}
              </Alert>
            ) : null}
            {failure ? (
              <Alert tone="danger" title="Could not post the decision" live="alert">
                {failure.code === "conflict_version" ? "The order changed since you opened it. Reload and decide again." : failure.message} ({failure.code})
              </Alert>
            ) : null}
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button onClick={() => setLines([...lines, { decision: "REJECT", quantity: "", reason: "" }])}>Add decision line</Button>
          <Button type="submit" variant="primary" loading={busy} disabled={missing}>
            Post usage decision
          </Button>
        </div>
      </form>
    </Card>
  );
}
