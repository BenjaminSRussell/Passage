import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { createCostElement, describeError, listCostElements, type CostElement, type CostElementKind } from "./api/client";
import { UUID, validateAccountRange, validateCodeForm, type CodeForm } from "./logic";
import styles from "./pages.module.css";
import { dash, ErrorList, LoadError } from "./parts";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<CostElement>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "kind", header: "Kind", render: (e) => <Badge tone={e.kind === "Primary" ? "info" : "neutral"}>{e.kind}</Badge> },
  { key: "range", header: "Main accounts", mono: true, render: (e) => (e.mainAccountFrom ? `${e.mainAccountFrom}..${e.mainAccountTo}` : dash) },
  { key: "legalEntityId", header: "Legal entity", mono: true },
];

/** Cost elements (FR-38.6): primary elements map main account ranges; secondary elements carry allocations. */
export function CostElementsPage() {
  const [legalEntityId, setLegalEntityId] = useState("");
  const le = UUID.test(legalEntityId.trim()) ? legalEntityId.trim() : "";
  const state = useLoad((signal) => listCostElements(le || undefined, signal), le);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cost elements"
        description="Primary elements decide which journal lines enter the cost ledger. Secondary elements carry the cost that allocation runs move."
        actions={
          <Link className={buttonClassName("primary")} to="/costacct/cost-elements/new">
            New cost element
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="elements-legal-entity" label="Legal entity" optional>
          {(c) => <Input {...c} className={styles.mono} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} />}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load cost elements" error={state.error} onRetry={state.reload} />
      ) : (
        <DataTable
          label="Cost elements"
          columns={columns}
          rows={state.data?.items ?? []}
          getRowKey={(e) => e.id}
          loading={state.loading && !state.data}
          empty={{ title: "No cost elements yet", description: "Map a main account range to a primary element so journal lines can mirror." }}
        />
      )}
    </div>
  );
}

/** Creates a primary or secondary cost element. Primary ranges may not overlap (CST-0009). */
export function CreateCostElementPage() {
  const navigate = useNavigate();
  const [form, setForm] = useState<CodeForm & { kind: CostElementKind; from: string; to: string }>({
    legalEntityId: "",
    code: "",
    name: "",
    kind: "Primary",
    from: "",
    to: "",
  });
  const [errors, setErrors] = useState<Partial<Record<keyof CodeForm | "range", string>>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });

  async function submit(e: FormEvent) {
    e.preventDefault();
    const base = { legalEntityId: form.legalEntityId.trim(), code: form.code.trim(), name: form.name.trim() };
    const found: typeof errors = validateCodeForm(base);
    const primary = form.kind === "Primary";
    if (primary) {
      const rangeError = validateAccountRange(form.from.trim(), form.to.trim());
      if (rangeError) found.range = rangeError;
    }
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const input = primary ? { ...base, kind: form.kind, mainAccountFrom: form.from.trim(), mainAccountTo: form.to.trim() } : { ...base, kind: form.kind };
    const body = JSON.stringify(input);
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      await createCostElement(input, key.current.value);
      navigate("/costacct/cost-elements");
    } catch (err) {
      setFailure(describeError(err, "The cost element could not be saved."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New cost element" />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="element-le" label="Legal entity" error={errors.legalEntityId}>
            {(c) => <Input {...c} className={styles.mono} value={form.legalEntityId} invalid={Boolean(errors.legalEntityId)} onChange={(e) => setForm({ ...form, legalEntityId: e.target.value })} />}
          </FormField>
          <FormField id="element-kind" label="Kind">
            {(c) => (
              <Select {...c} value={form.kind} onChange={(e) => setForm({ ...form, kind: e.target.value as CostElementKind })}>
                <option value="Primary">Primary (mirrors main accounts)</option>
                <option value="Secondary">Secondary (carries allocations)</option>
              </Select>
            )}
          </FormField>
          <FormField id="element-code" label="Code" error={errors.code}>
            {(c) => <Input {...c} className={styles.mono} value={form.code} invalid={Boolean(errors.code)} onChange={(e) => setForm({ ...form, code: e.target.value })} />}
          </FormField>
          <FormField id="element-name" label="Name" error={errors.name}>
            {(c) => <Input {...c} value={form.name} invalid={Boolean(errors.name)} onChange={(e) => setForm({ ...form, name: e.target.value })} />}
          </FormField>
          {form.kind === "Primary" ? (
            <>
              <FormField id="element-from" label="Main account from" error={errors.range}>
                {(c) => <Input {...c} className={styles.mono} value={form.from} invalid={Boolean(errors.range)} onChange={(e) => setForm({ ...form, from: e.target.value })} />}
              </FormField>
              <FormField id="element-to" label="Main account to" hint="Inclusive. Accounts compare as text, so use fixed-width numbers.">
                {(c) => <Input {...c} className={styles.mono} value={form.to} onChange={(e) => setForm({ ...form, to: e.target.value })} />}
              </FormField>
            </>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save cost element
            </Button>
            <Link className={buttonClassName("ghost")} to="/costacct/cost-elements">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      <ErrorList errors={[]} failure={failure} />
    </div>
  );
}
