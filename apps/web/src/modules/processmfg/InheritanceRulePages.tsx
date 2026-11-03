import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  type DataTableColumn,
} from "../../shell/ui";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import {
  createInheritanceRule,
  describeProblem,
  getInheritanceRule,
  listInheritanceRules,
  newKey,
  retireInheritanceRule,
  type InheritanceRule,
  type NewInheritanceRule,
  type Problem,
  type RuleAggregation,
} from "./api/client";
import { dash, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Duty that defines and retires inheritance rules; enforced by the service (FR-24.15). */
export const INHERITANCE_RULE_DUTY = "production.inheritance-rule.write";

const NUMBER = /^-?\d{1,12}(\.\d{1,6})?$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

const AGGREGATIONS: { value: RuleAggregation; label: string }[] = [
  { value: "copy", label: "Copy (senders must agree)" },
  { value: "min", label: "Lowest sender value" },
  { value: "max", label: "Highest sender value" },
  { value: "weighted-average", label: "Weighted average by quantity" },
  { value: "earliest-date", label: "Earliest date" },
  { value: "latest-date", label: "Latest date" },
  { value: "fixed", label: "Fixed value" },
];

function aggregationText(a: RuleAggregation): string {
  return AGGREGATIONS.find((x) => x.value === a)?.label ?? a;
}

function fixedText(r: InheritanceRule): string {
  return r.fixedNum ?? r.fixedText ?? r.fixedDate ?? dash;
}

function StatusBadge({ rule }: { rule: InheritanceRule }) {
  return rule.retiredAt ? <Badge tone="neutral">Retired</Badge> : <Badge tone="success">Active</Badge>;
}

function LoadError({ title, error, reload }: { title: string; error: Problem; reload: () => void }) {
  return (
    <Alert tone="danger" title={title} live="alert" action={<Button size="sm" onClick={reload}>Try again</Button>}>
      {error.message} ({error.code})
    </Alert>
  );
}

const columns: DataTableColumn<InheritanceRule>[] = [
  { key: "itemId", header: "Item", mono: true, render: (r) => shortId(r.itemId) },
  { key: "attribute", header: "Attribute" },
  { key: "aggregation", header: "Derived by", render: (r) => aggregationText(r.aggregation) },
  { key: "fixed", header: "Fixed value", render: fixedText },
  { key: "retiredAt", header: "Status", render: (r) => <StatusBadge rule={r} /> },
];

/** FR-24.15: the inheritance rules, filtered by item. */
export function InheritanceRulesPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), INHERITANCE_RULE_DUTY);
  const [itemId, setItemId] = useState("");
  const [activeOnly, setActiveOnly] = useState(false);
  const item = itemId.trim();
  const filterError = item !== "" && !UUID.test(item);
  const state = useLoad(
    (signal) => (filterError ? Promise.resolve([]) : listInheritanceRules({ itemId: item, active: activeOnly }, signal)),
    `rules|${item}|${activeOnly}|${filterError}`,
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Inheritance rules"
        description="How a receiver batch gets its attribute values from the batches it was made from: copied, lowest, highest, a quantity-weighted average, the earliest or latest date, or a fixed value. One active rule per item and attribute."
        actions={
          canWrite ? (
            <Link className={buttonClassName("primary")} to="/processmfg/inheritance-rules/new">
              Define rule
            </Link>
          ) : undefined
        }
      />
      <div className={styles.toolbar}>
        <FormField id="rule-filter-item" label="Item id" error={filterError ? "Enter the item id as a UUID." : undefined}>
          {(c) => <Input {...c} value={itemId} onChange={(e) => setItemId(e.target.value)} invalid={filterError} />}
        </FormField>
        <div className={styles.lineChecks}>
          <label htmlFor="rule-active-only">
            <input id="rule-active-only" type="checkbox" checked={activeOnly} onChange={(e) => setActiveOnly(e.target.checked)} /> Active rules only
          </label>
        </div>
      </div>
      {state.error ? (
        <LoadError title="Could not load the rules" error={state.error} reload={state.reload} />
      ) : (
        <DataTable
          label="Inheritance rules"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(r) => r.id}
          loading={state.loading}
          empty={
            item || activeOnly
              ? { title: "No rules match", description: "Clear the filters to see every rule." }
              : { title: "No inheritance rules yet", description: "Without a rule, a derived batch gets no attribute values from its senders." }
          }
          onRowOpen={(r) => navigate(`/processmfg/inheritance-rules/${r.id}`)}
        />
      )}
    </div>
  );
}

/** Defines an inheritance rule. */
export function NewInheritanceRulePage() {
  const navigate = useNavigate();
  const allowed = holdsDuty(usePrincipalDuties(), INHERITANCE_RULE_DUTY);
  const [itemId, setItemId] = useState("");
  const [attribute, setAttribute] = useState("");
  const [aggregation, setAggregation] = useState<RuleAggregation>("copy");
  const [decimals, setDecimals] = useState("");
  const [fixedKind, setFixedKind] = useState<"num" | "text" | "date">("num");
  const [fixed, setFixed] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function on<T>(set: (v: T) => void) {
    return (v: T) => {
      set(v);
      key.current = newKey();
    };
  }

  if (!allowed) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Define rule" />
        <Card title="Inheritance rule">
          <p className={styles.note}>Defining rules needs the inheritance rule duty. Ask your administrator if you should have it.</p>
        </Card>
      </div>
    );
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!UUID.test(itemId.trim())) found.itemId = "Enter the item id as a UUID.";
    const name = attribute.trim();
    if (name === "" || name.length > 40) found.attribute = "Enter the attribute, at most 40 characters.";
    const d = decimals.trim();
    if (aggregation === "weighted-average" && d !== "" && !/^[0-6]$/.test(d)) found.decimals = "Enter 0 to 6 decimals.";
    const f = fixed.trim();
    if (aggregation === "fixed") {
      if (f === "") found.fixed = "Enter the fixed value.";
      else if (fixedKind === "num" && !NUMBER.test(f)) found.fixed = "Enter a number with at most 6 decimals.";
      else if (fixedKind === "date" && !DATE.test(f)) found.fixed = "Enter a date as yyyy-mm-dd.";
      else if (fixedKind === "text" && f.length > 200) found.fixed = "At most 200 characters.";
    }
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const input: NewInheritanceRule = {
      itemId: itemId.trim(),
      attribute: name,
      aggregation,
      ...(aggregation === "weighted-average" && d !== "" ? { decimals: Number(d) } : {}),
      ...(aggregation === "fixed" ? { fixed: { [fixedKind]: f } } : {}),
    };
    setBusy(true);
    try {
      const rule = await createInheritanceRule(input, key.current);
      navigate(`/processmfg/inheritance-rules/${rule.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      // A refused command can be corrected and retried under a new key; an answer lost in transit is retried under the same one.
      if (problem.code !== "COM-0500") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Define rule" description="The item must already define the attribute, and the way of deriving it must fit its kind: lowest, highest and average need a number, earliest and latest a date. A rule is never edited: retire it and define its replacement." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="rule-item" label="Item id" error={errors.itemId}>
            {(c) => <Input {...c} value={itemId} onChange={(e) => on(setItemId)(e.target.value)} invalid={Boolean(errors.itemId)} />}
          </FormField>
          <FormField id="rule-attribute" label="Attribute" hint="The receiver attribute the rule fills." error={errors.attribute}>
            {(c) => <Input {...c} value={attribute} onChange={(e) => on(setAttribute)(e.target.value)} invalid={Boolean(errors.attribute)} />}
          </FormField>
          <FormField id="rule-aggregation" label="Derived by">
            {(c) => (
              <Select {...c} value={aggregation} onChange={(e) => on(setAggregation)(e.target.value as RuleAggregation)}>
                {AGGREGATIONS.map((a) => (
                  <option key={a.value} value={a.value}>
                    {a.label}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {aggregation === "weighted-average" ? (
            <FormField id="rule-decimals" label="Decimals" hint="Rounding of the average, 0 to 6; empty for 4." error={errors.decimals}>
              {(c) => <Input {...c} inputMode="numeric" value={decimals} onChange={(e) => on(setDecimals)(e.target.value)} invalid={Boolean(errors.decimals)} />}
            </FormField>
          ) : null}
          {aggregation === "fixed" ? (
            <>
              <FormField id="rule-fixed-kind" label="Fixed value is">
                {(c) => (
                  <Select {...c} value={fixedKind} onChange={(e) => on(setFixedKind)(e.target.value as typeof fixedKind)}>
                    <option value="num">A number</option>
                    <option value="text">Text or an enum option</option>
                    <option value="date">A date</option>
                  </Select>
                )}
              </FormField>
              <FormField id="rule-fixed" label="Fixed value" error={errors.fixed}>
                {(c) => <Input {...c} value={fixed} onChange={(e) => on(setFixed)(e.target.value)} invalid={Boolean(errors.fixed)} />}
              </FormField>
            </>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not define the rule" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Define rule
            </Button>
            <Link className={buttonClassName("ghost")} to="/processmfg/inheritance-rules">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

/** One rule, with retire for a holder of the duty. */
export function InheritanceRuleDetailPage() {
  const { id = "" } = useParams();
  const canWrite = holdsDuty(usePrincipalDuties(), INHERITANCE_RULE_DUTY);
  const state = useLoad((signal) => getInheritanceRule(id, signal), id);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Inheritance rule" />
        <LoadError title="Could not load the rule" error={state.error} reload={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Inheritance rule" />
        <span className="visually-hidden">Loading the rule…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const rule = state.data;

  async function retire() {
    setBusy(true);
    setFailure(null);
    try {
      await retireInheritanceRule(rule.id, rule.version, key.current);
      key.current = newKey();
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code !== "COM-0500") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title={rule.attribute} description={`Item ${rule.itemId}`} meta={<StatusBadge rule={rule} />} />
      <StatGroup label="Rule">
        <StatTile label="Derived by" value={aggregationText(rule.aggregation)} />
        <StatTile label="Decimals" value={rule.decimals === undefined ? dash : String(rule.decimals)} />
        <StatTile label="Fixed value" value={fixedText(rule)} />
        <StatTile label="Version" value={String(rule.version)} />
      </StatGroup>
      {rule.retiredAt ? (
        <Alert tone="warning" title="This rule is retired" live="status">
          Retired by {rule.retiredBy ?? "unknown"}; later derivations ignore it, batches derived earlier keep their values.
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="Could not retire the rule" live="alert" action={failure.code === "PMF-0104" ? <Button size="sm" onClick={state.reload}>Reload</Button> : undefined}>
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <div className={styles.formActions}>
        {canWrite && !rule.retiredAt ? (
          <Button variant="danger" onClick={retire} loading={busy}>
            Retire rule
          </Button>
        ) : null}
        <Link className={buttonClassName("ghost")} to="/processmfg/inheritance-rules">
          Back to the list
        </Link>
      </div>
    </div>
  );
}
