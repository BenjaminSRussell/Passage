import { useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { COST_KINDS, describeError, listCostTransactions, type CostKind, type CostTransaction, type CostTransactionFilter } from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { dash, formatDateTime, LoadError, UUID } from "./versionParts";

const DAY = /^\d{4}-\d{2}-\d{2}$/;
const FIELDS = ["legalEntityId", "valuationLedgerId", "productId", "siteId", "kind", "asOf"] as const;
type Field = (typeof FIELDS)[number];

/** The filter the URL asks for, or null while it has no valid legal entity. */
export function filterFromSearch(search: URLSearchParams): CostTransactionFilter | null {
  const legalEntityId = (search.get("legalEntityId") ?? "").toLowerCase();
  if (!UUID.test(legalEntityId)) return null;
  const out: CostTransactionFilter = { legalEntityId };
  for (const f of ["valuationLedgerId", "productId", "siteId"] as const) {
    const v = search.get(f);
    if (v && UUID.test(v)) out[f] = v.toLowerCase();
  }
  const kind = search.get("kind");
  if (kind && (COST_KINDS as readonly string[]).includes(kind)) out.kind = kind as CostKind;
  const asOf = search.get("asOf");
  if (asOf && DAY.test(asOf)) out.asOf = asOf;
  return out;
}

const columns: DataTableColumn<CostTransaction>[] = [
  { key: "financialDate", header: "Financial date", mono: true },
  { key: "kind", header: "Kind", render: (row) => (row.kind === "PPV" ? <Badge tone="info">PPV (not in stock value)</Badge> : row.kind) },
  { key: "productId", header: "Product", mono: true },
  { key: "siteId", header: "Site", mono: true },
  { key: "warehouseId", header: "Warehouse", mono: true, render: (row) => row.warehouseId || dash },
  { key: "valuationType", header: "Valuation type", render: (row) => row.valuationType || "None" },
  { key: "qty", header: "Quantity", align: "end", mono: true },
  { key: "amount", header: "Amount", align: "end", mono: true, render: (row) => `${row.amount} ${row.currency}` },
  { key: "ledgerEntryId", header: "Voucher", mono: true, render: (row) => row.ledgerEntryId || dash },
  { key: "sourceKey", header: "Source key", mono: true },
  { key: "createdAt", header: "Recorded", render: (row) => formatDateTime(row.createdAt) },
];

/** The cost transactions behind the value report (FR-20.4 drill-down), newest financial date first. */
export function CostTransactionsPage() {
  const [search, setSearch] = useSearchParams();
  const applied = filterFromSearch(search);
  const appliedKey = applied ? JSON.stringify(applied) : "";
  const [form, setForm] = useState<Record<Field, string>>(() => Object.fromEntries(FIELDS.map((f) => [f, search.get(f) ?? ""])) as Record<Field, string>);
  const [errors, setErrors] = useState<Partial<Record<Field, string>>>({});
  const state = useLoad((signal) => (applied ? listCostTransactions(applied, signal) : Promise.resolve(null)), appliedKey);
  const [more, setMore] = useState<{ key: string; rows: CostTransaction[]; next?: string }>({ key: "", rows: [] });
  const [moreError, setMoreError] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const extra = more.key === appliedKey ? more : { key: appliedKey, rows: [], next: undefined };
  const rows = [...(state.data?.items ?? []), ...extra.rows];
  const next = extra.rows.length > 0 ? extra.next : state.data?.nextCursor;

  function set(field: Field, value: string) {
    setForm((prev) => ({ ...prev, [field]: value }));
  }

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    const found: Partial<Record<Field, string>> = {};
    if (!UUID.test(form.legalEntityId.trim())) found.legalEntityId = "Enter the legal entity's id (a UUID).";
    for (const f of ["valuationLedgerId", "productId", "siteId"] as const) {
      if (form[f].trim() !== "" && !UUID.test(form[f].trim())) found[f] = "Enter a UUID, or leave it empty.";
    }
    if (form.asOf !== "" && !DAY.test(form.asOf)) found.asOf = "Enter a date, or leave it empty.";
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    const q: Record<string, string> = {};
    for (const f of FIELDS) if (form[f].trim() !== "") q[f] = form[f].trim();
    setSearch(q, { replace: true });
  }

  async function loadMore() {
    if (!applied || !next) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listCostTransactions({ ...applied, cursor: next });
      setMore({ key: appliedKey, rows: [...extra.rows, ...page.items], next: page.nextCursor });
    } catch (err) {
      setMoreError(describeError(err, "The next page could not be loaded."));
    } finally {
      setLoadingMore(false);
    }
  }

  const uuidField = (field: Field, label: string, hint?: string) => (
    <FormField id={`ctx-${field}`} label={label} optional={field !== "legalEntityId"} hint={hint} error={errors[field]}>
      {(c) => <Input {...c} className={styles.mono} value={form[field]} invalid={Boolean(errors[field])} onChange={(e) => set(field, e.target.value)} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cost transactions"
        description="Every value-affecting event behind the inventory value report: physical and financial postings, invoice differences, settlement adjustments, revaluations, manual adjustments and PPV. They are never changed once recorded."
        actions={
          <Link className={buttonClassName("secondary")} to="/costing/value-report">
            Inventory value report
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          {uuidField("legalEntityId", "Legal entity")}
          {uuidField("valuationLedgerId", "Valuation ledger", "Empty is the primary valuation.")}
          {uuidField("productId", "Product")}
          {uuidField("siteId", "Site")}
          <FormField id="ctx-kind" label="Kind" optional>
            {(c) => (
              <Select {...c} value={form.kind} onChange={(e) => set("kind", e.target.value)}>
                <option value="">Every kind</option>
                {COST_KINDS.map((k) => (
                  <option key={k} value={k}>
                    {k}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="ctx-as-of" label="Up to date" optional error={errors.asOf}>
            {(c) => <Input {...c} type="date" value={form.asOf} invalid={Boolean(errors.asOf)} onChange={(e) => set("asOf", e.target.value)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Show cost transactions
            </Button>
          </div>
        </form>
      </Card>
      {!applied ? (
        <EmptyState icon="receipt" headingLevel={2} title="Choose a legal entity" description="Cost transactions are listed per legal entity and valuation ledger." />
      ) : state.error ? (
        <LoadError title="Could not load the cost transactions" error={state.error} onRetry={state.reload} />
      ) : (
        <DataTable
          label="Cost transactions"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading && !state.data}
          empty={{ title: "No cost transactions match", description: "Nothing has been recorded for this selection yet." }}
        />
      )}
      {moreError ? (
        <Alert tone="danger" live="alert">
          {moreError}
        </Alert>
      ) : null}
      {next && applied && !state.error ? (
        <div className={styles.more}>
          <Button loading={loadingMore} onClick={loadMore}>
            Load more cost transactions
          </Button>
        </div>
      ) : null}
    </div>
  );
}
