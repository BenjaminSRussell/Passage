import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createGoodsInTransit, describeProblem, newKey, ORIGIN_INCOTERMS, type NewGoodsInTransit, type Problem } from "./api/client";
import { UUID } from "./format";
import styles from "./pages.module.css";

export interface GoodsInTransitValues {
  voyageId: string;
  incoterm: string;
  siteId: string;
  receiptLines: string;
}

/** Checks that mirror the service (BR-34.11): receipt lines are a comma-separated list, each once, empty for all. */
export function validateGoodsInTransit(v: GoodsInTransitValues): { errors: Record<string, string>; input?: NewGoodsInTransit } {
  const errors: Record<string, string> = {};
  const voyageId = v.voyageId.trim();
  if (!UUID.test(voyageId)) errors.voyageId = "Enter the voyage id as a lower-case UUID.";
  const siteId = v.siteId.trim();
  if (!UUID.test(siteId)) errors.siteId = "Choose a destination site.";
  if (!(ORIGIN_INCOTERMS as readonly string[]).includes(v.incoterm)) errors.incoterm = "Choose an origin Incoterm.";
  const parts = v.receiptLines
    .split(",")
    .map((p) => p.trim())
    .filter((p) => p !== "");
  const lines = parts.map(Number);
  if (parts.some((p) => !/^\d+$/.test(p)) || lines.some((n) => n < 1)) errors.receiptLines = "Enter receipt line numbers such as 1, 2, 5.";
  else if (new Set(lines).size !== lines.length) errors.receiptLines = "List each receipt line once.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { voyageId, incoterm: v.incoterm, siteId, ...(lines.length ? { receiptLines: lines } : {}) } };
}

/** Open a goods-in-transit order for an estimated voyage (FR-34.12). */
export function NewGoodsInTransitPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [values, setValues] = useState<GoodsInTransitValues>({ voyageId: params.get("voyageId") ?? "", incoterm: "FOB", siteId: "", receiptLines: "" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<GoodsInTransitValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateGoodsInTransit(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const order = await createGoodsInTransit(input, key.current);
      navigate(`/transport/goods-in-transit/${order.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "COM-0001" && Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New goods-in-transit order"
        description="The order takes the voyage's estimated landed cost for each receipt line. Ship it when the forwarder reports the departure; ownership then moves to the in-transit warehouse."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="git-voyage" label="Voyage id" hint="An estimated voyage (UUID)." error={errors.voyageId}>
            {(control) => <Input {...control} className={styles.mono} value={values.voyageId} onChange={(e) => update({ voyageId: e.target.value })} invalid={Boolean(errors.voyageId)} />}
          </FormField>
          <FormField id="git-incoterm" label="Incoterm" hint="DAP, DPU and DDP transfer at receipt and open no order." error={errors.incoterm}>
            {(control) => (
              <Select {...control} value={values.incoterm} onChange={(e) => update({ incoterm: e.target.value })}>
                {ORIGIN_INCOTERMS.map((i) => (
                  <option key={i} value={i}>
                    {i}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="git-site" label="Destination site" hint="Its in-transit warehouse holds the goods." error={errors.siteId}>
            {(control) => <EntityLookup entity="site" {...control} value={values.siteId} onChange={(e) => update({ siteId: e.target.value })} invalid={Boolean(errors.siteId)} />}
          </FormField>
          <FormField id="git-lines" label="Receipt lines" hint="Comma-separated, such as 1, 2. Leave empty for every line of the voyage." optional error={errors.receiptLines}>
            {(control) => <Input {...control} inputMode="numeric" value={values.receiptLines} onChange={(e) => update({ receiptLines: e.target.value })} invalid={Boolean(errors.receiptLines)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not open the order" live="alert">
                {failure.message} ({failure.code})
                {Object.values(failure.fields).length > 0 ? ` ${Object.values(failure.fields).join(" ")}` : null}
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Open order
            </Button>
            <Link className={buttonClassName("ghost")} to="/transport/goods-in-transit">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
