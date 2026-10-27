import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  createBehaviorRule,
  describeError,
  listBehaviorRules,
  listCostElements,
  type Behavior,
  type BehaviorRule,
  type CostObjectKind,
} from "./api/client";
import { UUID, validateBehaviorRule, type BehaviorRuleForm } from "./logic";
import styles from "./pages.module.css";
import { ErrorList, formatDateTime, kindLabel, LoadError } from "./parts";
import { useLoad } from "./useLoad";

const KINDS: CostObjectKind[] = ["CostCenter", "InternalOrder", "Product", "Project"];

export function BehaviorBadge({ behavior }: { behavior: string | undefined }) {
  if (!behavior) return <Badge tone="neutral">Unclassified</Badge>;
  return <Badge tone={behavior === "Fixed" ? "info" : "warning"}>{behavior}</Badge>;
}

/**
 * Cost behavior rules (FR-38.16): which journal cost counts as fixed and which as variable. Rules classify
 * lines mirrored after they are saved; entries already in the ledger keep their class.
 */
export function BehaviorRulesPage() {
  const [legalEntityId, setLegalEntityId] = useState("");
  const le = UUID.test(legalEntityId.trim()) ? legalEntityId.trim() : "";
  const rules = useLoad((signal) => listBehaviorRules({ legalEntityId: le || undefined }, signal), le);
  const elements = useLoad((signal) => listCostElements(le || undefined, signal), le);
  const elementCode = (id: string) => {
    const el = (elements.data?.items ?? []).find((e) => e.id === id);
    return el ? `${el.code} ${el.name}` : id;
  };
  const columns: DataTableColumn<BehaviorRule>[] = [
    { key: "costElementId", header: "Cost element", render: (r) => elementCode(r.costElementId) },
    { key: "objectKind", header: "Cost object kind", render: (r) => (r.objectKind ? kindLabel(r.objectKind) : "Every kind") },
    { key: "behavior", header: "Behavior", render: (r) => <BehaviorBadge behavior={r.behavior} /> },
    { key: "effectiveFrom", header: "Effective from", mono: true },
    { key: "createdAt", header: "Saved", render: (r) => formatDateTime(r.createdAt) },
  ];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cost behavior rules"
        description="Journal cost is classified when it is mirrored: a rule for the object's kind beats a rule for every kind, the latest effective date wins, then the element's default applies."
        actions={
          <Link className={buttonClassName("primary")} to="/costacct/behavior-rules/new">
            New behavior rule
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="behavior-legal-entity" label="Legal entity" optional>
          {(c) => <Input {...c} className={styles.mono} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} />}
        </FormField>
      </div>
      {rules.error ? (
        <LoadError title="Could not load behavior rules" error={rules.error} onRetry={rules.reload} />
      ) : (
        <DataTable
          label="Cost behavior rules"
          columns={columns}
          rows={rules.data?.items ?? []}
          getRowKey={(r) => r.id}
          loading={rules.loading && !rules.data}
          empty={{
            title: "No behavior rules yet",
            description: "Without a rule, journal cost takes the element's default behavior, or stays unclassified.",
          }}
        />
      )}
    </div>
  );
}

/** Saves one rule. A second rule for the same element, kind and date is refused (CST-0020). */
export function CreateBehaviorRulePage() {
  const navigate = useNavigate();
  const [form, setForm] = useState<BehaviorRuleForm>({ legalEntityId: "", costElementId: "", objectKind: "", behavior: "Fixed", effectiveFrom: "" });
  const [errors, setErrors] = useState<Partial<Record<keyof BehaviorRuleForm, string>>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });
  const le = UUID.test(form.legalEntityId.trim()) ? form.legalEntityId.trim() : "";
  const elements = useLoad((signal) => (le ? listCostElements(le, signal) : Promise.resolve({ items: [] })), le);

  async function submit(e: FormEvent) {
    e.preventDefault();
    const clean = { ...form, legalEntityId: form.legalEntityId.trim(), effectiveFrom: form.effectiveFrom.trim() };
    const found = validateBehaviorRule(clean);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const input = {
      legalEntityId: clean.legalEntityId,
      costElementId: clean.costElementId,
      behavior: clean.behavior,
      effectiveFrom: clean.effectiveFrom,
      ...(clean.objectKind ? { objectKind: clean.objectKind } : {}),
    };
    const body = JSON.stringify(input);
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      await createBehaviorRule(input, key.current.value);
      navigate("/costacct/behavior-rules");
    } catch (err) {
      setFailure(describeError(err, "The behavior rule could not be saved."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New behavior rule"
        description="The rule classifies journal lines mirrored from the first period that starts on or after its date. Entries already in the ledger keep their class."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="rule-le" label="Legal entity" error={errors.legalEntityId}>
            {(c) => (
              <Input
                {...c}
                className={styles.mono}
                value={form.legalEntityId}
                invalid={Boolean(errors.legalEntityId)}
                onChange={(e) => setForm({ ...form, legalEntityId: e.target.value, costElementId: "" })}
              />
            )}
          </FormField>
          <FormField id="rule-element" label="Cost element" error={errors.costElementId}>
            {(c) => (
              <Select {...c} value={form.costElementId} invalid={Boolean(errors.costElementId)} onChange={(e) => setForm({ ...form, costElementId: e.target.value })}>
                <option value="">{le ? (elements.loading ? "Loading elements…" : "Choose an element") : "Enter a legal entity first"}</option>
                {(elements.data?.items ?? []).map((el) => (
                  <option key={el.id} value={el.id}>
                    {el.code} {el.name}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="rule-kind" label="Cost object kind" hint="A rule for one kind overrides a rule for every kind.">
            {(c) => (
              <Select {...c} value={form.objectKind} onChange={(e) => setForm({ ...form, objectKind: e.target.value as CostObjectKind | "" })}>
                <option value="">Every kind</option>
                {KINDS.map((k) => (
                  <option key={k} value={k}>
                    {kindLabel(k)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="rule-behavior" label="Behavior">
            {(c) => (
              <Select {...c} value={form.behavior} onChange={(e) => setForm({ ...form, behavior: e.target.value as Behavior })}>
                <option value="Fixed">Fixed</option>
                <option value="Variable">Variable</option>
              </Select>
            )}
          </FormField>
          <FormField id="rule-from" label="Effective from" hint="YYYY-MM-DD" error={errors.effectiveFrom}>
            {(c) => (
              <Input
                {...c}
                className={styles.mono}
                value={form.effectiveFrom}
                invalid={Boolean(errors.effectiveFrom)}
                onChange={(e) => setForm({ ...form, effectiveFrom: e.target.value })}
              />
            )}
          </FormField>
          {elements.error ? <LoadError title="Could not load cost elements" error={elements.error} onRetry={elements.reload} /> : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save behavior rule
            </Button>
            <Link className={buttonClassName("ghost")} to="/costacct/behavior-rules">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      <ErrorList errors={[]} failure={failure} />
    </div>
  );
}
