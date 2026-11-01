import { useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Textarea, type DataTableColumn } from "../../shell/ui";
import { BLOCK_REASONS, describeProblem, placeHold, type Hold, type HoldResult, type Problem } from "./api/client";
import { DECIMAL, describeDimensions, sentence, UUID } from "./format";
import { ALL_DIMENSIONS, emptyDimensions, fieldErrorsOf, FORM_DIMENSIONS, type FormDimension } from "./parts";
import styles from "./pages.module.css";

export interface HoldValues {
  legalEntityId: string;
  productId: string;
  wholeLot: boolean;
  qty: string;
  allowPartial: boolean;
  reason: string;
  sourceType: string;
  sourceRef: string;
  note: string;
  dimensions: Record<FormDimension, string>;
}

const fields = ["legalEntityId", "productId", "qty", "reason", "sourceType", "sourceRef", "note", "dimensions"] as const;
type Errors = Partial<Record<(typeof fields)[number], string>>;

/** Client checks that mirror the service. */
export function validateHold(values: HoldValues): Errors {
  const errors: Errors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!UUID.test(values.productId.trim())) errors.productId = "Enter the product id (a UUID).";
  const qty = values.qty.trim();
  if (!values.wholeLot && (!DECIMAL.test(qty) || Number(qty) <= 0)) errors.qty = "Enter a quantity above zero.";
  if (!/^[A-Z][A-Z0-9_]{1,31}$/.test(values.sourceType.trim())) errors.sourceType = "Enter 2 to 32 characters starting with a capital letter: capital letters, digits or underscore.";
  const ref = values.sourceRef.trim();
  if (ref === "" || ref.length > 60) errors.sourceRef = "Enter the document that asked for the hold (up to 60 characters).";
  if (values.note.length > 500) errors.note = "Keep the note to 500 characters.";
  if (values.wholeLot && values.dimensions.batch.trim() === "") errors.dimensions = "A whole-lot hold names the batch.";
  else if (!values.wholeLot && values.dimensions.site.trim() === "") errors.dimensions = "A quantity hold names the site and every dimension of the stock.";
  return errors;
}

const heldColumns: DataTableColumn<Hold>[] = [
  { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, ALL_DIMENSIONS) },
  { key: "heldQty", header: "Held", align: "end", mono: true },
];

const blank = (): HoldValues => ({
  legalEntityId: "",
  productId: "",
  wholeLot: false,
  qty: "",
  allowPartial: false,
  reason: "QUALITY",
  sourceType: "QUALITY_ORDER",
  sourceRef: "",
  note: "",
  dimensions: emptyDimensions(),
});

/** Place a hold on a quantity of one dimension set, or on a whole lot (every unrestricted row of a batch). */
export function NewHoldPage() {
  const [values, setValues] = useState<HoldValues>(blank);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<HoldResult | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per filled-in form: a double submit replays instead of holding twice.
  const key = useRef(crypto.randomUUID());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateHold(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const out = await placeHold(
        {
          legalEntityId: values.legalEntityId.trim(),
          productId: values.productId.trim(),
          dimensions: values.dimensions,
          qty: values.qty.trim(),
          wholeLot: values.wholeLot,
          allowPartial: values.allowPartial,
          reason: values.reason,
          sourceType: values.sourceType.trim(),
          sourceRef: values.sourceRef.trim(),
          note: values.note.trim(),
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

  const change = (next: HoldValues) => {
    setValues(next);
    key.current = crypto.randomUUID();
  };
  const set = (name: Exclude<keyof HoldValues, "dimensions" | "wholeLot" | "allowPartial">) => (event: { target: { value: string } }) =>
    change({ ...values, [name]: name === "sourceType" ? event.target.value.toUpperCase() : event.target.value });
  const setDim = (name: FormDimension) => (event: { target: { value: string } }) =>
    change({ ...values, dimensions: { ...values.dimensions, [name]: event.target.value } });

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Place hold"
        description="Keep available stock out of availability while it is checked. Physical stock and value stay where they are."
      />
      <Card>
        <form onSubmit={submit} noValidate className={styles.form}>
          <FormField id="hold-legal-entity" label="Legal entity" error={errors.legalEntityId}>
            {(control) => <Input {...control} value={values.legalEntityId} onChange={set("legalEntityId")} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="hold-product" label="Product" error={errors.productId}>
            {(control) => <Input {...control} value={values.productId} onChange={set("productId")} invalid={Boolean(errors.productId)} />}
          </FormField>
          {values.wholeLot ? null : (
            <FormField id="hold-qty" label="Quantity" error={errors.qty}>
              {(control) => <Input {...control} inputMode="decimal" value={values.qty} onChange={set("qty")} invalid={Boolean(errors.qty)} />}
            </FormField>
          )}
          <FormField id="hold-reason" label="Reason">
            {(control) => (
              <Select {...control} value={values.reason} onChange={set("reason")}>
                {BLOCK_REASONS.map((r) => (
                  <option key={r} value={r}>
                    {sentence(r)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="hold-source-type" label="Source type" hint="The kind of document that asked for the hold, such as QUALITY_ORDER." error={errors.sourceType}>
            {(control) => <Input {...control} value={values.sourceType} maxLength={32} onChange={set("sourceType")} invalid={Boolean(errors.sourceType)} />}
          </FormField>
          <FormField id="hold-source-ref" label="Source reference" error={errors.sourceRef}>
            {(control) => <Input {...control} value={values.sourceRef} maxLength={60} onChange={set("sourceRef")} invalid={Boolean(errors.sourceRef)} />}
          </FormField>
          {FORM_DIMENSIONS.map((name) => {
            const required = values.wholeLot ? name === "batch" : name === "site";
            return (
              <FormField key={name} id={`hold-${name}`} label={sentence(name)} optional={!required} error={required ? errors.dimensions : undefined}>
                {(control) => <Input {...control} value={values.dimensions[name]} maxLength={60} onChange={setDim(name)} invalid={required && Boolean(errors.dimensions)} />}
              </FormField>
            );
          })}
          <div className={styles.formActions}>
            <FormField id="hold-note" label="Note" optional error={errors.note}>
              {(control) => <Textarea {...control} value={values.note} maxLength={500} onChange={set("note")} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input type="checkbox" id="hold-whole-lot" checked={values.wholeLot} onChange={(event) => change({ ...values, wholeLot: event.target.checked })} />
              <label htmlFor="hold-whole-lot">Hold the whole lot (everything available in the batch)</label>
            </span>
            {values.wholeLot ? null : (
              <span className={styles.check}>
                <Input type="checkbox" id="hold-partial" checked={values.allowPartial} onChange={(event) => change({ ...values, allowPartial: event.target.checked })} />
                <label htmlFor="hold-partial">Hold what is available if not all of it is</label>
              </span>
            )}
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not place the hold" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Place hold
            </Button>
            <Link className={buttonClassName("ghost")} to="/inventory/holds">
              Back to holds
            </Link>
          </div>
        </form>
      </Card>
      {result ? (
        <Card title="Held">
          <div className={styles.stack}>
            <Alert tone={Number(result.shortfall) > 0 ? "warning" : "success"} live="status">
              {Number(result.shortfall) > 0
                ? `Held part of the request; ${result.shortfall} was not available to hold.`
                : `Held in ${result.items.length} ${result.items.length === 1 ? "dimension set" : "dimension sets"}.`}
            </Alert>
            <DataTable label="Held stock" columns={heldColumns} rows={result.items} getRowKey={(row) => row.id} empty={{ title: "Nothing held" }} />
          </div>
        </Card>
      ) : null}
    </div>
  );
}
