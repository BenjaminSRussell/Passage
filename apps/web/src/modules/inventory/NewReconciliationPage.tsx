import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { describeProblem, runReconciliation, type Problem } from "./api/client";
import { DECIMAL, UUID } from "./format";
import { fieldErrorsOf } from "./parts";
import styles from "./pages.module.css";

export interface ReconcileValues {
  legalEntityId: string;
  currency: string;
  ledgerBalance: string;
}

const fields = ["legalEntityId", "currency", "ledgerBalance"] as const;
type Errors = Partial<Record<(typeof fields)[number], string>>;

/** Client checks that mirror the service: a UUID, an ISO currency, at most 6 decimals. */
export function validateReconcile(values: ReconcileValues): Errors {
  const errors: Errors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!/^[A-Z]{3}$/.test(values.currency.trim())) errors.currency = "Enter a three-letter currency code, for example USD.";
  const balance = values.ledgerBalance.trim();
  if (balance && (!DECIMAL.test(balance) || (balance.split(".")[1]?.length ?? 0) > 6)) {
    errors.ledgerBalance = "Enter a number with at most 6 decimals, or leave it empty.";
  }
  return errors;
}

/** Runs the inventory-to-ledger reconciliation for one legal entity now (InventoryAdmin). */
export function NewReconciliationPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<ReconcileValues>({ legalEntityId: "", currency: "USD", ledgerBalance: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const set = (name: keyof ReconcileValues) => (event: { target: { value: string } }) => setValues({ ...values, [name]: event.target.value });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateReconcile(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const run = await runReconciliation({
        legalEntityId: values.legalEntityId.trim(),
        currency: values.currency.trim(),
        ledgerBalance: values.ledgerBalance.trim() || undefined,
      });
      navigate(`/inventory/reconciliations/${run.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const mapped = fieldErrorsOf(problem, fields);
      if (Object.keys(mapped).length > 0) setErrors(mapped);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Run reconciliation"
        description="Compares the legal entity's on-hand value with the ledger balance of the inventory account. Running it again today recomputes today's run."
      />
      <Card>
        <form onSubmit={submit} noValidate className={styles.form}>
          <FormField id="recon-legal-entity" label="Legal entity" error={errors.legalEntityId}>
            {(control) => <Input {...control} value={values.legalEntityId} onChange={set("legalEntityId")} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="recon-currency" label="Currency" hint="The accounting currency of the stock value." error={errors.currency}>
            {(control) => <Input {...control} value={values.currency} onChange={set("currency")} invalid={Boolean(errors.currency)} />}
          </FormField>
          <FormField
            id="recon-ledger-balance"
            label="Ledger balance (optional)"
            hint="The inventory account balance from the ledger, including manual journals. Leave empty to compare with the posted inventory journals."
            error={errors.ledgerBalance}
          >
            {(control) => <Input {...control} inputMode="decimal" value={values.ledgerBalance} onChange={set("ledgerBalance")} invalid={Boolean(errors.ledgerBalance)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not run the reconciliation" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run reconciliation
            </Button>
            <Link className={buttonClassName("ghost")} to="/inventory/reconciliations">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
