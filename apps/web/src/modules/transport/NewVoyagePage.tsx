import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import {
  ALLOCATION_METHODS,
  CONTAINER_TYPES,
  createVoyage,
  describeProblem,
  LEG_MODES,
  newKey,
  type AllocationMethod,
  type ContainerType,
  type LegMode,
  type NewVoyage,
  type Problem,
} from "./api/client";
import { AMOUNT, CURRENCY } from "./format";
import styles from "./pages.module.css";
import { RowsEditor, type RowField } from "./RowsEditor";
import { ProblemMessage } from "../../shell/errors";

type Rows = Record<string, string>[];

export interface VoyageValues {
  reference: string;
  currency: string;
  legs: Rows;
  containers: Rows;
  folios: Rows;
  costLines: Rows;
  receiptLines: Rows;
}

const CONTAINER = /^[A-Z]{4}\d{7}$/;
const COST = /^\d{1,14}(\.\d{1,2})?$/;

const legFields: RowField[] = [
  { key: "from", label: "From", upper: true, hint: "Port or place, such as CNSHA." },
  { key: "to", label: "To", upper: true },
  { key: "mode", label: "Mode", options: LEG_MODES },
];
const containerFields: RowField[] = [
  { key: "number", label: "Number", upper: true, mono: true, hint: "ISO 6346 with check digit, such as CSQU3054383." },
  { key: "type", label: "Type", options: CONTAINER_TYPES },
  { key: "seal", label: "Seal" },
];
const folioFields: RowField[] = [
  { key: "folioNo", label: "Folio number" },
  { key: "forwarder", label: "Vendor or forwarder" },
  { key: "containers", label: "Containers", upper: true, mono: true, hint: "Container numbers, separated by commas." },
];
const costFields: RowField[] = [
  { key: "costType", label: "Cost type", upper: true, hint: "FREIGHT, DUTY, BROKERAGE, INSURANCE …" },
  { key: "amount", label: "Amount", decimal: true },
  { key: "allocationMethod", label: "Allocate by", options: ALLOCATION_METHODS },
];
const receiptFields: RowField[] = [
  { key: "line", label: "Line", hint: "Purchase receipt line number." },
  { key: "value", label: "Value", decimal: true },
  { key: "weight", label: "Weight", decimal: true },
  { key: "volume", label: "Volume", decimal: true },
  { key: "quantity", label: "Quantity", decimal: true },
];

const blank = {
  leg: () => ({ from: "", to: "", mode: "OCEAN" }),
  container: () => ({ number: "", type: "40HC", seal: "" }),
  folio: () => ({ folioNo: "", forwarder: "", containers: "" }),
  cost: () => ({ costType: "FREIGHT", amount: "", allocationMethod: "WEIGHT" }),
  receipt: () => ({ line: "", value: "0", weight: "0", volume: "0", quantity: "0" }),
};

const decimalOrZero = (s: string | undefined) => (s ?? "").trim() || "0";

/** Checks that mirror ValidateVoyage in the service (the check digit is the service's), and the request the form sends. */
export function validateVoyage(v: VoyageValues): { errors: Record<string, string>; input?: NewVoyage } {
  const errors: Record<string, string> = {};
  const reference = v.reference.trim();
  if (reference.length < 1 || reference.length > 40) errors.reference = "Enter a reference of up to 40 characters, such as the vessel voyage number.";
  if (!CURRENCY.test(v.currency)) errors.currency = "Enter a three-letter currency code such as USD.";
  if (v.legs.length === 0) errors.legs = "Add at least one leg.";
  v.legs.forEach((l, i) => {
    if (!l.from.trim() || !l.to.trim()) errors[`legs[${i}]`] = "Enter where the leg starts and ends.";
  });
  const numbers = v.containers.map((c) => c.number.trim());
  numbers.forEach((_, i) => {
    if (!CONTAINER.test(numbers[i])) errors[`containers[${i}]`] = "Enter four letters and seven digits, such as CSQU3054383.";
    else if (numbers.indexOf(numbers[i]) !== i) errors[`containers[${i}]`] = "Each container appears once.";
  });
  const folioContainers = v.folios.map((f) =>
    f.containers
      .split(",")
      .map((c) => c.trim())
      .filter(Boolean),
  );
  v.folios.forEach((f, i) => {
    if (!f.folioNo.trim() || !f.forwarder.trim()) errors[`folios[${i}]`] = "Enter the folio number and the vendor or forwarder.";
    else if (folioContainers[i].some((c) => !numbers.includes(c))) errors[`folios[${i}]`] = "Only list containers that are on this voyage.";
  });
  if (v.costLines.length === 0) errors.costLines = "Add at least one cost line.";
  v.costLines.forEach((c, i) => {
    if (!c.costType.trim() || c.costType.trim().length > 20) errors[`costLines[${i}]`] = "Enter a cost type of up to 20 characters.";
    else if (!COST.test(c.amount.trim())) errors[`costLines[${i}]`] = "Enter the amount with at most two decimals, such as 1000.00.";
  });
  if (v.receiptLines.length === 0) errors.receiptLines = "Add at least one receipt line.";
  const lines = v.receiptLines.map((r) => Number(r.line));
  v.receiptLines.forEach((r, i) => {
    if (!Number.isInteger(lines[i]) || lines[i] < 1) errors[`receiptLines[${i}]`] = "Enter the receipt line number.";
    else if (lines.indexOf(lines[i]) !== i) errors[`receiptLines[${i}]`] = "Each receipt line appears once.";
    else if (["value", "weight", "volume", "quantity"].some((k) => !AMOUNT.test(decimalOrZero(r[k])))) errors[`receiptLines[${i}]`] = "Enter value, weight, volume and quantity as numbers of zero or more.";
  });
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      reference,
      currency: v.currency,
      legs: v.legs.map((l) => ({ from: l.from.trim(), to: l.to.trim(), mode: l.mode as LegMode })),
      containers: v.containers.map((c, i) => ({ number: numbers[i], type: c.type as ContainerType, ...(c.seal.trim() ? { seal: c.seal.trim() } : {}) })),
      folios: v.folios.map((f, i) => ({ folioNo: f.folioNo.trim(), forwarder: f.forwarder.trim(), containers: folioContainers[i] })),
      costLines: v.costLines.map((c) => ({ costType: c.costType.trim(), amount: c.amount.trim(), allocationMethod: c.allocationMethod as AllocationMethod })),
      receiptLines: v.receiptLines.map((r, i) => ({
        line: lines[i],
        value: decimalOrZero(r.value),
        weight: decimalOrZero(r.weight),
        volume: decimalOrZero(r.volume),
        quantity: decimalOrZero(r.quantity),
      })),
    },
  };
}

/** Create a landed-cost voyage (FR-34.12) with its costs and the receipt lines that absorb them (FR-34.9). */
export function NewVoyagePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<VoyageValues>({
    reference: "",
    currency: "USD",
    legs: [blank.leg()],
    containers: [blank.container()],
    folios: [],
    costLines: [blank.cost()],
    receiptLines: [{ ...blank.receipt(), line: "1" }],
  });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<VoyageValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateVoyage(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const voyage = await createVoyage(input, key.current);
      navigate(`/transport/voyages/${voyage.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      if (Object.keys(problem.fields).length === 0 || problem.code === "TMS-0124") setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New voyage" description="Costs are estimates until you allocate the voyage. Each cost is split over the receipt lines by the basis you choose; rounding cents go to the highest-value line." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="voyage-reference" label="Reference" hint="Vessel and voyage number, or your shipment reference." error={errors.reference}>
            {(control) => <Input {...control} className={styles.mono} value={values.reference} onChange={(e) => update({ reference: e.target.value })} invalid={Boolean(errors.reference)} />}
          </FormField>
          <FormField id="voyage-currency" label="Currency" error={errors.currency}>
            {(control) => (
              <Input {...control} className={styles.mono} maxLength={3} value={values.currency} onChange={(e) => update({ currency: e.target.value.toUpperCase() })} invalid={Boolean(errors.currency)} />
            )}
          </FormField>
          <RowsEditor field="legs" legend="Legs" noun="leg" fields={legFields} rows={values.legs} errors={errors} blank={blank.leg} onChange={(legs) => update({ legs })} />
          <RowsEditor
            field="containers"
            legend="Containers"
            noun="container"
            fields={containerFields}
            rows={values.containers}
            errors={errors}
            min={0}
            blank={blank.container}
            onChange={(containers) => update({ containers })}
          />
          <RowsEditor field="folios" legend="Folios" noun="folio" fields={folioFields} rows={values.folios} errors={errors} min={0} blank={blank.folio} onChange={(folios) => update({ folios })} />
          <RowsEditor field="costLines" legend="Landed costs" noun="cost line" fields={costFields} rows={values.costLines} errors={errors} blank={blank.cost} onChange={(costLines) => update({ costLines })} />
          <RowsEditor
            field="receiptLines"
            legend="Receipt lines"
            noun="receipt line"
            fields={receiptFields}
            rows={values.receiptLines}
            errors={errors}
            blank={() => ({ ...blank.receipt(), line: String(values.receiptLines.length + 1) })}
            onChange={(receiptLines) => update({ receiptLines })}
          />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the voyage" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create voyage
            </Button>
            <Link className={buttonClassName("ghost")} to="/transport/voyages">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
