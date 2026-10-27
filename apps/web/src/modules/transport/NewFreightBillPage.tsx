import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { createFreightBill, describeProblem, newKey, type Line, type NewFreightBill, type Problem } from "./api/client";
import { ChargeLinesEditor, cleanLines, validateLines } from "./ChargeLinesEditor";
import { CURRENCY, UUID } from "./format";
import styles from "./pages.module.css";

interface Values {
  loadId: string;
  carrierId: string;
  currency: string;
  lines: Line[];
}

/** Checks that mirror ValidateFreightBill in the service, and the request the form sends. */
export function validateBill(v: Values): { errors: Record<string, string>; input?: NewFreightBill } {
  const errors = validateLines(v.lines, "expectedLines");
  if (!UUID.test(v.loadId.trim())) errors.loadId = "Enter the load id as a lower-case UUID.";
  if (!UUID.test(v.carrierId.trim())) errors.carrierId = "Enter the carrier id as a lower-case UUID.";
  if (!CURRENCY.test(v.currency)) errors.currency = "Enter a three-letter currency code such as USD.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { loadId: v.loadId.trim(), carrierId: v.carrierId.trim(), currency: v.currency, expectedLines: cleanLines(v.lines) } };
}

/** Record the expected charges for a load (FR-34.7). Ship confirmation will create these automatically in a later release. */
export function NewFreightBillPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Values>({ loadId: "", carrierId: "", currency: "USD", lines: [{ chargeCode: "BASE", amount: "" }] });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<Values>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateBill(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const bill = await createFreightBill(input, key.current);
      navigate(`/transport/freight-bills/${bill.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (id: "loadId" | "carrierId" | "currency", label: string, hint?: string) => (
    <FormField id={`bill-${id}`} label={label} hint={hint} error={errors[id]}>
      {(control) => (
        <Input
          {...control}
          className={styles.mono}
          value={values[id]}
          onChange={(e) => update({ [id]: id === "currency" ? e.target.value.toUpperCase() : e.target.value })}
          invalid={Boolean(errors[id])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New freight bill" description="The charges you expect the carrier to invoice for one load, usually taken from the chosen rate shop option." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("loadId", "Load id", "The shipped load (UUID).")}
          {text("carrierId", "Carrier id", "The carrier that hauls the load (UUID).")}
          {text("currency", "Currency")}
          <ChargeLinesEditor field="expectedLines" legend="Expected charges" lines={values.lines} errors={errors} onChange={(lines) => update({ lines })} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the freight bill" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create freight bill
            </Button>
            <Link className={buttonClassName("ghost")} to="/transport/freight-bills">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
