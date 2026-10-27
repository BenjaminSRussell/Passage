import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  CostingApiError,
  createValuationCategory,
  createValuationLedger,
  describeError,
  listValuationCategories,
  listValuationLedgers,
  type RateType,
  type ValuationCategory,
  type ValuationCategoryRequest,
  type ValuationLedger,
  type ValuationLedgerRequest,
  type ValuationType,
} from "./api/client";
import styles from "./pages.module.css";
import { CURRENCY, MethodField } from "./shared";
import { useLoad } from "./useLoad";
import { LoadError, UUID } from "./versionParts";

export const SETUP_CODE = /^[A-Z0-9][A-Z0-9_-]{1,31}$/;
const RATE_TYPES: RateType[] = ["SPOT", "AVERAGE", "HISTORICAL"];
const CODE_HINT = "2 to 32 capital letters, digits, _ or -.";

/** The legal entity filter of a list page: the typed text, and the id to filter by once it is a UUID (or empty). */
export function useLegalEntityFilter() {
  const [params, setParams] = useSearchParams();
  const [text, setText] = useState(params.get("legalEntityId") ?? "");
  const trimmed = text.trim();
  const valid = trimmed === "" || UUID.test(trimmed);
  function change(value: string) {
    setText(value);
    const v = value.trim();
    if (v === "" || UUID.test(v)) setParams(v === "" ? {} : { legalEntityId: v }, { replace: true });
  }
  return { text, change, applied: valid ? trimmed.toLowerCase() : (params.get("legalEntityId") ?? ""), error: valid ? undefined : "Enter a legal entity id (a UUID), or leave it empty for all." };
}

export function LegalEntityFilterField({ id, filter }: { id: string; filter: ReturnType<typeof useLegalEntityFilter> }) {
  return (
    <FormField id={id} label="Legal entity" optional hint="Empty shows every legal entity you can see." error={filter.error}>
      {(c) => <Input {...c} className={styles.mono} value={filter.text} invalid={Boolean(filter.error)} onChange={(e) => filter.change(e.target.value)} />}
    </FormField>
  );
}

const ledgerColumns: DataTableColumn<ValuationLedger>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "primary", header: "Role", render: (row) => (row.primary ? <Badge tone="success">Primary</Badge> : <Badge tone="neutral">Additional</Badge>) },
  { key: "method", header: "Costing method", mono: true },
  { key: "currency", header: "Currency", mono: true },
  { key: "rateType", header: "Rate type" },
  { key: "legalEntityId", header: "Legal entity", mono: true },
  { key: "ledgerGroupId", header: "Ledger group", mono: true },
];

export function typeSummary(types: ValuationType[]): string {
  return types.map((t) => `${t.code} → ${t.account}`).join(", ");
}

const categoryColumns: DataTableColumn<ValuationCategory>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "batch", header: "Kind", render: (row) => (row.batch ? "Batch: each batch is its own type" : "Split by type") },
  { key: "types", header: "Valuation types and accounts", render: (row) => (row.batch ? "Created at batch receipt" : typeSummary(row.types)) },
];

/** Valuation setup (FR-20.15, FR-20.16): parallel valuation ledgers per legal entity and the tenant's valuation categories. */
export function ValuationSetupPage() {
  const filter = useLegalEntityFilter();
  const ledgers = useLoad((signal) => listValuationLedgers(filter.applied, signal), filter.applied);
  const categories = useLoad((signal) => listValuationCategories(signal), "categories");

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Valuation setup"
        description="Valuation ledgers value the same stock under their own method and currency for a ledger group. Valuation categories split a product's stock into valuation types, each with its inventory account."
        actions={
          <>
            <Link className={buttonClassName("primary")} to="/costing/valuation-setup/ledgers/new">
              New valuation ledger
            </Link>
            <Link className={buttonClassName("secondary")} to="/costing/valuation-setup/categories/new">
              New valuation category
            </Link>
          </>
        }
      />
      <Card title="Valuation ledgers" headingLevel={2}>
        <div className={styles.stack}>
          <div className={styles.toolbar}>
            <LegalEntityFilterField id="ledgers-le" filter={filter} />
          </div>
          {ledgers.error ? (
            <LoadError title="Could not load the valuation ledgers" error={ledgers.error} onRetry={ledgers.reload} />
          ) : (
            <DataTable
              label="Valuation ledgers"
              columns={ledgerColumns}
              rows={ledgers.data?.items ?? []}
              getRowKey={(row) => row.id}
              loading={ledgers.loading && !ledgers.data}
              empty={{ title: "No valuation ledgers yet", description: "Add the primary ledger of a legal entity first, such as IFRS in EUR, then a ledger per additional ledger group." }}
            />
          )}
        </div>
      </Card>
      <Card title="Valuation categories" headingLevel={2}>
        {categories.error ? (
          <LoadError title="Could not load the valuation categories" error={categories.error} onRetry={categories.reload} />
        ) : (
          <DataTable
            label="Valuation categories"
            columns={categoryColumns}
            rows={categories.data?.items ?? []}
            getRowKey={(row) => row.id}
            loading={categories.loading && !categories.data}
            empty={{ title: "No valuation categories yet", description: "Add a category such as ORIGIN with the types DOMESTIC and IMPORT, or a BATCH category." }}
          />
        )}
      </Card>
    </div>
  );
}

type LedgerErrors = Partial<Record<keyof ValuationLedgerRequest, string>>;

/** Validates the ledger form as the service does. */
export function validateLedger(v: ValuationLedgerRequest): LedgerErrors {
  const errors: LedgerErrors = {};
  if (!UUID.test(v.legalEntityId)) errors.legalEntityId = "Enter the legal entity's id (a UUID).";
  if (!UUID.test(v.ledgerGroupId)) errors.ledgerGroupId = "Enter the ledger group's id (a UUID).";
  if (!SETUP_CODE.test(v.code)) errors.code = `Use ${CODE_HINT} For example IFRS.`;
  if (!CURRENCY.test(v.currency)) errors.currency = "Use a three-letter ISO 4217 code, such as EUR.";
  return errors;
}

function Back() {
  return (
    <Link className={buttonClassName("ghost")} to="/costing/valuation-setup">
      Back to valuation setup
    </Link>
  );
}

/** Register a valuation ledger bound to a ledger group (FR-20.15). */
export function CreateLedgerPage() {
  const navigate = useNavigate();
  const [form, setForm] = useState<ValuationLedgerRequest>({ legalEntityId: "", ledgerGroupId: "", code: "", method: "FIFO", currency: "EUR", rateType: "AVERAGE", primary: false });
  const [errors, setErrors] = useState<LedgerErrors>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  function set<K extends keyof ValuationLedgerRequest>(field: K, value: ValuationLedgerRequest[K]) {
    setForm((f) => ({ ...f, [field]: value }));
  }

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setFailure(null);
    const body = { ...form, legalEntityId: form.legalEntityId.trim(), ledgerGroupId: form.ledgerGroupId.trim(), code: form.code.trim() };
    const found = validateLedger(body);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const ledger = await createValuationLedger(body);
      navigate(`/costing/valuation-setup?legalEntityId=${ledger.legalEntityId}`);
    } catch (err) {
      if (err instanceof CostingApiError && err.problem.errors) setErrors(err.problem.errors as LedgerErrors);
      setFailure(describeError(err, "The valuation ledger could not be created."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New valuation ledger" description="Each ledger group of a legal entity takes one valuation ledger. Only one ledger is primary: its results adjust the inventory transactions." actions={<Back />} />
      <Card>
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <FormField id="ledger-le" label="Legal entity" error={errors.legalEntityId}>
            {(c) => <Input {...c} className={styles.mono} value={form.legalEntityId} invalid={Boolean(errors.legalEntityId)} onChange={(e) => set("legalEntityId", e.target.value)} />}
          </FormField>
          <FormField id="ledger-group" label="Ledger group" hint="The general ledger group this valuation posts to." error={errors.ledgerGroupId}>
            {(c) => <Input {...c} className={styles.mono} value={form.ledgerGroupId} invalid={Boolean(errors.ledgerGroupId)} onChange={(e) => set("ledgerGroupId", e.target.value)} />}
          </FormField>
          <FormField id="ledger-code" label="Code" hint={CODE_HINT} error={errors.code}>
            {(c) => <Input {...c} value={form.code} invalid={Boolean(errors.code)} onChange={(e) => set("code", e.target.value.toUpperCase())} />}
          </FormField>
          <MethodField id="ledger-method" value={form.method} onChange={(m) => set("method", m)} />
          <FormField id="ledger-currency" label="Currency" error={errors.currency}>
            {(c) => <Input {...c} value={form.currency} maxLength={3} invalid={Boolean(errors.currency)} onChange={(e) => set("currency", e.target.value.toUpperCase())} />}
          </FormField>
          <FormField id="ledger-rate" label="Exchange rate type" hint="Rate used to convert receipts into the ledger currency.">
            {(c) => (
              <Select {...c} value={form.rateType} onChange={(e) => set("rateType", e.target.value as RateType)}>
                {RATE_TYPES.map((r) => (
                  <option key={r} value={r}>
                    {r}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <span className={styles.check}>
            <Input id="ledger-primary" type="checkbox" checked={form.primary} onChange={(e) => set("primary", e.target.checked)} />
            <label htmlFor="ledger-primary">Primary ledger</label>
          </span>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create ledger
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" live="alert">
        {failure}
      </Alert>
    </div>
  );
}

/** Parses "DOMESTIC, 1300" lines into valuation types; returns a message instead of throwing. */
export function parseTypeLines(text: string): { ok: true; value: ValuationType[] } | { ok: false; error: string } {
  const value: ValuationType[] = [];
  const seen = new Set<string>();
  const lines = text.split(/\r?\n/).map((l, i) => ({ line: i + 1, text: l.trim() }));
  for (const { line, text: l } of lines) {
    if (l === "") continue;
    const [code = "", ...rest] = l.split(",").map((p) => p.trim());
    const account = rest.join(",").trim();
    if (!SETUP_CODE.test(code)) return { ok: false, error: `Line ${line}: the type code must be ${CODE_HINT}` };
    if (account === "" || account.length > 64) return { ok: false, error: `Line ${line}: give the inventory account after a comma, such as ${code}, 1300.` };
    if (seen.has(code)) return { ok: false, error: `Line ${line}: ${code} is already listed.` };
    seen.add(code);
    value.push({ code, account });
  }
  return { ok: true, value };
}

/** Register a valuation category (FR-20.16, BR-20.14). */
export function CreateCategoryPage() {
  const navigate = useNavigate();
  const [code, setCode] = useState("");
  const [batch, setBatch] = useState(false);
  const [types, setTypes] = useState("DOMESTIC, 1300\nIMPORT, 1301");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setFailure(null);
    const found: Record<string, string> = {};
    const trimmed = code.trim();
    if (!SETUP_CODE.test(trimmed)) found.code = `Use ${CODE_HINT} For example ORIGIN.`;
    const parsed: ReturnType<typeof parseTypeLines> = batch ? { ok: true, value: [] } : parseTypeLines(types);
    if (!parsed.ok) found.types = parsed.error;
    else if (!batch && parsed.value.length === 0) found.types = "List at least one valuation type, or make it a batch category.";
    setErrors(found);
    if (Object.keys(found).length > 0 || !parsed.ok) return;
    const body: ValuationCategoryRequest = { code: trimmed, batch, types: parsed.value };
    setBusy(true);
    try {
      await createValuationCategory(body);
      navigate("/costing/valuation-setup");
    } catch (err) {
      if (err instanceof CostingApiError && err.problem.errors) setErrors(err.problem.errors);
      setFailure(describeError(err, "The valuation category could not be created."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New valuation category" description="Products in a split category keep stock, costs and value per valuation type. Set the category when you create the product: it cannot change while stock exists." actions={<Back />} />
      <Card>
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <FormField id="category-code" label="Code" hint={CODE_HINT} error={errors.code}>
            {(c) => <Input {...c} value={code} invalid={Boolean(errors.code)} onChange={(e) => setCode(e.target.value.toUpperCase())} />}
          </FormField>
          <span className={styles.check}>
            <Input id="category-batch" type="checkbox" checked={batch} onChange={(e) => setBatch(e.target.checked)} />
            <label htmlFor="category-batch">Batch valuation</label>
          </span>
          {batch ? (
            <p className={`${styles.note} ${styles.wide}`}>Each batch becomes its own valuation type at receipt, valued at the receipt cost, so a batch category lists no types.</p>
          ) : (
            <div className={styles.wide}>
              <FormField id="category-types" label="Valuation types" hint="One type per line: the type code, a comma, then its inventory account." error={errors.types}>
                {(c) => <Textarea {...c} mono rows={5} value={types} invalid={Boolean(errors.types)} onChange={(e) => setTypes(e.target.value)} />}
              </FormField>
            </div>
          )}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create category
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" live="alert">
        {failure}
      </Alert>
    </div>
  );
}
