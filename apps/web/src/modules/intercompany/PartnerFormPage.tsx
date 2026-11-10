import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, Input, PageHeader } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import {
  createPartner,
  newIdempotencyKey,
  type CreatePartnerInput,
  type PartnerActions,
  type PartnerPolicyInput,
  type PricingPolicy,
} from "./api/partners";
import { Fields, type FieldSpec } from "./Fields";
import { isCount, isDecimal } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export const pricingLabels: Record<PricingPolicy, string> = {
  PriceList: "Agreed price list",
  CostPlus: "Cost plus markup",
  TransferPriceTable: "Transfer price table",
};

export const actionLabels: Record<keyof PartnerActions, string> = {
  autoCreate: "Create the mirror document automatically",
  autoConfirm: "Confirm the mirror document",
  autoPostPackingSlip: "Post the packing slip",
  autoPostInvoice: "Post the invoice",
};

type PolicyKey = "customerAccount" | "vendorAccount" | "pricing" | "markupPct" | "pricePct" | "dateDays";

/** The editable policy of a pairing, as form text plus the action flags. */
export interface PolicyValues {
  text: Record<PolicyKey, string>;
  actions: PartnerActions;
  active: boolean;
}

export const defaultPolicy: PolicyValues = {
  text: { customerAccount: "", vendorAccount: "", pricing: "PriceList", markupPct: "0", pricePct: "0.5", dateDays: "2" },
  // ic.partnerPolicy default: auto-create on, auto-post off.
  actions: { autoCreate: true, autoConfirm: false, autoPostPackingSlip: false, autoPostInvoice: false },
  active: true,
};

export const policyFields: FieldSpec<PolicyKey>[] = [
  { name: "customerAccount", label: "Customer account", mono: true, hint: "The IC customer in the seller entity that stands for the buyer." },
  { name: "vendorAccount", label: "Vendor account", mono: true, hint: "The IC vendor in the buyer entity that stands for the seller." },
  {
    name: "pricing",
    label: "Pricing policy",
    options: (Object.keys(pricingLabels) as PricingPolicy[]).map((value) => ({ value, label: pricingLabels[value] })),
  },
  { name: "markupPct", label: "Markup %", decimal: true, hint: "Used by cost plus pricing, 0 to 1000." },
  { name: "pricePct", label: "Price tolerance %", path: "tolerance.pricePct", decimal: true, hint: "A price change up to this share updates the mirror silently." },
  { name: "dateDays", label: "Date tolerance (days)", path: "tolerance.dateDays", hint: "0 to 30. Any quantity change always needs re-approval." },
];

/** True when a non-negative decimal string is at most `max` (a whole number), compared without floats. */
export function atMost(value: string, max: number): boolean {
  const [whole, fraction = ""] = value.trim().split(".");
  const w = whole.replace(/^0+(?=\d)/, "");
  const m = String(max);
  if (w.length !== m.length) return w.length < m.length;
  if (w !== m) return w < m;
  return /^0*$/.test(fraction);
}

const code = (v: string) => v.trim();

/** Checks that mirror the service, and the policy part of the request. */
export function validatePolicy(values: PolicyValues): { errors: Record<string, string>; policy?: PartnerPolicyInput } {
  const errors: Record<string, string> = {};
  const t = values.text;
  for (const [name, noun] of [
    ["customerAccount", "customer account"],
    ["vendorAccount", "vendor account"],
  ] as const) {
    if (code(t[name]) === "") errors[name] = `Enter the ${noun}.`;
    else if (code(t[name]).length > 64) errors[name] = `The ${noun} can have at most 64 characters.`;
  }
  if (!isDecimal(t.markupPct) || !atMost(t.markupPct, 1000)) errors.markupPct = "Enter a markup from 0 to 1000, such as 10.";
  if (!isDecimal(t.pricePct) || !atMost(t.pricePct, 100)) errors["tolerance.pricePct"] = "Enter a percentage from 0 to 100, such as 0.5.";
  if (!isCount(t.dateDays) || !atMost(t.dateDays, 30)) errors["tolerance.dateDays"] = "Enter a whole number of days from 0 to 30.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    policy: {
      customerAccount: code(t.customerAccount),
      vendorAccount: code(t.vendorAccount),
      active: values.active,
      actions: values.actions,
      pricing: t.pricing as PricingPolicy,
      markupPct: t.markupPct.trim(),
      tolerance: { pricePct: t.pricePct.trim(), dateDays: Number.parseInt(t.dateDays.trim(), 10) },
    },
  };
}

export interface PolicyFieldsProps {
  idPrefix: string;
  values: PolicyValues;
  errors: Record<string, string>;
  onChange(values: PolicyValues): void;
  /** Show the Active switch; the detail page has its own activate button. */
  showActive?: boolean;
}

/** The policy inputs shared by the create and edit forms. */
export function PolicyFields({ idPrefix, values, errors, onChange, showActive = false }: PolicyFieldsProps) {
  return (
    <>
      <Fields idPrefix={idPrefix} fields={policyFields} values={values.text} errors={errors} onChange={(text) => onChange({ ...values, text })} />
      <fieldset className={styles.checks}>
        <legend className={styles.checksLegend}>When the seller confirms an order</legend>
        {(Object.keys(actionLabels) as (keyof PartnerActions)[]).map((name) => (
          <span key={name} className={styles.check}>
            <Input
              id={`${idPrefix}-${name}`}
              type="checkbox"
              checked={values.actions[name]}
              onChange={(e) => onChange({ ...values, actions: { ...values.actions, [name]: e.target.checked } })}
            />
            <label htmlFor={`${idPrefix}-${name}`}>{actionLabels[name]}</label>
          </span>
        ))}
        {showActive ? (
          <span className={styles.check}>
            <Input id={`${idPrefix}-active`} type="checkbox" checked={values.active} onChange={(e) => onChange({ ...values, active: e.target.checked })} />
            <label htmlFor={`${idPrefix}-active`}>Active</label>
          </span>
        ) : null}
      </fieldset>
    </>
  );
}

type DirectionKey = "sellerEntity" | "buyerEntity";

const directionFields: FieldSpec<DirectionKey>[] = [
  { name: "sellerEntity", label: "Seller entity", mono: true, hint: "Sells to the buyer; holds the IC customer." },
  { name: "buyerEntity", label: "Buyer entity", mono: true, hint: "Buys from the seller; holds the IC vendor." },
];

export interface NewPartnerValues {
  direction: Record<DirectionKey, string>;
  policy: PolicyValues;
}

/** Checks the new pairing and builds the create request. */
export function validateNewPartner(values: NewPartnerValues): { errors: Record<string, string>; request?: CreatePartnerInput } {
  const { errors, policy } = validatePolicy(values.policy);
  const seller = code(values.direction.sellerEntity);
  const buyer = code(values.direction.buyerEntity);
  if (seller === "") errors.sellerEntity = "Enter the seller entity.";
  else if (seller.length > 64) errors.sellerEntity = "The entity code can have at most 64 characters.";
  if (buyer === "") errors.buyerEntity = "Enter the buyer entity.";
  else if (buyer.length > 64) errors.buyerEntity = "The entity code can have at most 64 characters.";
  else if (buyer === seller) errors.buyerEntity = "The buyer must be a different entity from the seller.";
  if (Object.keys(errors).length > 0 || !policy) return { errors };
  return { errors, request: { ...policy, sellerEntity: seller, buyerEntity: buyer } as CreatePartnerInput };
}

/** Pair a customer in the seller entity with a vendor in the buyer entity (FR-40.1). */
export function PartnerFormPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<NewPartnerValues>({ direction: { sellerEntity: "", buyerEntity: "" }, policy: defaultPolicy });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per submission, kept across retries so a lost response never creates two pairings.
  const key = useRef(newIdempotencyKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const checked = validateNewPartner(values);
    setErrors(checked.errors);
    setFailure(null);
    if (!checked.request) return;
    setBusy(true);
    try {
      const created = await createPartner(checked.request, key.current);
      key.current = newIdempotencyKey();
      navigate(`/intercompany/partners/${created.id}`);
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      // A rejected request is not stored, so the corrected one gets a new key.
      if (problem.status >= 400 && problem.status < 500) key.current = newIdempotencyKey();
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New trading partner"
        description="Pair a customer in the seller entity with a vendor in the buyer entity. Each direction has one active pairing; the reverse direction is a pairing of its own."
        actions={
          <Link className={buttonClassName("secondary")} to="/intercompany/partners">
            All trading partners
          </Link>
        }
      />
      <Card title="Pairing and policy" headingLevel={3}>
        <form className={styles.form} onSubmit={submit} noValidate>
          <Fields
            idPrefix="partner"
            fields={directionFields}
            values={values.direction}
            errors={errors}
            onChange={(direction) => setValues({ ...values, direction })}
          />
          <PolicyFields idPrefix="partner" values={values.policy} errors={errors} onChange={(policy) => setValues({ ...values, policy })} showActive />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The pairing was not saved" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save pairing
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}
