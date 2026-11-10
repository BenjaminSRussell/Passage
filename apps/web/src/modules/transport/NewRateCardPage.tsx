import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import {
  BASES,
  createRateCard,
  describeProblem,
  newKey,
  UNITS,
  type Basis,
  type NewRateCard,
  type Problem,
  type RateUnit,
} from "./api/client";
import { AMOUNT, CURRENCY, DATE, UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

interface RowValues {
  origin: string;
  destination: string;
  breakFrom: string;
  breakTo: string;
  rate: string;
  unit: RateUnit;
}

interface AccessorialValues {
  code: string;
  kind: "flat" | "pct";
  amount: string;
}

export interface CardValues {
  carrierId: string;
  serviceId: string;
  basis: Basis;
  currency: string;
  validFrom: string;
  validTo: string;
  minimumCharge: string;
  dimDivisor: string;
  fuelPct: string;
  rows: RowValues[];
  accessorials: AccessorialValues[];
}

type Errors = Record<string, string>;

const emptyRow = (): RowValues => ({ origin: "*", destination: "*", breakFrom: "0", breakTo: "", rate: "", unit: "KG" });

/** Checks that mirror RateCard.Validate in the service, and the request the form sends. */
export function validateCard(v: CardValues): { errors: Errors; input?: NewRateCard } {
  const errors: Errors = {};
  if (!UUID.test(v.carrierId.trim())) errors.carrierId = "Enter the carrier id as a lower-case UUID.";
  if (!UUID.test(v.serviceId.trim())) errors.serviceId = "Enter the carrier service id as a lower-case UUID.";
  if (!CURRENCY.test(v.currency)) errors.currency = "Enter a three-letter currency code such as USD.";
  if (!DATE.test(v.validFrom)) errors.validFrom = "Enter the first day the card applies.";
  if (v.validTo !== "" && (!DATE.test(v.validTo) || v.validTo < v.validFrom)) errors.validTo = "Enter a last day on or after the first day, or leave it empty.";
  if (!AMOUNT.test(v.minimumCharge.trim())) errors.minimumCharge = "Enter an amount such as 15.00, or 0 for none.";
  if (v.dimDivisor.trim() !== "" && !/^[1-9]\d{0,5}$/.test(v.dimDivisor.trim())) errors.dimDivisor = "Enter a whole number of cm³ per kg, such as 5000.";
  if (!AMOUNT.test(v.fuelPct.trim())) errors.fuelPct = "Enter a percentage such as 18.5, or 0 for none.";
  if (v.rows.length === 0) errors.rows = "Add at least one rate row.";
  v.rows.forEach((r, i) => {
    if (r.origin.trim() === "" || r.destination.trim() === "") errors[`rows[${i}]`] = "Enter an origin and destination zone; use * for any.";
    else if (!AMOUNT.test(r.breakFrom.trim()) || !AMOUNT.test(r.rate.trim())) errors[`rows[${i}]`] = "Enter a break from and a rate as decimals.";
    else if (r.breakTo.trim() !== "" && (!AMOUNT.test(r.breakTo.trim()) || Number(r.breakTo) <= Number(r.breakFrom)))
      errors[`rows[${i}]`] = "The break to must be above the break from, or empty for no upper bound.";
  });
  v.accessorials.forEach((a, i) => {
    if (a.code.trim() === "" || a.code.trim().length > 20 || !AMOUNT.test(a.amount.trim()))
      errors[`accessorials[${i}]`] = "Enter a code of up to 20 characters and an amount.";
  });
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewRateCard = {
    carrierId: v.carrierId.trim(),
    serviceId: v.serviceId.trim(),
    basis: v.basis,
    currency: v.currency,
    validFrom: v.validFrom,
    validTo: v.validTo === "" ? null : v.validTo,
    minimumCharge: v.minimumCharge.trim(),
    fuelPct: v.fuelPct.trim(),
    rows: v.rows.map((r) => ({
      origin: r.origin.trim().toUpperCase(),
      destination: r.destination.trim().toUpperCase(),
      breakFrom: r.breakFrom.trim(),
      breakTo: r.breakTo.trim() === "" ? null : r.breakTo.trim(),
      rate: r.rate.trim(),
      unit: r.unit,
    })),
    accessorials: v.accessorials.map((a) => ({
      code: a.code.trim().toUpperCase(),
      flat: a.kind === "flat" ? a.amount.trim() : "0",
      pct: a.kind === "pct" ? a.amount.trim() : "0",
    })),
  };
  if (v.dimDivisor.trim() !== "") input.dimDivisor = Number(v.dimDivisor.trim());
  return { errors, input };
}

const initial: CardValues = {
  carrierId: "",
  serviceId: "",
  basis: "LANE",
  currency: "USD",
  validFrom: "",
  validTo: "",
  minimumCharge: "0",
  dimDivisor: "",
  fuelPct: "0",
  rows: [emptyRow()],
  accessorials: [],
};

/** Create a carrier rate card (transport.rate-card.approve). Overlapping validity is refused with TMS-0102. */
export function NewRateCardPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<CardValues>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<CardValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }
  const updateRow = (i: number, patch: Partial<RowValues>) => update({ rows: values.rows.map((r, idx) => (idx === i ? { ...r, ...patch } : r)) });
  const updateAcc = (i: number, patch: Partial<AccessorialValues>) =>
    update({ accessorials: values.accessorials.map((a, idx) => (idx === i ? { ...a, ...patch } : a)) });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateCard(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createRateCard(input, key.current);
      navigate("/transport/rate-cards");
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      if (problem.code === "TMS-0102") setErrors({ validFrom: `${problem.message}. Adjust the validity so the cards do not share a day.` });
      else if (Object.keys(problem.fields).length === 0) setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (id: keyof CardValues, label: string, hint?: string, optional?: boolean, extra?: Record<string, unknown>) => (
    <FormField id={`card-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input
          {...control}
          {...extra}
          value={values[id] as string}
          onChange={(e) => update({ [id]: e.target.value } as Partial<CardValues>)}
          invalid={Boolean(errors[id])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New rate card"
        description="Rows price a lane or zone pair inside a break band; the most specific matching row wins. Accessorials are flat or a percentage of the base charge, and the fuel surcharge is a percentage of the base."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("carrierId", "Carrier id", "The carrier party (UUID).", false, { className: styles.mono })}
          {text("serviceId", "Service id", "The carrier service (UUID).", false, { className: styles.mono })}
          <FormField id="card-basis" label="Basis" error={errors.basis}>
            {(control) => (
              <Select {...control} value={values.basis} onChange={(e) => update({ basis: e.target.value as Basis })}>
                {BASES.map((b) => (
                  <option key={b} value={b}>
                    {b}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("currency", "Currency", undefined, false, { maxLength: 3, className: styles.mono })}
          {text("validFrom", "Valid from", undefined, false, { type: "date" })}
          {text("validTo", "Valid to", "Inclusive; empty means open-ended.", true, { type: "date" })}
          {text("minimumCharge", "Minimum charge", undefined, false, { inputMode: "decimal" })}
          {text("fuelPct", "Fuel surcharge %", "Percent of the base charge.", false, { inputMode: "decimal" })}
          {text("dimDivisor", "Dimensional divisor", "cm³ per kg; empty means 5000.", true, { inputMode: "numeric" })}

          <fieldset className={styles.valueGroup}>
            <legend className={styles.legend}>Rate rows</legend>
            {values.rows.map((row, i) => (
              <div key={i} className={styles.lineRow}>
                <FormField id={`row-${i}-origin`} label={`Row ${i + 1} origin`} error={errors[`rows[${i}]`]}>
                  {(control) => <Input {...control} className={styles.mono} value={row.origin} onChange={(e) => updateRow(i, { origin: e.target.value })} />}
                </FormField>
                <FormField id={`row-${i}-destination`} label={`Row ${i + 1} destination`}>
                  {(control) => (
                    <Input {...control} className={styles.mono} value={row.destination} onChange={(e) => updateRow(i, { destination: e.target.value })} />
                  )}
                </FormField>
                <FormField id={`row-${i}-from`} label={`Row ${i + 1} break from`}>
                  {(control) => <Input {...control} inputMode="decimal" value={row.breakFrom} onChange={(e) => updateRow(i, { breakFrom: e.target.value })} />}
                </FormField>
                <FormField id={`row-${i}-to`} label={`Row ${i + 1} break to`} optional>
                  {(control) => <Input {...control} inputMode="decimal" value={row.breakTo} onChange={(e) => updateRow(i, { breakTo: e.target.value })} />}
                </FormField>
                <FormField id={`row-${i}-rate`} label={`Row ${i + 1} rate`}>
                  {(control) => <Input {...control} inputMode="decimal" value={row.rate} onChange={(e) => updateRow(i, { rate: e.target.value })} />}
                </FormField>
                <FormField id={`row-${i}-unit`} label={`Row ${i + 1} unit`}>
                  {(control) => (
                    <Select {...control} value={row.unit} onChange={(e) => updateRow(i, { unit: e.target.value as RateUnit })}>
                      {UNITS.map((u) => (
                        <option key={u} value={u}>
                          {u}
                        </option>
                      ))}
                    </Select>
                  )}
                </FormField>
                {values.rows.length > 1 ? (
                  <Button variant="ghost" size="sm" onClick={() => update({ rows: values.rows.filter((_, idx) => idx !== i) })}>
                    Remove row {i + 1}
                  </Button>
                ) : (
                  <span />
                )}
              </div>
            ))}
            <div className={styles.actions}>
              <Button size="sm" onClick={() => update({ rows: [...values.rows, emptyRow()] })}>
                Add rate row
              </Button>
            </div>
          </fieldset>

          <fieldset className={styles.valueGroup}>
            <legend className={styles.legend}>Accessorials</legend>
            {values.accessorials.length === 0 ? <p className={styles.note}>No accessorials. Add LIFTGATE, RESIDENTIAL and the like if the carrier charges them.</p> : null}
            {values.accessorials.map((acc, i) => (
              <div key={i} className={styles.lineRow}>
                <FormField id={`acc-${i}-code`} label={`Accessorial ${i + 1} code`} error={errors[`accessorials[${i}]`]}>
                  {(control) => <Input {...control} className={styles.mono} maxLength={20} value={acc.code} onChange={(e) => updateAcc(i, { code: e.target.value })} />}
                </FormField>
                <FormField id={`acc-${i}-kind`} label={`Accessorial ${i + 1} kind`}>
                  {(control) => (
                    <Select {...control} value={acc.kind} onChange={(e) => updateAcc(i, { kind: e.target.value as "flat" | "pct" })}>
                      <option value="flat">Flat amount</option>
                      <option value="pct">Percent of base</option>
                    </Select>
                  )}
                </FormField>
                <FormField id={`acc-${i}-amount`} label={`Accessorial ${i + 1} amount`}>
                  {(control) => <Input {...control} inputMode="decimal" value={acc.amount} onChange={(e) => updateAcc(i, { amount: e.target.value })} />}
                </FormField>
                <Button variant="ghost" size="sm" onClick={() => update({ accessorials: values.accessorials.filter((_, idx) => idx !== i) })}>
                  Remove accessorial {i + 1}
                </Button>
              </div>
            ))}
            <div className={styles.actions}>
              <Button size="sm" onClick={() => update({ accessorials: [...values.accessorials, { code: "", kind: "flat", amount: "" }] })}>
                Add accessorial
              </Button>
            </div>
          </fieldset>

          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the rate card" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save rate card
            </Button>
            <Link className={buttonClassName("ghost")} to="/transport/rate-cards">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
