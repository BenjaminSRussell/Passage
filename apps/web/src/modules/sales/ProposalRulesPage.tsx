import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  createProposalRule,
  deleteProposalRule,
  describeProblem,
  evaluateProposals,
  listProposalRules,
  PROPOSAL_REASONS,
  type Problem,
  type Proposal,
  type ProposalReason,
  type ProposalRule,
  type ProposalRuleFilter,
  type ProposalRuleInput,
} from "./api/client";
import { dash, sentence, shortId, UUID } from "./format";
import { reasonBadge } from "./OrderProposals";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

export interface ProposalRuleValues {
  legalEntityId: string;
  orderedProductId: string;
  proposedProductId: string;
  customerId: string;
  reason: ProposalReason;
}

/** Checks that mirror the service (api/proposals.go buildProposalRule). */
export function validateProposalRule(values: ProposalRuleValues): { errors: Errors; input?: ProposalRuleInput } {
  const errors: Errors = {};
  const ordered = values.orderedProductId.trim();
  const proposed = values.proposedProductId.trim();
  const customer = values.customerId.trim();
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (!UUID.test(ordered)) errors.orderedProductId = "Enter the ordered product's UUID.";
  if (!UUID.test(proposed)) errors.proposedProductId = "Enter the proposed product's UUID.";
  else if (proposed === ordered) errors.proposedProductId = "The proposed product must differ from the ordered product.";
  if (customer && !UUID.test(customer)) errors.customerId = "Enter the customer's UUID, or leave it empty for every customer.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: { legalEntityId: values.legalEntityId.trim(), orderedProductId: ordered, proposedProductId: proposed, ...(customer ? { customerId: customer } : {}), reason: values.reason },
  };
}

/** "3 of 4 (75%)" accepted, or a dash before any answer. Counts are whole numbers. */
export function acceptanceRate(rule: Pick<ProposalRule, "acceptedCount" | "declinedCount">): string {
  const total = rule.acceptedCount + rule.declinedCount;
  if (total === 0) return dash;
  return `${rule.acceptedCount} of ${total} (${Math.round((rule.acceptedCount * 100) / total)}%)`;
}

/** Splits a comma or space separated list of ids; blanks are dropped. */
export function parseIds(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
}

const proposalColumns: DataTableColumn<Proposal>[] = [
  { key: "proposedProductId", header: "Proposed product", mono: true },
  { key: "orderedProductId", header: "For ordered product", mono: true },
  { key: "reason", header: "Reason", render: (p) => reasonBadge(p.reason) },
];

/** Checks which products the service would propose for products being ordered (FR-27.21); nothing is stored. */
function CheckCard() {
  const [values, setValues] = useState({ legalEntityId: "", productIds: "", currentProductIds: "", customerId: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [result, setResult] = useState<Proposal[] | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const products = parseIds(values.productIds);
    const current = parseIds(values.currentProductIds);
    const found: Errors = {};
    if (!UUID.test(values.legalEntityId.trim())) found.legalEntityId = "Enter the legal entity's UUID.";
    if (products.length === 0 || products.length > 50 || !products.every((id) => UUID.test(id))) found.productIds = "Enter 1 to 50 product UUIDs.";
    if (current.length > 50 || !current.every((id) => UUID.test(id))) found.currentProductIds = "Enter up to 50 product UUIDs.";
    if (values.customerId.trim() && !UUID.test(values.customerId.trim())) found.customerId = "Enter the customer's UUID.";
    setErrors(found);
    setFailure(null);
    setResult(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setResult(
        await evaluateProposals({
          legalEntityId: values.legalEntityId.trim(),
          ...(values.customerId.trim() ? { customerId: values.customerId.trim() } : {}),
          productIds: products,
          ...(current.length > 0 ? { currentProductIds: current } : {}),
        }),
      );
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const field = (name: keyof typeof values, label: string, hint?: string, optional?: boolean) => (
    <FormField id={`proposal-check-${name}`} label={label} hint={hint} error={errors[name]} optional={optional}>
      {(control) => (
        <Input {...control} className={styles.mono} value={values[name]} onChange={(e) => setValues((v) => ({ ...v, [name]: e.target.value }))} invalid={Boolean(errors[name])} />
      )}
    </FormField>
  );

  return (
    <Card title="Check proposals for an order">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Check proposals for an order">
        {field("legalEntityId", "Legal entity")}
        {field("productIds", "Products being ordered", "UUIDs, separated by commas.")}
        {field("currentProductIds", "Products already on the order", "Never proposed again.", true)}
        {field("customerId", "Customer", undefined, true)}
        <div className={styles.formActions}>
          <Button type="submit" loading={busy}>
            Check proposals
          </Button>
        </div>
      </form>
      {failure ? (
        <Alert tone="danger" title="Could not check the proposals" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {result === null ? null : result.length === 0 ? (
        <EmptyState compact headingLevel={3} title="No proposals" description="No rule proposes a product that is not already on the order." />
      ) : (
        <DataTable label="Proposals for the order" columns={proposalColumns} rows={result} getRowKey={(p) => p.ruleId} />
      )}
    </Card>
  );
}

/** Product proposal rules (FR-27.21, SAP VB41 cross-selling): products offered when another is ordered. */
export function ProposalRulesPage() {
  const [draft, setDraft] = useState<ProposalRuleFilter>({ legalEntityId: "", orderedProductId: "" });
  const [filter, setFilter] = useState<ProposalRuleFilter>({});
  const [confirming, setConfirming] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const invalid: Errors = {};
  if (draft.legalEntityId?.trim() && !UUID.test(draft.legalEntityId.trim())) invalid.legalEntityId = "Enter a legal entity UUID.";
  if (draft.orderedProductId?.trim() && !UUID.test(draft.orderedProductId.trim())) invalid.orderedProductId = "Enter a product UUID.";
  const state = useLoad((signal) => listProposalRules(filter, signal), JSON.stringify(filter));

  async function remove(rule: ProposalRule) {
    setBusy(rule.id);
    setFailure(null);
    setNotice("");
    try {
      await deleteProposalRule(rule.id);
      setNotice(`The rule proposing ${shortId(rule.proposedProductId)} with ${shortId(rule.orderedProductId)} was removed. Its recorded answers are kept.`);
      setConfirming(null);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const columns: DataTableColumn<ProposalRule>[] = [
    { key: "orderedProductId", header: "Ordered product", mono: true },
    { key: "proposedProductId", header: "Proposed product", mono: true },
    { key: "reason", header: "Reason", render: (r) => reasonBadge(r.reason) },
    { key: "customerId", header: "Customer", mono: true, render: (r) => (r.customerId ? shortId(r.customerId) : "Every customer") },
    { key: "acceptance", header: "Accepted", align: "end", render: (r) => acceptanceRate(r) },
    { key: "legalEntityId", header: "Legal entity", mono: true, render: (r) => shortId(r.legalEntityId) },
    {
      key: "actions",
      header: "Action",
      render: (r) =>
        confirming === r.id ? (
          <div className={styles.actions}>
            <Button size="sm" variant="danger" loading={busy === r.id} onClick={() => remove(r)} aria-label={`Confirm removing the rule for ${r.proposedProductId}`}>
              Confirm removal
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setConfirming(null)}>
              Keep
            </Button>
          </div>
        ) : (
          <Button size="sm" disabled={busy !== null} onClick={() => setConfirming(r.id)} aria-label={`Remove the rule for ${r.proposedProductId}`}>
            Remove
          </Button>
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Product proposals"
        description="Cross-sell, up-sell and accessory products offered when another product is ordered. A proposal adds nothing to the order unless the user accepts it; each answer counts toward the rule's acceptance rate."
        actions={
          <Link className={buttonClassName("primary")} to="/sales/product-proposals/new">
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
        <FormField id="proposal-le" label="Legal entity" error={invalid.legalEntityId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })} invalid={Boolean(invalid.legalEntityId)} />}
        </FormField>
        <FormField id="proposal-ordered" label="Ordered product" error={invalid.orderedProductId} optional>
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
        <Alert tone="danger" title="Could not load product proposal rules" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Product proposal rules"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(r) => r.id}
          loading={state.loading}
          empty={{ title: "No product proposal rules yet", description: "Add a rule to offer an accessory, an upgrade or a related product when a product is ordered." }}
        />
      )}
      <CheckCard />
    </div>
  );
}

/** Creates a product proposal rule (duty sales.listing.maintain). */
export function NewProposalRulePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<ProposalRuleValues>({ legalEntityId: "", orderedProductId: "", proposedProductId: "", customerId: "", reason: "CROSS_SELL" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateProposalRule(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createProposalRule(input);
      navigate("/sales/product-proposals");
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const field = (name: Exclude<keyof ProposalRuleValues, "reason">, label: string, hint: string, optional?: boolean) => (
    <FormField id={`proposal-${name}`} label={label} hint={hint} error={errors[name]} optional={optional}>
      {(control) => (
        <Input {...control} className={styles.mono} value={values[name]} onChange={(e) => setValues((v) => ({ ...v, [name]: e.target.value }))} invalid={Boolean(errors[name])} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New product proposal rule" description="When the ordered product is entered on an order, the proposed product is offered to the user." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New product proposal rule">
          {field("legalEntityId", "Legal entity", "UUID of the selling legal entity.")}
          {field("orderedProductId", "Ordered product", "UUID of the product that triggers the proposal.")}
          {field("proposedProductId", "Proposed product", "UUID of the product to offer.")}
          {field("customerId", "Customer", "Leave empty for every customer.", true)}
          <FormField id="proposal-reason" label="Reason" hint="Shown with the proposal.">
            {(control) => (
              <Select {...control} value={values.reason} onChange={(e) => setValues((v) => ({ ...v, reason: e.target.value as ProposalReason }))}>
                {PROPOSAL_REASONS.map((r) => (
                  <option key={r} value={r}>
                    {sentence(r)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "SLS-0034" ? "This rule already exists" : "Could not save the rule"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save rule
            </Button>
            <Link className={buttonClassName("ghost")} to="/sales/product-proposals">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
