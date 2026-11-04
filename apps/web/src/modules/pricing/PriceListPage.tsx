import { useMemo, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getNetPriceList, getPriceList, type ConditionType, type NetPriceList, type PriceList, type PriceListItem, type PriceListScope, type Problem } from "./api/client";
import { DAY, describePercent, describeProduct, netPriceLines, netPricesCsv, today, TYPE_LABELS, UUID, winningQuantities, type NetPriceLine } from "./format";
import styles from "./pages.module.css";

const SCOPE_LABELS: Record<PriceListScope, string> = { CUSTOMER: "This customer", GROUP: "Customer group", ALL: "All customers" };

const netColumns: DataTableColumn<NetPriceLine>[] = [
  { key: "product", header: "Product", render: (l) => l.product },
  { key: "unit", header: "Unit", render: (l) => l.unit || "Any" },
  { key: "quantity", header: "From quantity", mono: true, align: "end", render: (l) => l.quantity },
  { key: "unitPrice", header: "List price", mono: true, align: "end", render: (l) => (l.errorCode ? "—" : l.unitPrice) },
  { key: "netUnitPrice", header: "Net unit price", mono: true, align: "end", render: (l) => (l.errorCode ? "No price" : l.netUnitPrice) },
  { key: "lineNet", header: "Line net", mono: true, align: "end", render: (l) => (l.errorCode ? "—" : l.lineNet) },
];

function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/csv;charset=utf-8" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

function columnsFor(wins: Map<string, string[]>): DataTableColumn<PriceListItem>[] {
  return [
    ...baseColumns,
    { key: "wins", header: "Applies at quantity", mono: true, align: "end", render: (r) => wins.get(r.id)?.join(", ") ?? "—" },
  ];
}

const baseColumns: DataTableColumn<PriceListItem>[] = [
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
  const [f, setF] = useState({ legalEntityId: "", customerId: "", customerGroupId: "", currency: "USD", asOf: today(), type: "PRICE" as ConditionType | "", productId: "", productGroupId: "", salesChannel: "" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [list, setList] = useState<PriceList | null>(null);
  const [net, setNet] = useState<NetPriceList | null>(null);
  const [netFailure, setNetFailure] = useState<Problem | null>(null);
  // The query the shown tables answer, for the CSV header and file name (the form may have changed since).
  const [asked, setAsked] = useState({ customerId: "", asOf: "", currency: "USD" });

  const wins = useMemo(() => winningQuantities(net?.rows ?? []), [net]);
  const lines = useMemo(() => netPriceLines(net?.rows ?? []), [net]);

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
    setNetFailure(null);
    if (Object.keys(found).length > 0) return;
    setLoading(true);
    const who = { legalEntityId: f.legalEntityId.trim(), customerId: f.customerId.trim(), customerGroupId: f.customerGroupId.trim() || undefined, currency: f.currency, asOf: f.asOf };
    try {
      setList(await getPriceList({ ...who, type: f.type }));
      setAsked({ customerId: who.customerId, asOf: who.asOf, currency: who.currency });
    } catch (error) {
      const problem = describeProblem(error);
      setList(null);
      setNet(null);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      setLoading(false);
      return;
    }
    try {
      setNet(
        await getNetPriceList({
          ...who,
          productId: f.productId.trim() || undefined,
          productGroupId: f.productGroupId.trim() || undefined,
          salesChannel: f.salesChannel.trim() || undefined,
        }),
      );
    } catch (error) {
      const problem = describeProblem(error);
      setNet(null);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setNetFailure(problem);
    } finally {
      setLoading(false);
    }
  }

  const text = (id: string, name: "legalEntityId" | "customerId" | "customerGroupId" | "currency" | "asOf" | "productId" | "productGroupId" | "salesChannel", label: string, extra: { optional?: boolean; type?: string; mono?: boolean; hint?: string } = {}) => (
    <FormField id={id} label={label} optional={extra.optional} hint={extra.hint} error={errors[name]}>
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
          {text("pl-product", "productId", "Product", { optional: true, hint: "Narrows the net prices to this product." })}
          {text("pl-product-group", "productGroupId", "Product group", { optional: true, hint: "Narrows the net prices to this group's own prices." })}
          {text("pl-channel", "salesChannel", "Sales channel", { optional: true, hint: "Prices with the procedure assigned to the channel." })}
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
            columns={columnsFor(wins)}
            rows={list.items}
            getRowKey={(r) => r.id}
            empty={{ title: "No agreements apply", description: "No active record matches this customer on that date." }}
          />
        </Card>
      ) : null}
      {!loading && netFailure ? (
        <Alert tone="warning" title="The net prices could not be loaded" live="alert">
          {netFailure.message} ({netFailure.code})
        </Alert>
      ) : null}
      {!loading && net?.truncated ? (
        <Alert tone="warning" title="Only the first products are shown" live="status">
          More products apply than one response prices ({net.rows.length} shown, in product order). Enter a product or a product group to narrow it.
        </Alert>
      ) : null}
      {!loading && net ? (
        <Card
          title="Net price per quantity break"
          actions={
            lines.length > 0 ? (
              <Button variant="secondary" onClick={() => download(`price-list-${asked.customerId}-${asked.asOf}.csv`, netPricesCsv(lines, asked.currency))}>
                Export CSV
              </Button>
            ) : undefined
          }
        >
          <DataTable
            label="Net prices"
            columns={netColumns}
            rows={lines}
            getRowKey={(l) => l.key}
            empty={{ title: "No prices to resolve", description: "No product has an active price for this customer on that date." }}
          />
        </Card>
      ) : null}
    </div>
  );
}
