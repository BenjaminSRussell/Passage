import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { describeProblem, listTolerances, newKey, putTolerance, type Problem, type Tolerance, type ToleranceRequest } from "./api/client";
import { ANY_TOTAL, TOLERANCE_PACKS, TOLERANCE_TOTALS, type Pack } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export interface ToleranceValues {
  pack: Pack;
  total: string;
  amount: string;
  reason: string;
}

type Field = keyof ToleranceValues;
type Errors = Partial<Record<Field, string>>;

const AMOUNT = /^\d{1,22}(\.\d{1,6})?$/;

/** The checks the service makes on a tolerance, and the request the form sends. `existing` is the stored tolerance of the same total. */
export function validateTolerance(values: ToleranceValues, existing?: Tolerance): { errors: Errors; input?: ToleranceRequest } {
  const errors: Errors = {};
  const total = values.total.trim();
  const totals = TOLERANCE_TOTALS[values.pack];
  if (!totals) errors.pack = `${values.pack} has no money totals; its totals compare at 0.00.`;
  else if (total !== ANY_TOTAL && !totals.includes(total)) errors.total = `Choose a money total of ${values.pack}, or ${ANY_TOTAL} for all of them.`;
  const amount = values.amount.trim();
  if (!AMOUNT.test(amount)) errors.amount = "Enter an amount of 0 or more with at most 6 decimals.";
  const reason = values.reason.trim();
  if (AMOUNT.test(amount) && Number(amount) > 0 && reason === "") errors.reason = "A tolerance above 0 needs a reason.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { pack: values.pack, total, amount, ...(reason !== "" ? { reason } : {}), ...(existing ? { version: existing.version } : {}) } };
}

/** Set or change the tolerance of a pack's control total (FR-49.2). */
export function SetTolerancePage() {
  const [params] = useSearchParams();
  const initialPack = TOLERANCE_PACKS.find((p) => p === params.get("pack")) ?? "open_items";
  const requested = params.get("total") ?? ANY_TOTAL;
  const initialTotal = TOLERANCE_TOTALS[initialPack]?.includes(requested) ? requested : ANY_TOTAL;
  const state = useLoad((signal) => listTolerances(signal), "tolerances");
  const navigate = useNavigate();
  const [values, setValues] = useState<ToleranceValues>({ pack: initialPack, total: initialTotal, amount: "", reason: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const existing = state.data?.find((t) => t.pack === values.pack && t.total === values.total.trim());

  function update(patch: Partial<ToleranceValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateTolerance(values, existing);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await putTolerance(input, key.current);
      navigate("/migration-factory/tolerances");
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "MIG-0007") key.current = newKey();
      if (problem.code === "MIG-0006") state.reload();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Set control-total tolerance"
        description="The allowance for one money total of a pack, or for every money total of the pack that has none of its own, in each total's own currency. Counts and quantities always match exactly. Set 0 to return a total to an exact comparison. Runs already started keep the tolerance they were reconciled with."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="tol-pack" label="Pack" error={errors.pack}>
            {(control) => (
              <Select {...control} value={values.pack} onChange={(event) => update({ pack: event.target.value as Pack, total: ANY_TOTAL })}>
                {TOLERANCE_PACKS.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="tol-total" label="Control total" hint="A money total of the pack. The tolerance is the absolute delta allowed in each total's own currency." error={errors.total}>
            {(control) => (
              <Select {...control} value={values.total} onChange={(event) => update({ total: event.target.value })}>
                <option value={ANY_TOTAL}>Every money total of the pack ({ANY_TOTAL})</option>
                {(TOLERANCE_TOTALS[values.pack] ?? []).map((t) => (
                  <option key={t} value={t}>
                    {t}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField
            id="tol-amount"
            label="Tolerance"
            hint={existing ? `Currently ${existing.amount} (version ${existing.version}).` : "No tolerance is set for this total yet."}
            error={errors.amount}
          >
            {(control) => <Input {...control} inputMode="decimal" value={values.amount} invalid={Boolean(errors.amount)} onChange={(event) => update({ amount: event.target.value })} />}
          </FormField>
          <FormField id="tol-reason" label="Reason" hint="Required above 0, for example the legacy rounding it absorbs." optional error={errors.reason}>
            {(control) => <Input {...control} value={values.reason} invalid={Boolean(errors.reason)} onChange={(event) => update({ reason: event.target.value })} />}
          </FormField>
          {state.error ? (
            <div className={styles.formActions}>
              <Alert tone="warning" title="Could not read the current tolerances" live="status">
                <ProblemMessage problem={state.error} /> Saving an existing tolerance needs its version.
              </Alert>
            </div>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not set the tolerance" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save tolerance
            </Button>
            <Link className={buttonClassName("ghost")} to="/migration-factory/tolerances">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
