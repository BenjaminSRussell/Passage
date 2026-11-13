import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  type DataTableColumn,
  FormErrorSummary,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type FormFieldTarget,
} from "../../shell/ui";
import {
  createSupplementaryRule,
  deleteSupplementaryRule,
  describeProblem,
  evaluateSupplementary,
  listSupplementaryRules,
  SUPPLEMENTARY_PRICING,
  SUPPLEMENTARY_REQUIREMENTS,
  type Problem,
  type SupplementaryEvaluationInput,
  type SupplementaryLine,
  type SupplementaryPricing,
  type SupplementaryRequirement,
  type SupplementaryRule,
  type SupplementaryRuleFilter,
  type SupplementaryRuleInput,
} from "./api/client";
import { DECIMAL, sentence, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { PlainName } from "./OrderNumber";

const PRICE = /^\d+(\.\d{1,4})?$/;
const ZERO = /^0+(\.0+)?$/;

type Errors = Record<string, string>;

export interface RuleValues {
  legalEntityId: string;
  parentProductId: string;
  customerId: string;
  supplementaryProductId: string;
  unit: string;
  requirement: SupplementaryRequirement;
  baseQuantity: string;
  quantityPerBase: string;
  pricing: SupplementaryPricing;
  unitPrice: string;
}

/** Checks that mirror the service (api/supplementary.go buildSupplementaryRule). */
export function validateRule(values: RuleValues): { errors: Errors; input?: SupplementaryRuleInput } {
  const errors: Errors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (!UUID.test(values.parentProductId.trim())) errors.parentProductId = "Enter the ordered product's UUID.";
  const customer = values.customerId.trim();
  if (customer && !UUID.test(customer)) errors.customerId = "Enter the customer's UUID, or leave it empty for every customer.";
  if (!UUID.test(values.supplementaryProductId.trim())) errors.supplementaryProductId = "Enter the supplementary product's UUID.";
  else if (values.supplementaryProductId.trim() === values.parentProductId.trim()) errors.supplementaryProductId = "The supplementary product must differ from the ordered product.";
  if (values.unit.trim().length < 1 || values.unit.trim().length > 10) errors.unit = "Enter a unit of up to 10 characters.";
  if (!DECIMAL.test(values.baseQuantity) || ZERO.test(values.baseQuantity)) errors.baseQuantity = "Enter a quantity above 0 with up to 6 decimals.";
  if (!DECIMAL.test(values.quantityPerBase) || ZERO.test(values.quantityPerBase)) errors.quantityPerBase = "Enter a quantity above 0 with up to 6 decimals.";
  if (values.pricing === "PRICED" && (!PRICE.test(values.unitPrice) || ZERO.test(values.unitPrice))) errors.unitPrice = "Enter a price above 0 with up to 4 decimals.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      legalEntityId: values.legalEntityId.trim(),
      parentProductId: values.parentProductId.trim(),
      ...(customer ? { customerId: customer } : {}),
      supplementaryProductId: values.supplementaryProductId.trim(),
      unit: values.unit.trim(),
      requirement: values.requirement,
      baseQuantity: values.baseQuantity,
      quantityPerBase: values.quantityPerBase,
      pricing: values.pricing,
      ...(values.pricing === "PRICED" ? { unitPrice: values.unitPrice } : {}),
    },
  };
}

const requirementBadge = (r: SupplementaryRequirement) => <Badge tone={r === "MANDATORY" ? "info" : "neutral"}>{sentence(r)}</Badge>;
const priceText = (pricing: SupplementaryPricing | undefined, price: string) => (pricing === "FREE" || ZERO.test(price) ? "Free" : price);

const evaluationColumns: DataTableColumn<SupplementaryLine>[] = [
  { key: "productId", header: "Supplementary product", render: (row) => <PlainName entity="product" id={row.productId} /> },
  { key: "quantity", header: "Quantity", align: "end", render: (l) => `${l.quantity} ${l.unit}` },
  { key: "requirement", header: "Added", render: (l) => (l.requirement === "MANDATORY" ? requirementBadge(l.requirement) : <Badge tone="neutral">Offered</Badge>) },
  { key: "unitPrice", header: "Unit price", align: "end", render: (l) => priceText(undefined, l.unitPrice) },
];

/** Checks which supplementary lines one order line would bring (FR-27.14); nothing is stored. */
function EvaluateCard() {
  const [values, setValues] = useState({ legalEntityId: "", productId: "", customerId: "", quantity: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [result, setResult] = useState<SupplementaryLine[] | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Errors = {};
    if (!UUID.test(values.legalEntityId.trim())) found.legalEntityId = "Enter the legal entity's UUID.";
    if (!UUID.test(values.productId.trim())) found.productId = "Enter the ordered product's UUID.";
    if (values.customerId.trim() && !UUID.test(values.customerId.trim())) found.customerId = "Enter the customer's UUID.";
    if (!DECIMAL.test(values.quantity) || ZERO.test(values.quantity)) found.quantity = "Enter a quantity above 0 with up to 6 decimals.";
    setErrors(found);
    setFailure(null);
    setResult(null);
    if (Object.keys(found).length > 0) return;
    const input: SupplementaryEvaluationInput = {
      legalEntityId: values.legalEntityId.trim(),
      productId: values.productId.trim(),
      ...(values.customerId.trim() ? { customerId: values.customerId.trim() } : {}),
      quantity: values.quantity,
    };
    setBusy(true);
    try {
      setResult(await evaluateSupplementary(input));
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const field = (name: keyof typeof values, label: string, extra: { mono?: boolean; optional?: boolean; decimal?: boolean } = {}) => (
    <FormField id={`evaluate-${name}`} label={label} error={errors[name]} optional={extra.optional}>
      {(control) => (
        <Input
          {...control}
          inputMode={extra.decimal ? "decimal" : undefined}
          className={extra.mono ? styles.mono : undefined}
          value={values[name]}
          onChange={(e) => setValues((v) => ({ ...v, [name]: e.target.value }))}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <Card title="Check an order line">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Check an order line">
        {field("legalEntityId", "Legal entity", { mono: true })}
        {field("productId", "Ordered product", { mono: true })}
        {field("customerId", "Customer", { mono: true, optional: true })}
        {field("quantity", "Ordered quantity", { decimal: true })}
        <div className={styles.formActions}>
          <Button type="submit" loading={busy}>
            Check line
          </Button>
        </div>
      </form>
      {failure ? (
        <Alert tone="danger" title="Could not check the line" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {result === null ? null : result.length === 0 ? (
        <EmptyState compact headingLevel={3} title="No supplementary items" description="No rule applies to this product, customer and quantity." />
      ) : (
        <DataTable label="Supplementary items for the line" columns={evaluationColumns} rows={result} getRowKey={(l) => l.ruleId} />
      )}
    </Card>
  );
}

/** Supplementary item rules (FR-27.14, AX PriceDiscSuppItem): items that go with an ordered product. */
export function SupplementaryRulesPage() {
  const [draft, setDraft] = useState<SupplementaryRuleFilter>({ legalEntityId: "", parentProductId: "" });
  const [filter, setFilter] = useState<SupplementaryRuleFilter>({});
  const [confirming, setConfirming] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const invalid: Errors = {};
  if (draft.legalEntityId?.trim() && !UUID.test(draft.legalEntityId.trim())) invalid.legalEntityId = "Enter a legal entity UUID.";
  if (draft.parentProductId?.trim() && !UUID.test(draft.parentProductId.trim())) invalid.parentProductId = "Enter a product UUID.";
  const state = useLoad((signal) => listSupplementaryRules(filter, signal), JSON.stringify(filter));

  async function remove(rule: SupplementaryRule) {
    setBusy(rule.id);
    setFailure(null);
    setNotice("");
    try {
      await deleteSupplementaryRule(rule.id);
      setNotice(`The rule adding ${shortId(rule.supplementaryProductId)} to ${shortId(rule.parentProductId)} was removed.`);
      setConfirming(null);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const columns: DataTableColumn<SupplementaryRule>[] = [
    { key: "parentProductId", header: "Ordered product", mono: true },
    { key: "supplementaryProductId", header: "Supplementary product", mono: true },
    { key: "quantity", header: "Quantity", render: (r) => `${r.quantityPerBase} ${r.unit} per ${r.baseQuantity}` },
    { key: "requirement", header: "Requirement", render: (r) => requirementBadge(r.requirement) },
    { key: "unitPrice", header: "Price", align: "end", render: (r) => priceText(r.pricing, r.unitPrice) },
    { key: "customerId", header: "Customer", render: (r) => (r.customerId ? <EntityName entity="customer" id={r.customerId} /> : "Every customer") },
    { key: "legalEntityId", header: "Legal entity", render: (r) => <EntityName entity="legalEntity" id={r.legalEntityId} /> },
    {
      key: "actions",
      header: "Action",
      render: (r) =>
        confirming === r.id ? (
          <div className={styles.actions}>
            <Button size="sm" variant="danger" loading={busy === r.id} onClick={() => remove(r)} aria-label={`Confirm removing the rule for ${r.supplementaryProductId}`}>
              Confirm removal
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setConfirming(null)}>
              Keep
            </Button>
          </div>
        ) : (
          <Button size="sm" disabled={busy !== null} onClick={() => setConfirming(r.id)} aria-label={`Remove the rule for ${r.supplementaryProductId}`}>
            Remove
          </Button>
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Supplementary items"
        description="Items that go with an ordered product, such as a manual with every 10 units. Mandatory items are added to the line; optional ones are offered. A customer's own rule replaces the rule for every customer."
        actions={
          <Link className={buttonClassName("primary")} to="/sales/supplementary-items/new">
            New rule
          </Link>
        }
      />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          if (Object.keys(invalid).length === 0) setFilter({ legalEntityId: draft.legalEntityId?.trim(), parentProductId: draft.parentProductId?.trim() });
        }}
      >
        <FormField id="supp-le" label="Legal entity" error={invalid.legalEntityId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })} invalid={Boolean(invalid.legalEntityId)} />}
        </FormField>
        <FormField id="supp-parent" label="Ordered product" error={invalid.parentProductId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.parentProductId} onChange={(e) => setDraft({ ...draft, parentProductId: e.target.value })} invalid={Boolean(invalid.parentProductId)} />}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {failure ? (
        <Alert tone="danger" title="Could not remove the rule" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <Alert tone="success" live="status">
        {notice || null}
      </Alert>
      {state.error ? (
        <Alert tone="danger" title="Could not load supplementary item rules" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Supplementary item rules"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(r) => r.id}
          loading={state.loading}
          empty={{ title: "No supplementary item rules yet", description: "Add a rule to bring an item, such as a manual or a cable, with an ordered product." }}
        />
      )}
      <EvaluateCard />
    </div>
  );
}

const NEW_SUPPLEMENTARY_RULE_FIELDS: Record<string, FormFieldTarget> = {
  legalEntityId: { id: "rule-legalEntityId", label: "Legal entity" },
  parentProductId: { id: "rule-parentProductId", label: "Ordered product" },
  customerId: { id: "rule-customerId", label: "Customer" },
  supplementaryProductId: { id: "rule-supplementaryProductId", label: "Supplementary product" },
  baseQuantity: { id: "rule-baseQuantity", label: "Per ordered quantity" },
  quantityPerBase: { id: "rule-quantityPerBase", label: "Supplementary quantity" },
  unit: { id: "rule-unit", label: "Unit" },
  unitPrice: { id: "rule-unitPrice", label: "Unit price" },
  productId: { id: "evaluate-productId", label: "Ordered product" },
  quantity: { id: "evaluate-quantity", label: "Ordered quantity" },
};

/** Creates a supplementary item rule (duty sales.listing.maintain). */
export function NewSupplementaryRulePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<RuleValues>({
    legalEntityId: "",
    parentProductId: "",
    customerId: "",
    supplementaryProductId: "",
    unit: "EA",
    requirement: "MANDATORY",
    baseQuantity: "",
    quantityPerBase: "1",
    pricing: "FREE",
    unitPrice: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([values]));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateRule(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createSupplementaryRule(input);
      release();
      navigate("/sales/supplementary-items");
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const field = (name: keyof RuleValues, label: string, hint?: string, extra: { mono?: boolean; optional?: boolean; decimal?: boolean } = {}) => (
    <FormField id={`rule-${name}`} label={label} hint={hint} error={errors[name]} optional={extra.optional}>
      {(control) => (
        <Input
          {...control}
          inputMode={extra.decimal ? "decimal" : undefined}
          className={extra.mono ? styles.mono : undefined}
          value={values[name]}
          onChange={(e) => setValues((v) => ({ ...v, [name]: e.target.value }))}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New supplementary item rule" description="For every whole base quantity of the ordered product, the line brings the given quantity of the supplementary product." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New supplementary item rule">
          <FormErrorSummary items={summaryItems(errors, NEW_SUPPLEMENTARY_RULE_FIELDS)} attempt={attempt} />
          {field("legalEntityId", "Legal entity", "UUID of the selling legal entity.", { mono: true })}
          {field("parentProductId", "Ordered product", "UUID of the product the rule applies to.", { mono: true })}
          {field("customerId", "Customer", "Leave empty for every customer.", { mono: true, optional: true })}
          {field("supplementaryProductId", "Supplementary product", "UUID of the item that goes with it.", { mono: true })}
          {field("baseQuantity", "Per ordered quantity", "For example 10: one step for every 10 ordered.", { decimal: true })}
          {field("quantityPerBase", "Supplementary quantity", "Added for each step.", { decimal: true })}
          {field("unit", "Unit")}
          <FormField id="rule-requirement" label="Requirement" hint="Mandatory items are added; optional ones are offered.">
            {(control) => (
              <Select {...control} value={values.requirement} onChange={(e) => setValues((v) => ({ ...v, requirement: e.target.value as SupplementaryRequirement }))}>
                {SUPPLEMENTARY_REQUIREMENTS.map((r) => (
                  <option key={r} value={r}>
                    {sentence(r)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="rule-pricing" label="Pricing">
            {(control) => (
              <Select {...control} value={values.pricing} onChange={(e) => setValues((v) => ({ ...v, pricing: e.target.value as SupplementaryPricing }))}>
                {SUPPLEMENTARY_PRICING.map((p) => (
                  <option key={p} value={p}>
                    {sentence(p)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {values.pricing === "PRICED" ? field("unitPrice", "Unit price", undefined, { decimal: true }) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "SLS-0033" ? "This rule already exists" : "Could not save the rule"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save rule
            </Button>
            <Link className={buttonClassName("ghost")} to="/sales/supplementary-items">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      {prompt}
    </div>
  );
}
