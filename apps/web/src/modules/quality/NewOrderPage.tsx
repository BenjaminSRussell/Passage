import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createOrder, describeProblem, listTestGroups, newKey, type NewOrder, type Problem, type Trigger } from "./api/client";
import { DECIMAL, pickFields, TRIGGER_LABELS, UUID } from "./format";
import styles from "./pages.module.css";
import { emptyPlan, planFieldErrors, PlanFields, validatePlan, type PlanErrors, type PlanValues } from "./PlanFields";
import { useLoad } from "./useLoad";

interface Values {
  trigger: Trigger;
  sourceType: string;
  sourceId: string;
  itemId: string;
  itemGroupId: string;
  partyId: string;
  siteId: string;
  warehouseId: string;
  lotQty: string;
  uom: string;
  testGroupId: string;
}

type Field = keyof Values;
type Errors = Partial<Record<Field | "sourceRef", string>>;

const FIELDS = ["trigger", "itemId", "itemGroupId", "partyId", "siteId", "warehouseId", "lotQty", "uom", "testGroupId", "sourceRef"] as const;

/** Checks that mirror the service, and the request the form sends. */
export function validateOrder(values: Values, plan: PlanValues): { errors: Errors; planErrors: PlanErrors; input?: NewOrder } {
  const errors: Errors = {};
  const type = values.sourceType.trim();
  if (type === "" || type.length > 40) errors.sourceType = "Enter the source document type, up to 40 characters, such as receipt_line.";
  if (!UUID.test(values.sourceId.trim())) errors.sourceId = "Enter the source document id (a UUID).";
  for (const name of ["itemId", "siteId", "warehouseId"] as const) {
    if (!UUID.test(values[name].trim())) errors[name] = "Enter the id (a UUID).";
  }
  for (const name of ["itemGroupId", "partyId"] as const) {
    if (values[name].trim() !== "" && !UUID.test(values[name].trim())) errors[name] = "Enter a UUID, or leave it empty.";
  }
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
    sourceRef: { type, id: values.sourceId.trim() },
    itemId: values.itemId.trim(),
    siteId: values.siteId.trim(),
    warehouseId: values.warehouseId.trim(),
    lotQty: lot,
    uom,
  };
  if (values.itemGroupId.trim()) input.itemGroupId = values.itemGroupId.trim();
  if (values.partyId.trim()) input.partyId = values.partyId.trim();
  if (manual && "plan" in checked) {
    input.testGroupId = values.testGroupId;
    input.samplingPlan = checked.plan;
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
  lotQty: "",
  uom: "EA",
  testGroupId: "",
};

/**
 * Create a quality order. A manual order names its test group and sampling plan; any
 * other trigger takes them from the best matching quality association (BR-33.3).
 */
export function NewOrderPage() {
  const navigate = useNavigate();
  const groups = useLoad((signal) => listTestGroups(signal), "test-groups");
  const [values, setValues] = useState<Values>(initial);
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
      const fieldErrors = pickFields(problem.fields, FIELDS);
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
          {text("sourceType", "Source type", "The document line that triggered the order, such as receipt_line.")}
          {text("sourceId", "Source id")}
          {text("itemId", "Item id")}
          {text("itemGroupId", "Item group id", "Matches item-group associations.", true)}
          {text("partyId", "Vendor or customer id", "Matches party associations.", true)}
          {text("siteId", "Site id")}
          {text("warehouseId", "Warehouse id")}
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
