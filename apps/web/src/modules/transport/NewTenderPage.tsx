import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { describeProblem, newKey, startTender, type NewTender, type Problem } from "./api/client";
import { CURRENCY, UUID } from "./format";
import styles from "./pages.module.css";
import { RowsEditor, type RowField } from "./RowsEditor";

export interface TenderValues {
  loadId: string;
  carriers: Record<string, string>[];
  currency: string;
  timeoutMin: string;
}

const carrierFields: RowField[] = [
  { key: "carrierId", label: "Carrier id", mono: true, hint: "Rank order: the first carrier is asked first." },
  { key: "rateQuoted", label: "Rate quoted", decimal: true, hint: "Optional, such as the rate shop total." },
];

const blankCarrier = () => ({ carrierId: "", rateQuoted: "" });

/** Checks that mirror startTender in the service, and the request the form sends. */
export function validateTender(v: TenderValues): { errors: Record<string, string>; loadId?: string; input?: NewTender } {
  const errors: Record<string, string> = {};
  const loadId = v.loadId.trim();
  if (!UUID.test(loadId)) errors.loadId = "Enter the load id as a lower-case UUID.";
  const seen = new Set<string>();
  const carriers = v.carriers.map((c, i) => {
    const carrierId = c.carrierId.trim();
    const rate = (c.rateQuoted ?? "").trim();
    if (!UUID.test(carrierId)) errors[`carriers[${i}]`] = "Enter the carrier id as a lower-case UUID.";
    else if (seen.has(carrierId)) errors[`carriers[${i}]`] = "List each carrier once.";
    else if (rate !== "" && !/^\d{1,14}(\.\d{1,2})?$/.test(rate)) errors[`carriers[${i}]`] = "Enter the rate with at most two decimals, such as 412.50.";
    seen.add(carrierId);
    return rate === "" ? { carrierId } : { carrierId, rateQuoted: rate };
  });
  if (carriers.length === 0 || carriers.length > 20) errors.carriers = "Add between 1 and 20 carriers.";
  const rated = carriers.some((c) => c.rateQuoted !== undefined);
  if (rated && !CURRENCY.test(v.currency)) errors.currency = "Enter the currency of the quoted rates, such as USD.";
  const timeout = v.timeoutMin.trim() === "" ? 120 : Number(v.timeoutMin);
  if (!Number.isInteger(timeout) || timeout < 5 || timeout > 1440) errors.timeoutMin = "Enter whole minutes from 5 to 1440.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, loadId, input: { carriers, ...(rated ? { currency: v.currency } : {}), timeoutMin: timeout } };
}

/** Offer a load to carriers in route-guide order (FR-34.5). */
export function NewTenderPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [values, setValues] = useState<TenderValues>({ loadId: params.get("loadId") ?? "", carriers: [blankCarrier()], currency: "USD", timeoutMin: "120" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<TenderValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, loadId, input } = validateTender(values);
    setErrors(found);
    setFailure(null);
    if (!input || !loadId) return;
    setBusy(true);
    try {
      const tender = await startTender(loadId, input, key.current);
      navigate(`/transport/tenders/${tender.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Start tender" description="The load goes to the first carrier now. Each carrier has the response time you set before the load moves on to the next." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="tender-load" label="Load id" hint="The load to tender (UUID)." error={errors.loadId}>
            {(control) => <Input {...control} className={styles.mono} value={values.loadId} onChange={(e) => update({ loadId: e.target.value })} invalid={Boolean(errors.loadId)} />}
          </FormField>
          <FormField id="tender-timeout" label="Response time (minutes)" hint="Per carrier; 5 to 1440, default 120." error={errors.timeoutMin}>
            {(control) => <Input {...control} inputMode="numeric" value={values.timeoutMin} onChange={(e) => update({ timeoutMin: e.target.value })} invalid={Boolean(errors.timeoutMin)} />}
          </FormField>
          <FormField id="tender-currency" label="Currency" hint="For the quoted rates." error={errors.currency}>
            {(control) => (
              <Input {...control} className={styles.mono} maxLength={3} value={values.currency} onChange={(e) => update({ currency: e.target.value.toUpperCase() })} invalid={Boolean(errors.currency)} />
            )}
          </FormField>
          <RowsEditor
            field="carriers"
            legend="Carriers in rank order"
            noun="carrier"
            fields={carrierFields}
            rows={values.carriers}
            errors={errors}
            blank={blankCarrier}
            onChange={(carriers) => update({ carriers })}
          />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not start the tender" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Start tender
            </Button>
            <Link className={buttonClassName("ghost")} to="/transport/tenders">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
