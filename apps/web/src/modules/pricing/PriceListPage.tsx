import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getPriceList, type ConditionType, type PriceList, type PriceListItem, type PriceListScope, type Problem } from "./api/client";
import { DAY, describePercent, describeProduct, today, TYPE_LABELS, UUID } from "./format";
import styles from "./pages.module.css";

const SCOPE_LABELS: Record<PriceListScope, string> = { CUSTOMER: "This customer", GROUP: "Customer group", ALL: "All customers" };

const columns: DataTableColumn<PriceListItem>[] = [
  { key: "productKey", header: "Product", render: (r) => describeProduct(r) },
  { key: "type", header: "Condition", render: (r) => TYPE_LABELS[r.type] ?? r.type },
  { key: "scope", header: "Applies as", render: (r) => SCOPE_LABELS[r.scope] },
  { key: "fromQuantity", header: "From quantity", mono: true, align: "end", render: (r) => `${r.fromQuantity} ${r.unit}` },
  { key: "amount", header: "Amount", mono: true, align: "end", render: (r) => (Number(r.amount) !== 0 ? `${r.amount} ${r.currency}` : "—") },
  { key: "discPct1", header: "Percent", mono: true, align: "end", render: (r) => describePercent(r) || "—" },
  { key: "validTo", header: "Valid", render: (r) => `${r.validFrom} to ${r.validTo}` },
];

/** Price list by customer (PRD 5.26.14): every active agreement that applies to a customer on a date. */
export function PriceListPage() {
  const [f, setF] = useState({ legalEntityId: "", customerId: "", customerGroupId: "", currency: "USD", asOf: today(), type: "PRICE" as ConditionType | "" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [list, setList] = useState<PriceList | null>(null);

  const set = (patch: Partial<typeof f>) => setF((prev) => ({ ...prev, ...patch }));

  async function load(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!UUID.test(f.legalEntityId.trim())) found.legalEntityId = "Enter the legal entity id (a UUID).";
    if (f.customerId.trim() === "") found.customerId = "Enter the customer.";
    if (!/^[A-Z]{3}$/.test(f.currency)) found.currency = "Use a three-letter ISO 4217 code, such as USD.";
    if (!DAY.test(f.asOf)) found.asOf = "Enter the date.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setLoading(true);
    try {
      setList(await getPriceList({ ...f, legalEntityId: f.legalEntityId.trim(), customerId: f.customerId.trim(), customerGroupId: f.customerGroupId.trim() || undefined }));
    } catch (error) {
      const problem = describeProblem(error);
      setList(null);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setLoading(false);
    }
  }

  const text = (id: string, name: "legalEntityId" | "customerId" | "customerGroupId" | "currency" | "asOf", label: string, extra: { optional?: boolean; type?: string; mono?: boolean } = {}) => (
    <FormField id={id} label={label} optional={extra.optional} error={errors[name]}>
      {(c) => <Input {...c} type={extra.type ?? "text"} className={extra.mono ? styles.mono : undefined} value={f[name]} onChange={(e) => set({ [name]: e.target.value })} invalid={Boolean(errors[name])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="Price list by customer" description="The active agreements that apply to a customer on a date, the customer's own first. Price simulation decides which one wins for a line." />
      <Card>
        <form className={styles.form} onSubmit={load} noValidate>
          {text("pl-legal-entity", "legalEntityId", "Legal entity id", { mono: true })}
          {text("pl-customer", "customerId", "Customer")}
          {text("pl-group", "customerGroupId", "Customer group", { optional: true })}
          {text("pl-currency", "currency", "Currency")}
          {text("pl-date", "asOf", "As of", { type: "date" })}
          <FormField id="pl-type" label="Condition">
            {(c) => (
              <Select {...c} value={f.type} onChange={(e) => set({ type: e.target.value as ConditionType | "" })}>
                <option value="">All conditions</option>
                {(Object.keys(TYPE_LABELS) as ConditionType[]).map((t) => (
                  <option key={t} value={t}>
                    {TYPE_LABELS[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={loading}>
              Show price list
            </Button>
          </div>
        </form>
      </Card>
      {failure ? (
        <Alert tone="danger" title="The price list could not be loaded" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {loading ? <Skeleton height="tile" /> : null}
      {!loading && list === null && !failure ? <EmptyState title="No price list yet" description="Enter a customer and a date to list the agreements that apply." /> : null}
      {!loading && list?.truncated ? (
        <Alert tone="warning" title="Only the first agreements are shown" live="status">
          More agreements apply than one list holds ({list.items.length} shown, in product order). Choose a condition to narrow the list.
        </Alert>
      ) : null}
      {!loading && list ? (
        <Card title="Applicable agreements">
          <DataTable
            label="Price list"
            columns={columns}
            rows={list.items}
            getRowKey={(r) => r.id}
            empty={{ title: "No agreements apply", description: "No active record matches this customer on that date." }}
          />
        </Card>
      ) : null}
    </div>
  );
}
