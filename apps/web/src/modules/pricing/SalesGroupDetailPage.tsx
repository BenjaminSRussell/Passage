import { useRef, useState, type FormEvent } from "react";
import { RepName } from "./RepName";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  createCommissionRule,
  describeProblem,
  getSalesGroup,
  listCommissionRules,
  newKey,
  type CommissionBasis,
  type CommissionRule,
  type Problem,
  type RepShare,
} from "./api/client";
import { COMMISSION_BASIS_LABELS, groupLabel, ruleLevel } from "./commissionFormat";
import { DECIMAL } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export interface RuleDraft {
  customerGroup: string;
  itemGroup: string;
  basis: CommissionBasis;
  ratePct: string;
}

type Errors = Record<string, string>;

export const emptyRule: RuleDraft = { customerGroup: "", itemGroup: "", basis: "REVENUE", ratePct: "" };

/** Client checks that mirror the service (BR-26.12); the service has the last word. */
export function validateRule(v: RuleDraft): Errors {
  const errors: Errors = {};
  if (v.customerGroup.trim().length > 64) errors.customerGroup = "Use up to 64 characters.";
  if (v.itemGroup.trim().length > 64) errors.itemGroup = "Use up to 64 characters.";
  if (!DECIMAL.test(v.ratePct) || Number(v.ratePct) > 100) errors.ratePct = "Enter a rate from 0 to 100.";
  return errors;
}

const repColumns: DataTableColumn<RepShare>[] = [
  { key: "repId", header: "Rep", render: (r) => <RepName id={r.repId} /> },
  { key: "sharePct", header: "Share", mono: true, align: "end", render: (r) => `${r.sharePct}%` },
];

const ruleColumns: DataTableColumn<CommissionRule>[] = [
  { key: "level", header: "Search order", align: "end", render: (r) => String(ruleLevel(r)) },
  { key: "customerGroup", header: "Customer group", mono: true, render: (r) => groupLabel(r.customerGroup, "All customers") },
  { key: "itemGroup", header: "Item group", mono: true, render: (r) => groupLabel(r.itemGroup, "All items") },
  { key: "basis", header: "Basis", render: (r) => COMMISSION_BASIS_LABELS[r.basis] },
  { key: "ratePct", header: "Rate", mono: true, align: "end", render: (r) => `${r.ratePct}%` },
];

/** One sales group: its reps and shares, and the commission rules resolved specific to general (BR-26.12). */
export function SalesGroupDetailPage() {
  const { id = "" } = useParams();
  const group = useLoad((signal) => getSalesGroup(id, signal), id);
  const rules = useLoad((signal) => listCommissionRules(id, signal), id);
  const [draft, setDraft] = useState<RuleDraft>(emptyRule);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState("");
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<RuleDraft>) {
    setDraft((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function addRule(event: FormEvent) {
    event.preventDefault();
    const found = validateRule(draft);
    setErrors(found);
    setFailure(null);
    setSaved("");
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const rule = await createCommissionRule(
        { salesGroupId: id, customerGroup: draft.customerGroup.trim(), itemGroup: draft.itemGroup.trim(), basis: draft.basis, ratePct: draft.ratePct },
        key.current,
      );
      setSaved(`Rule added: ${rule.ratePct}% for ${groupLabel(rule.customerGroup, "all customers")} and ${groupLabel(rule.itemGroup, "all items")}.`);
      setDraft(emptyRule);
      key.current = newKey();
      rules.reload();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  if (group.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Sales group" />
        <Alert tone="danger" title="Could not load the sales group" live="alert" action={<Button size="sm" onClick={group.reload}>Try again</Button>}>
          <ProblemMessage problem={group.error} />
        </Alert>
      </div>
    );
  }
  const g = group.data;
  if (!g) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Sales group" />
        <span className="visually-hidden">Loading the sales group…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <PageHeader
        title={g.name}
        description={`Legal entity ${g.legalEntityId}`}
        actions={
          <>
            <Link className={buttonClassName("secondary")} to={`/pricing/commission/transactions?salesGroupId=${g.id}`}>
              Transactions
            </Link>
            <Link className={buttonClassName("ghost")} to="/pricing/commission/sales-groups">
              All sales groups
            </Link>
          </>
        }
      />
      <Card title="Reps">
        <div className={styles.stack}>
          <DataTable label="Reps and shares" columns={repColumns} rows={g.reps} getRowKey={(r) => r.repId} />
          <p className={styles.note}>Each rep's share is rounded half to even; the first rep takes the remaining cent.</p>
        </div>
      </Card>
      <Card title="Commission rules">
        <div className={styles.stack}>
          {rules.error ? (
            <Alert tone="danger" title="Could not load the rules" live="alert" action={<Button size="sm" onClick={rules.reload}>Try again</Button>}>
              <ProblemMessage problem={rules.error} />
            </Alert>
          ) : (
            <DataTable
              label="Commission rules"
              columns={ruleColumns}
              rows={rules.data ?? []}
              getRowKey={(r) => r.id}
              loading={rules.loading}
              empty={{ title: "No rules yet", description: "Without a rule, invoices for this group are refused with PRC-0013. Add a rule for all customers and items as the default." }}
            />
          )}
          <p className={styles.note}>
            An invoice line uses the first rule that matches: customer group and item group, then customer group, then item group, then the rule for all.
          </p>
        </div>
      </Card>
      <Card title="Add a rule">
        <form className={styles.form} onSubmit={addRule} noValidate>
          <FormField id="cr-customer-group" label="Customer commission group" optional hint="Leave empty for all customers." error={errors.customerGroup}>
            {(c) => <Input {...c} className={styles.mono} value={draft.customerGroup} onChange={(e) => update({ customerGroup: e.target.value })} invalid={Boolean(errors.customerGroup)} />}
          </FormField>
          <FormField id="cr-item-group" label="Item commission group" optional hint="Leave empty for all items." error={errors.itemGroup}>
            {(c) => <Input {...c} className={styles.mono} value={draft.itemGroup} onChange={(e) => update({ itemGroup: e.target.value })} invalid={Boolean(errors.itemGroup)} />}
          </FormField>
          <FormField id="cr-basis" label="Basis" hint="Margin needs the line cost from the invoice." error={errors.basis}>
            {(c) => (
              <Select {...c} value={draft.basis} onChange={(e) => update({ basis: e.target.value as CommissionBasis })}>
                {(Object.keys(COMMISSION_BASIS_LABELS) as CommissionBasis[]).map((b) => (
                  <option key={b} value={b}>
                    {COMMISSION_BASIS_LABELS[b]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="cr-rate" label="Rate %" error={errors.ratePct}>
            {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={draft.ratePct} onChange={(e) => update({ ratePct: e.target.value })} invalid={Boolean(errors.ratePct)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not add the rule" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          {saved ? (
            <div className={styles.formActions}>
              <Alert tone="success" live="status">
                {saved}
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Add rule
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}
