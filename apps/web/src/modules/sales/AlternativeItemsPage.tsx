import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  createAlternativeRule,
  deleteAlternativeRule,
  describeProblem,
  evaluateAlternatives,
  listAlternativeRules,
  type AlternativeEvaluation,
  type AlternativeEvaluationInput,
  type AlternativeOffer,
  type AlternativeRule,
  type AlternativeRuleFilter,
  type AlternativeRuleInput,
  type AtpPeriod,
  type Problem,
} from "./api/client";
import { DECIMAL, dash, shortId, today, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

const DATE = /^\d{4}-\d{2}-\d{2}$/;

export interface AlternativeRuleValues {
  legalEntityId: string;
  orderedProductId: string;
  alternativeProductId: string;
  customerId: string;
  priority: string;
  validFrom: string;
  validTo: string;
}

/** Checks that mirror the service (api/alternatives.go buildAlternativeRule). */
export function validateAlternativeRule(values: AlternativeRuleValues): { errors: Errors; input?: AlternativeRuleInput } {
  const errors: Errors = {};
  const ordered = values.orderedProductId.trim();
  const alternative = values.alternativeProductId.trim();
  const customer = values.customerId.trim();
  const priority = values.priority.trim() === "" ? 1 : Number(values.priority);
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (!UUID.test(ordered)) errors.orderedProductId = "Enter the ordered product's UUID.";
  if (!UUID.test(alternative)) errors.alternativeProductId = "Enter the alternative product's UUID.";
  else if (alternative === ordered) errors.alternativeProductId = "The alternative must differ from the ordered product.";
  if (customer && !UUID.test(customer)) errors.customerId = "Enter the customer's UUID, or leave it empty for every customer.";
  if (!Number.isInteger(priority) || priority < 1 || priority > 99) errors.priority = "Enter a whole number from 1 to 99.";
  if (values.validFrom && !DATE.test(values.validFrom)) errors.validFrom = "Choose a date.";
  if (values.validTo && !DATE.test(values.validTo)) errors.validTo = "Choose a date.";
  if (!errors.validFrom && !errors.validTo && values.validFrom && values.validTo && values.validTo < values.validFrom) errors.validTo = "The last day must not be before the first day.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      legalEntityId: values.legalEntityId.trim(),
      orderedProductId: ordered,
      alternativeProductId: alternative,
      ...(customer ? { customerId: customer } : {}),
      priority,
      ...(values.validFrom ? { validFrom: values.validFrom } : {}),
      ...(values.validTo ? { validTo: values.validTo } : {}),
    },
  };
}

/** "from 2027-03-01 to 2027-06-30", "until ...", "from ..." or a dash when the rule always applies. */
export function validityText(rule: Pick<AlternativeRule, "validFrom" | "validTo">): string {
  if (rule.validFrom && rule.validTo) return `${rule.validFrom} to ${rule.validTo}`;
  if (rule.validFrom) return `from ${rule.validFrom}`;
  if (rule.validTo) return `until ${rule.validTo}`;
  return dash;
}

/** Parses bucket lines "date supply [demand]" (demand defaults to 0). Returns the buckets or the first bad line. */
export function parseBuckets(text: string): { periods: AtpPeriod[]; error?: string } {
  const periods: AtpPeriod[] = [];
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (line === "") continue;
    const [date = "", supply = "", demand = "0", ...rest] = line.split(/[\s,]+/);
    if (rest.length > 0 || !DATE.test(date) || !DECIMAL.test(supply) || !DECIMAL.test(demand)) return { periods, error: `"${line}" is not "date supply demand".` };
    if (periods.length > 0 && date <= periods[periods.length - 1].date) return { periods, error: `"${line}" must be dated after the line before it.` };
    periods.push({ date, supply, demand });
  }
  return { periods };
}

/** Parses alternative bucket lines "productUUID date supply [demand]", grouped by product in first-seen order. */
export function parseAlternativeBuckets(text: string): { alternatives: { productId: string; periods: AtpPeriod[] }[]; error?: string } {
  const byProduct = new Map<string, string[]>();
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (line === "") continue;
    const [product = "", ...rest] = line.split(/[\s,]+/);
    if (!UUID.test(product)) return { alternatives: [], error: `"${line}" must start with the alternative product's UUID.` };
    byProduct.set(product, [...(byProduct.get(product) ?? []), rest.join(" ")]);
  }
  const alternatives: { productId: string; periods: AtpPeriod[] }[] = [];
  for (const [productId, lines] of byProduct) {
    const { periods, error } = parseBuckets(lines.join("\n"));
    if (error) return { alternatives: [], error };
    alternatives.push({ productId, periods });
  }
  return { alternatives };
}

const offerColumns: DataTableColumn<AlternativeOffer>[] = [
  { key: "alternativeProductId", header: "Alternative product", mono: true },
  { key: "priority", header: "Priority", align: "end" },
  { key: "availableOnDate", header: "Ships by the date", align: "end" },
  { key: "coversQuantity", header: "Covers the quantity", render: (o) => <Badge tone={o.coversQuantity ? "success" : "warning"}>{o.coversQuantity ? "Covers it" : "Partly"}</Badge> },
  { key: "promisedDate", header: "Last delivery", mono: true, render: (o) => o.promise.promisedDate ?? dash },
];

/** One sentence about the ordered product: whether it needs an alternative. */
export function orderedSummary(result: AlternativeEvaluation, quantity: string, requested: string): string {
  if (!result.needed) return `The ordered product covers ${quantity} by ${requested}; no alternative is needed.`;
  return `The ordered product can ship ${result.ordered.availableOnDate} of ${quantity} by ${requested}.`;
}

/** Checks which alternatives could ship what the ordered product cannot (FR-27.13); nothing is stored. */
function CheckCard() {
  const [values, setValues] = useState({
    legalEntityId: "",
    customerId: "",
    productId: "",
    quantity: "",
    requested: today(),
    periods: "",
    alternatives: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [result, setResult] = useState<{ value: AlternativeEvaluation; quantity: string; requested: string } | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Errors = {};
    if (!UUID.test(values.legalEntityId.trim())) found.legalEntityId = "Enter the legal entity's UUID.";
    if (values.customerId.trim() && !UUID.test(values.customerId.trim())) found.customerId = "Enter the customer's UUID.";
    if (!UUID.test(values.productId.trim())) found.productId = "Enter the ordered product's UUID.";
    if (!DECIMAL.test(values.quantity) || Number(values.quantity) <= 0) found.quantity = "Enter a quantity above 0 with up to 6 decimals.";
    if (!DATE.test(values.requested)) found.requested = "Choose the requested date.";
    const ordered = parseBuckets(values.periods);
    if (ordered.error) found.periods = ordered.error;
    else if (ordered.periods.length === 0) found.periods = "Enter at least one bucket.";
    const alternatives = parseAlternativeBuckets(values.alternatives);
    if (alternatives.error) found.alternatives = alternatives.error;
    else if (alternatives.alternatives.length > 20) found.alternatives = "Enter at most 20 alternative products.";
    setErrors(found);
    setFailure(null);
    setResult(null);
    if (Object.keys(found).length > 0) return;
    const input: AlternativeEvaluationInput = {
      legalEntityId: values.legalEntityId.trim(),
      ...(values.customerId.trim() ? { customerId: values.customerId.trim() } : {}),
      productId: values.productId.trim(),
      quantity: values.quantity,
      requested: values.requested,
      periods: ordered.periods,
      ...(alternatives.alternatives.length > 0 ? { alternatives: alternatives.alternatives } : {}),
    };
    setBusy(true);
    try {
      setResult({ value: await evaluateAlternatives(input), quantity: values.quantity, requested: values.requested });
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const text = (name: "legalEntityId" | "customerId" | "productId", label: string, optional?: boolean) => (
    <FormField id={`alt-check-${name}`} label={label} error={errors[name]} optional={optional}>
      {(control) => <Input {...control} className={styles.mono} value={values[name]} onChange={(e) => setValues((v) => ({ ...v, [name]: e.target.value }))} invalid={Boolean(errors[name])} />}
    </FormField>
  );
  const area = (name: "periods" | "alternatives", label: string, hint: string, optional?: boolean) => (
    <FormField id={`alt-check-${name}`} label={label} hint={hint} error={errors[name]} optional={optional}>
      {(control) => (
        <Textarea {...control} mono rows={4} value={values[name]} onChange={(e) => setValues((v) => ({ ...v, [name]: e.target.value }))} invalid={Boolean(errors[name])} />
      )}
    </FormField>
  );

  return (
    <Card title="Check alternatives for a line">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Check alternatives for a line">
        {text("legalEntityId", "Legal entity")}
        {text("customerId", "Customer", true)}
        {text("productId", "Ordered product")}
        <FormField id="alt-check-quantity" label="Quantity" error={errors.quantity}>
          {(control) => <Input {...control} inputMode="decimal" value={values.quantity} onChange={(e) => setValues((v) => ({ ...v, quantity: e.target.value }))} invalid={Boolean(errors.quantity)} />}
        </FormField>
        <FormField id="alt-check-requested" label="Requested date" error={errors.requested}>
          {(control) => <Input {...control} type="date" value={values.requested} onChange={(e) => setValues((v) => ({ ...v, requested: e.target.value }))} invalid={Boolean(errors.requested)} />}
        </FormField>
        {area("periods", "Ordered product buckets", "One per line: date supply demand, earliest first.")}
        {area("alternatives", "Alternative product buckets", "One per line: product UUID, date, supply, demand. An alternative without a line is listed as unchecked.", true)}
        <div className={styles.formActions}>
          <Button type="submit" loading={busy}>
            Check alternatives
          </Button>
        </div>
      </form>
      {failure ? (
        <Alert tone="danger" title="Could not check the alternatives" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <p role="status" className={styles.muted}>
        {result ? orderedSummary(result.value, result.quantity, result.requested) : ""}
      </p>
      {result === null || !result.value.needed ? null : (
        <>
          {result.value.offers.length === 0 ? (
            <EmptyState compact headingLevel={3} title="No alternative can ship by the date" description="No applicable alternative has free supply by the requested date." />
          ) : (
            <DataTable label="Offered alternatives" columns={offerColumns} rows={result.value.offers} getRowKey={(o) => o.ruleId} />
          )}
          {result.value.unchecked.length > 0 ? (
            <Alert tone="info" title="Not checked" live="status">
              {result.value.unchecked.map((u) => u.alternativeProductId).join(", ")} came without buckets.
            </Alert>
          ) : null}
        </>
      )}
    </Card>
  );
}

/** Alternative item rules (FR-27.13): products offered when the ordered one cannot ship in time. */
export function AlternativeItemsPage() {
  const [draft, setDraft] = useState<AlternativeRuleFilter>({ legalEntityId: "", orderedProductId: "" });
  const [filter, setFilter] = useState<AlternativeRuleFilter>({});
  const [confirming, setConfirming] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const invalid: Errors = {};
  if (draft.legalEntityId?.trim() && !UUID.test(draft.legalEntityId.trim())) invalid.legalEntityId = "Enter a legal entity UUID.";
  if (draft.orderedProductId?.trim() && !UUID.test(draft.orderedProductId.trim())) invalid.orderedProductId = "Enter a product UUID.";
  const state = useLoad((signal) => listAlternativeRules(filter, signal), JSON.stringify(filter));

  async function remove(rule: AlternativeRule) {
    setBusy(rule.id);
    setFailure(null);
    setNotice("");
    try {
      await deleteAlternativeRule(rule.id);
      setNotice(`The rule offering ${shortId(rule.alternativeProductId)} for ${shortId(rule.orderedProductId)} was removed.`);
      setConfirming(null);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const columns: DataTableColumn<AlternativeRule>[] = [
    { key: "orderedProductId", header: "Ordered product", mono: true },
    { key: "alternativeProductId", header: "Alternative product", mono: true },
    { key: "priority", header: "Priority", align: "end" },
    { key: "customerId", header: "Customer", mono: true, render: (r) => (r.customerId ? shortId(r.customerId) : "Every customer") },
    { key: "validity", header: "Valid", render: (r) => validityText(r) },
    { key: "legalEntityId", header: "Legal entity", mono: true, render: (r) => shortId(r.legalEntityId) },
    {
      key: "actions",
      header: "Action",
      render: (r) =>
        confirming === r.id ? (
          <div className={styles.actions}>
            <Button size="sm" variant="danger" loading={busy === r.id} onClick={() => remove(r)} aria-label={`Confirm removing the rule for ${r.alternativeProductId}`}>
              Confirm removal
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setConfirming(null)}>
              Keep
            </Button>
          </div>
        ) : (
          <Button size="sm" disabled={busy !== null} onClick={() => setConfirming(r.id)} aria-label={`Remove the rule for ${r.alternativeProductId}`}>
            Remove
          </Button>
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Alternative items"
        description="Products to offer when the ordered one cannot ship the whole quantity by the requested date. The check uses the available-to-promise of each alternative; the line is only changed if the user swaps the product."
        actions={
          <Link className={buttonClassName("primary")} to="/sales/alternative-items/new">
            New rule
          </Link>
        }
      />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          if (Object.keys(invalid).length === 0) setFilter({ legalEntityId: draft.legalEntityId?.trim(), orderedProductId: draft.orderedProductId?.trim() });
        }}
      >
        <FormField id="alt-le" label="Legal entity" error={invalid.legalEntityId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })} invalid={Boolean(invalid.legalEntityId)} />}
        </FormField>
        <FormField id="alt-ordered" label="Ordered product" error={invalid.orderedProductId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.orderedProductId} onChange={(e) => setDraft({ ...draft, orderedProductId: e.target.value })} invalid={Boolean(invalid.orderedProductId)} />}
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
        <Alert tone="danger" title="Could not load alternative item rules" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Alternative item rules"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(r) => r.id}
          loading={state.loading}
          empty={{ title: "No alternative item rules yet", description: "Add a rule to offer a substitute when a product cannot ship in time." }}
        />
      )}
      <CheckCard />
    </div>
  );
}

/** Creates an alternative item rule (duty sales.listing.maintain). */
export function NewAlternativeRulePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<AlternativeRuleValues>({ legalEntityId: "", orderedProductId: "", alternativeProductId: "", customerId: "", priority: "1", validFrom: "", validTo: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateAlternativeRule(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createAlternativeRule(input);
      navigate("/sales/alternative-items");
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const field = (name: keyof AlternativeRuleValues, label: string, hint: string, opts: { optional?: boolean; type?: string; mono?: boolean } = {}) => (
    <FormField id={`alt-${name}`} label={label} hint={hint} error={errors[name]} optional={opts.optional}>
      {(control) => (
        <Input
          {...control}
          type={opts.type}
          className={opts.mono === false ? undefined : styles.mono}
          value={values[name]}
          onChange={(e) => setValues((v) => ({ ...v, [name]: e.target.value }))}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New alternative item rule" description="When the ordered product cannot ship the whole quantity by the requested date, the alternative is offered." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New alternative item rule">
          {field("legalEntityId", "Legal entity", "UUID of the selling legal entity.")}
          {field("orderedProductId", "Ordered product", "UUID of the product that may fall short.")}
          {field("alternativeProductId", "Alternative product", "UUID of the product to offer instead.")}
          {field("customerId", "Customer", "Leave empty for every customer.", { optional: true })}
          {field("priority", "Priority", "1 to 99; 1 is offered first.", { mono: false })}
          {field("validFrom", "Valid from", "First requested date the rule applies to.", { optional: true, type: "date", mono: false })}
          {field("validTo", "Valid to", "Last requested date the rule applies to.", { optional: true, type: "date", mono: false })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "SLS-0055" ? "This rule already exists" : "Could not save the rule"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save rule
            </Button>
            <Link className={buttonClassName("ghost")} to="/sales/alternative-items">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
