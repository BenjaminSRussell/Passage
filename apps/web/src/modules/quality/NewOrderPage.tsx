import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createOrder, describeProblem, listTestGroups, newKey, type CertRequirement, type NewOrder, type Problem, type Trigger } from "./api/client";
import { CERT_LABELS, CERT_REQUIREMENTS, DECIMAL, pickFields, TRIGGER_LABELS, UUID } from "./format";
import styles from "./pages.module.css";
import { emptyPlan, planFieldErrors, PlanFields, validatePlan, type PlanErrors, type PlanValues } from "./PlanFields";
import { useLoad } from "./useLoad";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import type { LookupEntityKey } from "../../shell/lookup/entities";
import { OrderSelect } from "../mfg-shared/orders";

interface Values {
  trigger: Trigger;
  sourceType: string;
  sourceId: string;
  itemId: string;
  itemGroupId: string;
  partyId: string;
  siteId: string;
  warehouseId: string;
  batch: string;
  lotQty: string;
  uom: string;
  testGroupId: string;
  certificateRequired: CertRequirement;
}

type Field = Exclude<keyof Values, "certificateRequired">;
type Errors = Partial<Record<Field | "sourceRef", string>>;

const FIELDS = ["trigger", "itemId", "itemGroupId", "partyId", "siteId", "warehouseId", "lotQty", "uom", "testGroupId", "sourceRef"] as const;

/** The longest batch the service takes, the length receipt checks match on (BR-33.16). */
const MAX_BATCH = 60;

/** Checks that mirror the service, and the request the form sends. */
export function validateOrder(values: Values, plan: PlanValues): { errors: Errors; planErrors: PlanErrors; input?: NewOrder } {
  const errors: Errors = {};
  const type = values.sourceType.trim();
  // A manual order has no source document: the service still wants a reference, so the form makes one.
  const manualOrder = values.trigger === "MANUAL";
  const sourceId = manualOrder && values.sourceId.trim() === "" ? crypto.randomUUID() : values.sourceId.trim();
  if (type === "" || type.length > 40) errors.sourceType = "Enter the source document type, up to 40 characters, such as receipt_line.";
  if (!UUID.test(sourceId)) errors.sourceId = "Choose the source document.";
  const choose = { itemId: "Choose an item.", siteId: "Choose a site.", warehouseId: "Choose a warehouse." };
  for (const name of ["itemId", "siteId", "warehouseId"] as const) {
    if (!UUID.test(values[name].trim())) errors[name] = choose[name];
  }
  for (const name of ["itemGroupId", "partyId"] as const) {
    if (values[name].trim() !== "" && !UUID.test(values[name].trim())) errors[name] = name === "partyId" ? "Choose a vendor or customer, or leave it empty." : "Choose an item group, or leave it empty.";
  }
  const batch = values.batch.trim();
  if (batch.length > MAX_BATCH) errors.batch = `Keep the batch to ${MAX_BATCH} characters.`;
  else if (values.trigger === "SOURCE_INSPECTION" && batch === "") errors.batch = "A source inspection names the batch it inspects at the vendor.";
  const lot = values.lotQty.trim();
  if (!DECIMAL.test(lot) || Number(lot) <= 0) errors.lotQty = "Enter the lot quantity, above 0.";
  const uom = values.uom.trim();
  if (uom === "" || uom.length > 10) errors.uom = "Enter the unit, up to 10 characters.";
  const manual = values.trigger === "MANUAL";
  if (manual && !UUID.test(values.testGroupId)) errors.testGroupId = "Choose the test group to inspect against.";
  const checked = manual ? validatePlan(plan) : { errors: {} };
  if (Object.keys(errors).length > 0 || Object.keys(checked.errors).length > 0) return { errors, planErrors: checked.errors };
  const input: NewOrder = {
    trigger: values.trigger,
    sourceRef: { type, id: sourceId },
    itemId: values.itemId.trim(),
    siteId: values.siteId.trim(),
    warehouseId: values.warehouseId.trim(),
    lotQty: lot,
    uom,
  };
  if (values.itemGroupId.trim()) input.itemGroupId = values.itemGroupId.trim();
  if (values.partyId.trim()) input.partyId = values.partyId.trim();
  if (batch) input.dims = { batch };
  if (manual && "plan" in checked) {
    input.testGroupId = values.testGroupId;
    input.samplingPlan = checked.plan;
    if (values.certificateRequired !== "NONE") input.certificateRequired = values.certificateRequired;
  }
  return { errors, planErrors: {}, input };
}

const initial: Values = {
  trigger: "MANUAL",
  sourceType: "manual",
  sourceId: "",
  itemId: "",
  itemGroupId: "",
  partyId: "",
  siteId: "",
  warehouseId: "",
  batch: "",
  lotQty: "",
  uom: "EA",
  testGroupId: "",
  certificateRequired: "NONE",
};

/**
 * The form's starting values: a link may prefill the trigger, source, batch and lot
 * quantity, as the receiving page does for the excess a source inspection does not cover.
 */
export function prefill(params: URLSearchParams): Values {
  const values = { ...initial };
  const trigger = params.get("trigger");
  if (trigger && trigger in TRIGGER_LABELS) values.trigger = trigger as Trigger;
  for (const name of ["sourceType", "sourceId", "batch", "lotQty", "uom"] as const) {
    const value = params.get(name);
    if (value) values[name] = value;
  }
  return values;
}

/**
 * Create a quality order. A manual order names its test group and sampling plan; any
 * other trigger takes them from the best matching quality association (BR-33.3).
 */
export function NewOrderPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const groups = useLoad((signal) => listTestGroups(signal), "test-groups");
  const [values, setValues] = useState<Values>(() => prefill(params));
  const [plan, setPlan] = useState<PlanValues>(emptyPlan);
  const [errors, setErrors] = useState<Errors>({});
  const [planErrors, setPlanErrors] = useState<PlanErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const checked = validateOrder(values, plan);
    setErrors(checked.errors);
    setPlanErrors(checked.planErrors);
    setFailure(null);
    if (!checked.input) return;
    setBusy(true);
    try {
      const order = await createOrder(checked.input, key.current);
      navigate(`/quality/orders/${order.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = pickFields(problem.fields, FIELDS);
      if (problem.fields.dims) fieldErrors.batch = problem.fields.dims;
      const fromPlan = planFieldErrors(problem.fields);
      if (Object.keys(fieldErrors).length > 0 || Object.keys(fromPlan).length > 0) {
        setErrors({ ...fieldErrors, sourceId: fieldErrors.sourceRef });
        setPlanErrors(fromPlan);
      } else {
        setFailure(problem);
      }
    } finally {
      setBusy(false);
    }
  }

  const text = (id: Field, label: string, hint?: string, optional?: boolean) => (
    <FormField id={`order-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => <Input {...control} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value } as Partial<Values>)} />}
    </FormField>
  );
  const lookup = (id: Field, label: string, entity: LookupEntityKey, hint?: string, optional?: boolean, scope?: { siteId?: string }) => (
    <FormField id={`order-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => <EntityLookup {...control} entity={entity} scope={scope} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value } as Partial<Values>)} />}
    </FormField>
  );
  const manual = values.trigger === "MANUAL";

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New quality order"
        description="The inspected quantity stays on quality hold until the usage decision. Sample size follows the sampling plan; Z1.4 samples larger than the lot inspect the whole lot."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New quality order">
          <FormField id="order-trigger" label="Trigger" hint="Anything but manual uses the best matching quality association.">
            {(control) => (
              <Select {...control} value={values.trigger} onChange={(event) => update({ trigger: event.target.value as Trigger })}>
                {(Object.keys(TRIGGER_LABELS) as Trigger[]).map((t) => (
                  <option key={t} value={t}>
                    {TRIGGER_LABELS[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {manual ? null : text("sourceType", "Source type", "The document line that triggered the order, such as receipt_line.")}
          {manual ? null : values.sourceType === "production-order" ? (
            <FormField id="order-sourceId" label="Source document" hint="The production order that triggered the inspection." error={errors.sourceId}>
              {(control) => <OrderSelect {...control} value={values.sourceId} onChange={(v) => update({ sourceId: v })} />}
            </FormField>
          ) : (
            text("sourceId", "Source document", "The reference of the document line that triggered the order.")
          )}
          {lookup("itemId", "Item", "product")}
          {lookup("itemGroupId", "Item group", "itemGroup", "Matches item-group associations.", true)}
          {lookup("partyId", "Vendor or customer", "party", "Matches party associations.", true)}
          {lookup("siteId", "Site", "site")}
          {lookup("warehouseId", "Warehouse", "warehouse", undefined, false, { siteId: values.siteId || undefined })}
          {text(
            "batch",
            "Batch",
            values.trigger === "SOURCE_INSPECTION"
              ? "The batch inspected at the vendor; with the PO line as source id, receipts of this batch skip receiving inspection once it is accepted."
              : "Printed on the certificate of analysis and carried to any nonconformance.",
            values.trigger !== "SOURCE_INSPECTION",
          )}
          {text("lotQty", "Lot quantity")}
          {text("uom", "Unit")}
          {manual ? (
            <>
              <FormField id="order-testGroupId" label="Test group" error={errors.testGroupId ?? (groups.error ? `Test groups could not be loaded: ${groups.error.message}` : undefined)}>
                {(control) => (
                  <Select {...control} value={values.testGroupId} invalid={Boolean(errors.testGroupId)} onChange={(event) => update({ testGroupId: event.target.value })}>
                    <option value="">{groups.loading ? "Loading test groups…" : "Choose a test group"}</option>
                    {(groups.data ?? []).map((g) => (
                      <option key={g.id} value={g.id}>
                        {`${g.code} v${g.version} (${g.tests.length} ${g.tests.length === 1 ? "test" : "tests"})`}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="order-certificateRequired" label="Vendor certificate" hint="Acceptance waits for it to be received in specification or waived.">
                {(control) => (
                  <Select {...control} value={values.certificateRequired} onChange={(event) => update({ certificateRequired: event.target.value as CertRequirement })}>
                    {CERT_REQUIREMENTS.map((c) => (
                      <option key={c} value={c}>
                        {CERT_LABELS[c]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <PlanFields idPrefix="order" values={plan} errors={planErrors} onChange={(patch) => setPlan({ ...plan, ...patch })} />
            </>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the quality order" live="alert">
                {failure.code === "QM-0100" ? "No quality association matches this trigger, item, party and site. Add an association or create a manual order." : failure.message}{" "}
                ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create quality order
            </Button>
            <Link className={buttonClassName("ghost")} to="/quality/orders">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
