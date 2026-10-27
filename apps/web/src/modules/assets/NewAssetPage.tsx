import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { createAsset, describeProblem, newKey, type NewAsset, type Problem } from "./api/client";
import { UUID } from "./format";
import styles from "./pages.module.css";

export interface AssetValues {
  legalEntityId: string;
  assetNumber: string;
  name: string;
  classCode: string;
  barcode: string;
  quantity: string;
}

type Errors = Partial<Record<keyof AssetValues, string>>;

const CODE = /^[A-Z0-9][A-Z0-9_-]{0,19}$/;
const QUANTITY = /^\d+(\.\d{1,4})?$/;

/** Checks that mirror the service (internal/api createAsset). Keys are the service's field names. */
export function validateAsset(values: AssetValues): { errors: Errors; input?: NewAsset } {
  const errors: Errors = {};
  const number = values.assetNumber.trim();
  const name = values.name.trim();
  const barcode = values.barcode.trim();
  const quantity = values.quantity.trim();
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (number.length < 1 || number.length > 40) errors.assetNumber = "Enter an asset number of up to 40 characters.";
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!CODE.test(values.classCode)) errors.classCode = "Use capital letters, digits, hyphens or underscores, such as MACHINE.";
  if (barcode.length > 64) errors.barcode = "Use at most 64 characters.";
  if (quantity && (!QUANTITY.test(quantity) || /^0+(\.0+)?$/.test(quantity))) errors.quantity = "Enter a quantity above 0 with at most 4 decimals.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      legalEntityId: values.legalEntityId.trim(),
      assetNumber: number,
      name,
      classCode: values.classCode,
      ...(barcode ? { barcode } : {}),
      ...(quantity ? { quantity } : {}),
    },
  };
}

/** New asset (FR-37.1). The asset starts as Not yet acquired until a book posts its acquisition. */
export function NewAssetPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<AssetValues>({ legalEntityId: "", assetNumber: "", name: "", classCode: "", barcode: "", quantity: "1" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<AssetValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateAsset(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createAsset(input, key.current);
      navigate(`/assets/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as Errors);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const field = (name: keyof AssetValues, label: string, hint?: string, extra: { mono?: boolean; optional?: boolean; upper?: boolean } = {}) => (
    <FormField id={`asset-${name}`} label={label} hint={hint} error={errors[name]} optional={extra.optional}>
      {(control) => (
        <Input
          {...control}
          className={extra.mono ? styles.mono : undefined}
          value={values[name]}
          onChange={(e) => update({ [name]: extra.upper ? e.target.value.toUpperCase() : e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New asset" description="Register the asset first, then add a depreciation book to acquire it." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New asset">
          {field("legalEntityId", "Legal entity", "UUID of the legal entity that owns the asset.", { mono: true })}
          {field("assetNumber", "Asset number", "Unique in the legal entity, for example FA-000123.", { mono: true })}
          {field("name", "Name")}
          {field("classCode", "Asset class", "For example MACHINE or VEHICLE.", { mono: true, upper: true })}
          {field("barcode", "Barcode", "Unique in the legal entity; printed on the asset tag.", { mono: true, optional: true })}
          {field("quantity", "Quantity", undefined, { optional: true })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "AST-0002" ? "That number or barcode is taken" : "Could not create the asset"} live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create asset
            </Button>
            <Link className={buttonClassName("ghost")} to="/assets">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
