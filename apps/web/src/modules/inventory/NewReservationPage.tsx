import { useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  createReservation,
  DEMAND_TYPES,
  describeProblem,
  RESERVATION_POLICIES,
  type DemandType,
  type Problem,
  type Reservation,
  type ReservationPolicy,
  type ReserveResult,
} from "./api/client";
import { DECIMAL, describeDimensions, sentence, today, UUID } from "./format";
import { ALL_DIMENSIONS, emptyDimensions, fieldErrorsOf, FORM_DIMENSIONS, type FormDimension } from "./parts";
import styles from "./pages.module.css";

export interface ReservationValues {
  legalEntityId: string;
  productId: string;
  qty: string;
  policy: ReservationPolicy;
  demandType: DemandType;
  demandId: string;
  requiredDate: string;
  shelfLifeMarginDays: string;
  allowPartial: boolean;
  dimensions: Record<FormDimension, string>;
}

const fields = ["legalEntityId", "productId", "qty", "policy", "demandType", "demandId", "requiredDate", "shelfLifeMarginDays", "dimensions"] as const;
type Field = (typeof fields)[number];
type Errors = Partial<Record<Field, string>>;

const policyHints: Record<ReservationPolicy, string> = {
  FIFO: "Oldest receipts first, across the dimension sets that match the dimensions you enter.",
  FEFO: "Earliest expiry first; batches expiring inside the shelf-life margin are skipped.",
  LIFO: "Newest receipts first.",
  MANUAL: "Exactly the dimension set you enter, including its site.",
};

/** Client checks that mirror the service, so most mistakes never leave the form. */
export function validateReservation(values: ReservationValues): Errors {
  const errors: Errors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!UUID.test(values.productId.trim())) errors.productId = "Enter the product id (a UUID).";
  const qty = values.qty.trim();
  if (!DECIMAL.test(qty) || Number(qty) <= 0) errors.qty = "Enter a quantity above zero.";
  if (values.demandId.trim() === "") errors.demandId = "Enter the order or reference the stock is held for.";
  const margin = values.shelfLifeMarginDays.trim();
  if (margin !== "" && (!/^\d+$/.test(margin) || Number(margin) > 365)) errors.shelfLifeMarginDays = "Enter 0 to 365 days.";
  if (values.policy === "MANUAL" && values.dimensions.site.trim() === "") errors.dimensions = "A manual reservation names the site and every dimension of the stock.";
  return errors;
}

const allocationColumns: DataTableColumn<Reservation>[] = [
  { key: "allocationNo", header: "Allocation", align: "end" },
  { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, ALL_DIMENSIONS) },
  { key: "qty", header: "Quantity", align: "end", mono: true },
];

const blank = (): ReservationValues => ({
  legalEntityId: "",
  productId: "",
  qty: "",
  policy: "FIFO",
  demandType: "SALES_ORDER",
  demandId: "",
  requiredDate: today(),
  shelfLifeMarginDays: "0",
  allowPartial: false,
  dimensions: emptyDimensions(),
});

/** Reservation dialog (PRD 3.16.6): reserve by policy or for one exact dimension set. */
export function NewReservationPage() {
  const [values, setValues] = useState<ReservationValues>(blank);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<ReserveResult | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per filled-in form: a double submit replays instead of reserving twice.
  const key = useRef(crypto.randomUUID());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateReservation(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const out = await createReservation(
        {
          legalEntityId: values.legalEntityId.trim(),
          productId: values.productId.trim(),
          qty: values.qty.trim(),
          policy: values.policy,
          demandType: values.demandType,
          demandId: values.demandId.trim(),
          requiredDate: values.requiredDate,
          shelfLifeMarginDays: Number(values.shelfLifeMarginDays || "0"),
          allowPartial: values.allowPartial,
          dimensions: values.dimensions,
        },
        key.current,
      );
      setResult(out);
      key.current = crypto.randomUUID();
    } catch (error) {
      const problem = describeProblem(error);
      const mapped = fieldErrorsOf(problem, fields);
      if (Object.keys(mapped).length > 0) setErrors(mapped);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = crypto.randomUUID();
    } finally {
      setBusy(false);
    }
  }

  const change = (next: ReservationValues) => {
    setValues(next);
    key.current = crypto.randomUUID();
  };
  const set = (name: Exclude<keyof ReservationValues, "dimensions" | "allowPartial">) => (event: { target: { value: string } }) =>
    change({ ...values, [name]: event.target.value });
  const setDim = (name: FormDimension) => (event: { target: { value: string } }) =>
    change({ ...values, dimensions: { ...values.dimensions, [name]: event.target.value } });

  return (
    <div className={styles.stack}>
      <PageHeader title="New reservation" description="Hold available stock for a demand so it cannot be issued elsewhere." />
      <Card>
        <form onSubmit={submit} noValidate className={styles.form}>
          <FormField id="res-legal-entity" label="Legal entity" error={errors.legalEntityId}>
            {(control) => <Input {...control} value={values.legalEntityId} onChange={set("legalEntityId")} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="res-product" label="Product" error={errors.productId}>
            {(control) => <Input {...control} value={values.productId} onChange={set("productId")} invalid={Boolean(errors.productId)} />}
          </FormField>
          <FormField id="res-qty" label="Quantity" error={errors.qty}>
            {(control) => <Input {...control} inputMode="decimal" value={values.qty} onChange={set("qty")} invalid={Boolean(errors.qty)} />}
          </FormField>
          <FormField id="res-policy" label="Policy" hint={policyHints[values.policy]}>
            {(control) => (
              <Select {...control} value={values.policy} onChange={set("policy")}>
                {RESERVATION_POLICIES.map((p) => (
                  <option key={p} value={p}>
                    {p === "MANUAL" ? "Manual" : p}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="res-demand-type" label="Demand type">
            {(control) => (
              <Select {...control} value={values.demandType} onChange={set("demandType")}>
                {DEMAND_TYPES.map((d) => (
                  <option key={d} value={d}>
                    {sentence(d)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="res-demand-id" label="Demand reference" error={errors.demandId}>
            {(control) => <Input {...control} value={values.demandId} maxLength={60} onChange={set("demandId")} invalid={Boolean(errors.demandId)} />}
          </FormField>
          <FormField id="res-required" label="Required date" error={errors.requiredDate}>
            {(control) => <Input {...control} type="date" value={values.requiredDate} onChange={set("requiredDate")} />}
          </FormField>
          <FormField id="res-margin" label="Shelf-life margin (days)" optional error={errors.shelfLifeMarginDays}>
            {(control) => (
              <Input {...control} inputMode="numeric" value={values.shelfLifeMarginDays} onChange={set("shelfLifeMarginDays")} invalid={Boolean(errors.shelfLifeMarginDays)} />
            )}
          </FormField>
          {FORM_DIMENSIONS.map((name) => (
            <FormField
              key={name}
              id={`res-${name}`}
              label={sentence(name)}
              optional={!(name === "site" && values.policy === "MANUAL")}
              error={name === "site" ? errors.dimensions : undefined}
            >
              {(control) => (
                <Input {...control} value={values.dimensions[name]} maxLength={60} onChange={setDim(name)} invalid={name === "site" && Boolean(errors.dimensions)} />
              )}
            </FormField>
          ))}
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input
                type="checkbox"
                id="res-partial"
                checked={values.allowPartial}
                onChange={(event) => change({ ...values, allowPartial: event.target.checked })}
              />
              <label htmlFor="res-partial">Reserve what is available if not all of it is</label>
            </span>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not reserve" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Reserve
            </Button>
            <Link className={buttonClassName("ghost")} to="/inventory/reservations">
              Back to reservations
            </Link>
          </div>
        </form>
      </Card>
      {result ? (
        <Card title="Reserved">
          <div className={styles.stack}>
            <Alert tone={Number(result.shortfall) > 0 ? "warning" : "success"} live="status">
              {Number(result.shortfall) > 0
                ? `Reserved part of the request; ${result.shortfall} could not be reserved.`
                : `Reserved in ${result.items.length} ${result.items.length === 1 ? "allocation" : "allocations"}.`}
            </Alert>
            <DataTable
              label="Allocations"
              columns={allocationColumns}
              rows={result.items}
              getRowKey={(row) => row.id}
              empty={{ title: "Nothing reserved" }}
            />
          </div>
        </Card>
      ) : null}
    </div>
  );
}
