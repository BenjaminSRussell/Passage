import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { createRule, describeError, listRules, type Rule } from "./api/client";
import { UUID, validateRule, type RuleErrors, type RuleForm } from "./logic";
import styles from "./pages.module.css";
import { LoadError } from "./parts";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Rule>[] = [
  { key: "code", header: "Rule", mono: true },
  { key: "version", header: "Version", align: "end" },
  { key: "warnPct", header: "Warn %", align: "end", mono: true },
  { key: "blockPct", header: "Block %", align: "end", mono: true },
  { key: "currency", header: "Currency", mono: true },
  { key: "effectiveFrom", header: "Effective from", mono: true },
  { key: "legalEntityId", header: "Legal entity", mono: true },
];

/** Budgeting workspace: budget control rule versions (FR-38.3). Each version owns its balances and checks. */
export function RulesPage() {
  const navigate = useNavigate();
  const [legalEntityId, setLegalEntityId] = useState("");
  const filter = UUID.test(legalEntityId.trim()) ? legalEntityId.trim() : "";
  const state = useLoad((signal) => listRules(filter || undefined, signal), filter);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Budget control rules"
        description="Each rule version sets the warn and block thresholds that budget checks apply. Open a version to see its available funds, register entries, encumbrances and checks."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/budgeting/check">
              Run a budget check
            </Link>
            <Link className={buttonClassName("primary")} to="/budgeting/rules/new">
              New rule version
            </Link>
          </>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="rules-legal-entity" label="Legal entity" optional hint="Paste a legal entity id to narrow the list.">
          {(c) => <Input {...c} className={styles.mono} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} />}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load budget control rules" error={state.error} onRetry={state.reload} />
      ) : (
        <DataTable
          label="Budget control rules"
          columns={columns}
          rows={state.data?.items ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading && !state.data}
          empty={{ title: "No budget control rules yet", description: "Create a rule version, then post an Original budget to it." }}
          onRowOpen={(row) => navigate(`/budgeting/rules/${row.id}`)}
        />
      )}
    </div>
  );
}

const blank: RuleForm = { legalEntityId: "", code: "", warnPct: "0", blockPct: "0", currency: "USD", effectiveFrom: new Date().toISOString().slice(0, 10) };

/** Creates the next version of a rule code (FR-38.3). Versions are immutable once stored. */
export function CreateRulePage() {
  const navigate = useNavigate();
  const [form, setForm] = useState<RuleForm>(blank);
  const [errors, setErrors] = useState<RuleErrors>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });

  function set<K extends keyof RuleForm>(field: K, value: RuleForm[K]) {
    setForm((f) => ({ ...f, [field]: value }));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    const trimmed = { ...form, legalEntityId: form.legalEntityId.trim(), code: form.code.trim() };
    const found = validateRule(trimmed);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const body = JSON.stringify(trimmed);
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      const rule = await createRule(trimmed, key.current.value);
      navigate(`/budgeting/rules/${rule.id}`);
    } catch (err) {
      setFailure(describeError(err, "The rule could not be saved."));
    } finally {
      setBusy(false);
    }
  }

  const field = (id: keyof RuleForm, label: string, hint?: string, mono = false) => (
    <FormField id={`rule-${id}`} label={label} hint={hint} error={errors[id]}>
      {(c) => <Input {...c} className={mono ? styles.mono : undefined} value={form[id]} invalid={Boolean(errors[id])} onChange={(e) => set(id, e.target.value)} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New rule version" description="A new version of an existing code takes the next version number; earlier versions keep their history." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {field("legalEntityId", "Legal entity", undefined, true)}
          {field("code", "Rule code", "For example DEPT or CAPEX.", true)}
          {field("warnPct", "Warn threshold %", "Over-budget percentage that still passes.")}
          {field("blockPct", "Block threshold %", "Above this the check fails.")}
          {field("currency", "Currency", undefined, true)}
          <FormField id="rule-effectiveFrom" label="Effective from" error={errors.effectiveFrom}>
            {(c) => <Input {...c} type="date" value={form.effectiveFrom} invalid={Boolean(errors.effectiveFrom)} onChange={(e) => set("effectiveFrom", e.target.value)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save rule version
            </Button>
            <Link className={buttonClassName("ghost")} to="/budgeting">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      <Alert tone="danger" live="alert">
        {failure}
      </Alert>
    </div>
  );
}
