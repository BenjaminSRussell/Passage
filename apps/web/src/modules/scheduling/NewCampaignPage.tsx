import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createCampaign, describeProblem, newKey, SEQUENCE_RULES, type NewCampaign, type Problem, type SequenceRule } from "./api/client";
import { fromUtcInput, sentence } from "./format";
import { CODE, decimalText, mapFields, optionalWhole, positive } from "./forms";
import styles from "./pages.module.css";
import { ResourceGate } from "./ResourceGate";

export interface CampaignValues {
  resourceId: string;
  campaignKey: string;
  plannedStart: string;
  sequenceRule: SequenceRule;
  currentSetupGroup: string;
  maxOrders: string;
  maxQty: string;
  maxMinutes: string;
}

type Errors = Partial<Record<keyof CampaignValues, string>>;

/** Checks that mirror the service, and the request the form sends. */
export function validateCampaign(values: CampaignValues): { errors: Errors; input?: NewCampaign } {
  const errors: Errors = {};
  if (values.resourceId === "") errors.resourceId = "Choose the campaign resource.";
  const key = values.campaignKey.trim();
  if (key.length < 1 || key.length > 64) errors.campaignKey = "Enter a key of up to 64 characters, such as a formula family.";
  const plannedStart = fromUtcInput(values.plannedStart);
  if (plannedStart === null) errors.plannedStart = "Enter a date and time.";
  const current = values.currentSetupGroup.trim();
  if (current !== "" && !CODE.test(current)) errors.currentSetupGroup = "Use 1 to 32 capital letters, digits, _ or -.";
  const maxOrders = optionalWhole(values.maxOrders, 1, 1000);
  if (maxOrders === null) errors.maxOrders = "Enter 1 to 1000.";
  const maxMinutes = optionalWhole(values.maxMinutes, 1, 1_000_000);
  if (maxMinutes === null) errors.maxMinutes = "Enter 1 to 1,000,000 minutes.";
  let maxQty: string | undefined;
  if (values.maxQty.trim() !== "") {
    const q = decimalText(values.maxQty, 6);
    if (q === null || !positive(q)) errors.maxQty = "Enter a quantity above zero with up to 6 decimals.";
    else maxQty = q;
  }
  if (Object.keys(errors).length > 0 || plannedStart === null) return { errors };
  const input: NewCampaign = { resourceId: values.resourceId, campaignKey: key, plannedStart, sequenceRule: values.sequenceRule, orders: [] };
  if (current !== "") input.currentSetupGroup = current;
  if (maxOrders !== undefined && maxOrders !== null) input.maxOrders = maxOrders;
  if (maxMinutes !== undefined && maxMinutes !== null) input.maxMinutes = maxMinutes;
  if (maxQty !== undefined) input.maxQty = maxQty;
  return { errors, input };
}

const fieldNames: Record<string, keyof CampaignValues> = {
  resourceId: "resourceId",
  campaignKey: "campaignKey",
  plannedStart: "plannedStart",
  sequenceRule: "sequenceRule",
  currentSetupGroup: "currentSetupGroup",
  maxOrders: "maxOrders",
  maxQty: "maxQty",
  maxMinutes: "maxMinutes",
};

const ruleHints: Record<SequenceRule, string> = {
  "nearest-neighbor": "Orders follow the smallest changeover from the previous setup group (BR-23.12).",
  manual: "Orders run in the order you add them; changeovers are priced as they fall.",
};

/** Create a proposed campaign; orders are added on the campaign page. */
export function NewCampaignPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<CampaignValues>({
    resourceId: "",
    campaignKey: "",
    plannedStart: "",
    sequenceRule: "nearest-neighbor",
    currentSetupGroup: "",
    maxOrders: "",
    maxQty: "",
    maxMinutes: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<CampaignValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateCampaign(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const campaign = await createCampaign(input, key.current);
      navigate(`/scheduling/campaigns/${campaign.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors = mapFields(problem.fields, fieldNames);
      if (problem.code === "SCHED-0107") fieldErrors.resourceId = "This resource no longer exists.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (name: "campaignKey" | "currentSetupGroup" | "maxOrders" | "maxQty" | "maxMinutes", props: { inputMode?: "numeric" | "decimal"; upper?: boolean } = {}) =>
    function Control(control: { id: string }) {
      return (
        <Input
          {...control}
          inputMode={props.inputMode}
          value={values[name]}
          onChange={(event) => update({ [name]: props.upper ? event.target.value.toUpperCase() : event.target.value })}
          invalid={Boolean(errors[name])}
        />
      );
    };

  return (
    <div className={styles.stack}>
      <PageHeader title="New campaign" description="A campaign runs its orders back to back on one resource with a leading setup and a cleanout at the end (FR-23.14)." />
      <Card>
        <ResourceGate need="A campaign runs on one resource.">
          {(resources) => (
            <form className={styles.form} onSubmit={submit} noValidate>
              <FormField id="campaign-resource" label="Resource" error={errors.resourceId}>
                {(control) => (
                  <Select {...control} value={values.resourceId} onChange={(event) => update({ resourceId: event.target.value })} invalid={Boolean(errors.resourceId)}>
                    <option value="">Choose a resource</option>
                    {resources.map((r) => (
                      <option key={r.id} value={r.id}>
                        {`${r.code} · ${r.name}`}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="campaign-key" label="Campaign key" hint="Formula family or setup group the orders share." error={errors.campaignKey}>
                {text("campaignKey")}
              </FormField>
              <FormField id="campaign-start" label="Planned start (UTC)" error={errors.plannedStart}>
                {(control) => (
                  <Input {...control} type="datetime-local" value={values.plannedStart} onChange={(event) => update({ plannedStart: event.target.value })} invalid={Boolean(errors.plannedStart)} />
                )}
              </FormField>
              <FormField id="campaign-rule" label="Sequence rule" hint={ruleHints[values.sequenceRule]} error={errors.sequenceRule}>
                {(control) => (
                  <Select {...control} value={values.sequenceRule} onChange={(event) => update({ sequenceRule: event.target.value as SequenceRule })}>
                    {SEQUENCE_RULES.map((rule) => (
                      <option key={rule} value={rule}>
                        {sentence(rule)}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="campaign-current" label="Current setup group" optional hint="The group the resource is set up for now; the leading setup starts from it." error={errors.currentSetupGroup}>
                {text("currentSetupGroup", { upper: true })}
              </FormField>
              <FormField id="campaign-max-orders" label="Maximum orders" optional error={errors.maxOrders}>
                {text("maxOrders", { inputMode: "numeric" })}
              </FormField>
              <FormField id="campaign-max-qty" label="Maximum quantity" optional error={errors.maxQty}>
                {text("maxQty", { inputMode: "decimal" })}
              </FormField>
              <FormField id="campaign-max-minutes" label="Maximum minutes" optional hint="Run, changeover and cleanout minutes together." error={errors.maxMinutes}>
                {text("maxMinutes", { inputMode: "numeric" })}
              </FormField>
              {failure ? (
                <div className={styles.formActions}>
                  <Alert tone="danger" title="Could not create the campaign" live="alert">
                    {failure.message} ({failure.code})
                  </Alert>
                </div>
              ) : null}
              <div className={styles.formActions}>
                <Button type="submit" variant="primary" loading={busy}>
                  Create campaign
                </Button>
                <Link className={buttonClassName("ghost")} to="/scheduling/campaigns">
                  Cancel
                </Link>
              </div>
            </form>
          )}
        </ResourceGate>
      </Card>
    </div>
  );
}
