import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Textarea } from "../../shell/ui";
import { BLOCK_REASONS, BLOCK_TARGETS, createBlocking, describeProblem, type BlockReason, type BlockTarget, type Problem } from "./api/client";
import { DECIMAL, sentence, today, UUID } from "./format";
import { emptyDimensions, fieldErrorsOf, FORM_DIMENSIONS, type FormDimension } from "./parts";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export interface BlockingValues {
  legalEntityId: string;
  productId: string;
  qty: string;
  unit: string;
  targetStatus: BlockTarget;
  reason: BlockReason;
  note: string;
  physicalDate: string;
  expectedReleaseDate: string;
  dimensions: Record<FormDimension, string>;
}

const fields = ["legalEntityId", "productId", "qty", "unit", "targetStatus", "reason", "note", "physicalDate", "expectedReleaseDate", "dimensions"] as const;
type Errors = Partial<Record<(typeof fields)[number], string>>;

const targetHints: Record<BlockTarget, string> = {
  BLOCKED: "Held out of availability until released.",
  QUARANTINE: "Held for inspection; leaving quarantine needs a disposition.",
};

/** Client checks that mirror the service. */
export function validateBlocking(values: BlockingValues): Errors {
  const errors: Errors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!UUID.test(values.productId.trim())) errors.productId = "Enter the product id (a UUID).";
  const qty = values.qty.trim();
  if (!DECIMAL.test(qty) || Number(qty) <= 0) {
    errors.qty = "Enter a quantity above zero.";
  } else if (values.dimensions.serial.trim() !== "" && Number(qty) !== 1) {
    errors.qty = "A serial number is blocked one unit at a time.";
  }
  if (!/^[A-Z0-9_-]{1,12}$/.test(values.unit.trim())) errors.unit = "Enter a unit code such as EA.";
  if (values.dimensions.site.trim() === "") errors.dimensions = "Site is required.";
  if (values.note.length > 500) errors.note = "Keep the note to 500 characters.";
  const expected = values.expectedReleaseDate.trim();
  if (expected !== "" && values.physicalDate !== "" && expected < values.physicalDate) {
    errors.expectedReleaseDate = "The expected release cannot be before the physical date.";
  }
  return errors;
}

/** Blocking screen (PRD 3.16.6): block or quarantine a quantity, batch or serial with a reason. */
export function NewBlockingPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<BlockingValues>({
    legalEntityId: "",
    productId: "",
    qty: "",
    unit: "EA",
    targetStatus: "BLOCKED",
    reason: "QUALITY",
    note: "",
    physicalDate: today(),
    expectedReleaseDate: "",
    dimensions: emptyDimensions(),
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(crypto.randomUUID());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateBlocking(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await createBlocking(
        {
          legalEntityId: values.legalEntityId.trim(),
          productId: values.productId.trim(),
          qty: values.qty.trim(),
          unit: values.unit.trim(),
          targetStatus: values.targetStatus,
          reason: values.reason,
          note: values.note.trim(),
          physicalDate: values.physicalDate,
          expectedReleaseDate: values.expectedReleaseDate.trim(),
          dimensions: values.dimensions,
        },
        key.current,
      );
      navigate("/inventory/blockings");
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

  const change = (next: BlockingValues) => {
    setValues(next);
    key.current = crypto.randomUUID();
  };
  const set = (name: Exclude<keyof BlockingValues, "dimensions">) => (event: { target: { value: string } }) =>
    change({ ...values, [name]: name === "unit" ? event.target.value.toUpperCase() : event.target.value });
  const setDim = (name: FormDimension) => (event: { target: { value: string } }) =>
    change({ ...values, dimensions: { ...values.dimensions, [name]: event.target.value } });

  return (
    <div className={styles.stack}>
      <PageHeader title="Block stock" description="Move available stock to blocked or quarantined. Physical stock and value stay; availability drops." />
      <Card>
        <form onSubmit={submit} noValidate className={styles.form}>
          <FormField id="blk-legal-entity" label="Legal entity" error={errors.legalEntityId}>
            {(control) => <Input {...control} value={values.legalEntityId} onChange={set("legalEntityId")} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="blk-product" label="Product" error={errors.productId}>
            {(control) => <Input {...control} value={values.productId} onChange={set("productId")} invalid={Boolean(errors.productId)} />}
          </FormField>
          <FormField id="blk-qty" label="Quantity" error={errors.qty}>
            {(control) => <Input {...control} inputMode="decimal" value={values.qty} onChange={set("qty")} invalid={Boolean(errors.qty)} />}
          </FormField>
          <FormField id="blk-unit" label="Unit" error={errors.unit}>
            {(control) => <Input {...control} value={values.unit} maxLength={12} onChange={set("unit")} invalid={Boolean(errors.unit)} />}
          </FormField>
          <FormField id="blk-target" label="Block as" hint={targetHints[values.targetStatus]}>
            {(control) => (
              <Select {...control} value={values.targetStatus} onChange={set("targetStatus")}>
                {BLOCK_TARGETS.map((t) => (
                  <option key={t} value={t}>
                    {sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="blk-reason" label="Reason">
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
          <FormField id="blk-date" label="Physical date">
            {(control) => <Input {...control} type="date" value={values.physicalDate} onChange={set("physicalDate")} />}
          </FormField>
          <FormField id="blk-expected" label="Expected release" optional error={errors.expectedReleaseDate} hint="When the inspection or recall hold should end. Information only.">
            {(control) => (
              <Input {...control} type="date" value={values.expectedReleaseDate} onChange={set("expectedReleaseDate")} invalid={Boolean(errors.expectedReleaseDate)} />
            )}
          </FormField>
          {FORM_DIMENSIONS.map((name) => (
            <FormField key={name} id={`blk-${name}`} label={sentence(name)} optional={name !== "site"} error={name === "site" ? errors.dimensions : undefined}>
              {(control) => (
                <Input {...control} value={values.dimensions[name]} maxLength={60} onChange={setDim(name)} invalid={name === "site" && Boolean(errors.dimensions)} />
              )}
            </FormField>
          ))}
          <div className={styles.formActions}>
            <FormField id="blk-note" label="Note" optional error={errors.note}>
              {(control) => <Textarea {...control} value={values.note} maxLength={500} onChange={set("note")} />}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not block the stock" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Block stock
            </Button>
            <Link className={buttonClassName("ghost")} to="/inventory/blockings">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
