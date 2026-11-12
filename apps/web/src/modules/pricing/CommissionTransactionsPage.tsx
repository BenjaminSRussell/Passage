import { useState, type FormEvent } from "react";
import { RepName } from "./RepName";
import { EntityName } from "../../shell/lookup/EntityName";
import { LookupFormField } from "../../shell/lookup/LookupFormField";
import { Link, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { listCommissionTransactions, listSalesGroups, type CommissionFilter, type CommissionTransaction } from "./api/client";
import { sumDecimals } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const MONTH = /^\d{4}-(0[1-9]|1[0-2])$/;
const FILTERS = ["salesGroupId", "repId", "period", "customerId", "invoiceLineId"] as const;
type FilterKey = (typeof FILTERS)[number];
type Draft = Record<FilterKey, string>;

function columns(groupName: (id: string) => string): DataTableColumn<CommissionTransaction>[] {
  return [
    { key: "invoiceDate", header: "Invoice date", mono: true },
    {
      key: "invoiceLineId",
      header: "Invoice line",
      mono: true,
      render: (t) => (t.reversesInvoiceLineId ? `${t.invoiceLineId} (credits ${t.reversesInvoiceLineId})` : t.invoiceLineId),
    },
    { key: "customerId", header: "Customer", render: (t) => (t.customerId ? <EntityName entity="customer" id={t.customerId} fallback="Customer" /> : "") },
    { key: "salesGroupId", header: "Sales group", render: (t) => groupName(t.salesGroupId) },
    { key: "repId", header: "Rep", render: (t) => <RepName id={t.repId} /> },
    { key: "basis", header: "Basis", render: (t) => (t.basis === "MARGIN" ? "Margin" : "Revenue") },
    { key: "basisAmount", header: "Basis amount", mono: true, align: "end" },
    { key: "ratePct", header: "Rate", mono: true, align: "end", render: (t) => `${t.ratePct}% × ${t.sharePct}%` },
    { key: "amount", header: "Commission", mono: true, align: "end", render: (t) => `${t.amount} ${t.currency}` },
    { key: "posting", header: "Posting", mono: true, render: (t) => `Dr ${t.debitAccount} / Cr ${t.creditAccount}` },
  ];
}

/** Commission transactions inquiry (§5.26.14; AX CommissionTrans): what each rep earned, by invoice line. */
export function CommissionTransactionsPage() {
  const [params, setParams] = useSearchParams();
  const applied: CommissionFilter = Object.fromEntries(FILTERS.map((k) => [k, params.get(k) ?? ""]));
  const [draft, setDraft] = useState<Draft>(applied as Draft);
  const [periodError, setPeriodError] = useState("");
  const groups = useLoad((signal) => listSalesGroups("", signal), "groups");
  const txns = useLoad((signal) => listCommissionTransactions(applied, signal), params.toString());

  function filter(event: FormEvent) {
    event.preventDefault();
    if (draft.period.trim() !== "" && !MONTH.test(draft.period.trim())) {
      setPeriodError("Use a month such as 2027-03.");
      return;
    }
    setPeriodError("");
    const next = new URLSearchParams();
    for (const k of FILTERS) {
      const v = draft[k].trim();
      if (v !== "") next.set(k, v);
    }
    setParams(next);
  }

  const names = new Map((groups.data ?? []).map((g) => [g.id, g.name]));
  const rows = txns.data ?? [];
  const currencies = new Set(rows.map((r) => r.currency));
  const total = currencies.size === 1 ? `${sumDecimals(rows.map((r) => r.amount))} ${[...currencies][0]}` : currencies.size === 0 ? "0" : "Several currencies";
  const lines = new Set(rows.map((r) => r.invoiceLineId)).size;
  const text = (key: FilterKey, label: string, hint?: string) => (
    <FormField id={`ct-${key}`} label={label} optional hint={hint} error={key === "period" ? periodError || undefined : undefined}>
      {(c) => (
        <Input {...c} className={styles.mono} value={draft[key]} onChange={(e) => setDraft((d) => ({ ...d, [key]: e.target.value }))} invalid={key === "period" && Boolean(periodError)} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Commission transactions"
        description="One row per rep for every posted invoice line. Credit notes post the negative of the line they reverse."
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to="/pricing/commission/settlement">
              Settlement export
            </Link>
            <Link className={buttonClassName("secondary")} to="/pricing/commission/statement">
              Rep statement
            </Link>
            <Link className={buttonClassName("ghost")} to="/pricing/commission/sales-groups">
              Sales groups
            </Link>
          </div>
        }
      />
      <form className={styles.toolbar} onSubmit={filter} noValidate>
        <FormField id="ct-salesGroupId" label="Sales group" optional>
          {(c) => (
            <Select {...c} value={draft.salesGroupId} onChange={(e) => setDraft((d) => ({ ...d, salesGroupId: e.target.value }))}>
              <option value="">All sales groups</option>
              {(groups.data ?? []).map((g) => (
                <option key={g.id} value={g.id}>
                  {g.name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <LookupFormField id="ct-repId" label="Rep" entity="worker" optional prefill={false} value={draft.repId} onChange={(repId) => setDraft((d) => ({ ...d, repId }))} />
        {text("period", "Month", "For example 2027-03.")}
        <LookupFormField id="ct-customerId" label="Customer" entity="customer" optional prefill={false} value={draft.customerId} onChange={(customerId) => setDraft((d) => ({ ...d, customerId }))} />
        {text("invoiceLineId", "Invoice line", "Also finds its credit notes.")}
        <Button type="submit">Show transactions</Button>
      </form>
      {txns.error ? (
        <Alert tone="danger" title="Could not load the commission transactions" live="alert" action={<Button size="sm" onClick={txns.reload}>Try again</Button>}>
          <ProblemMessage problem={txns.error} />
        </Alert>
      ) : (
        <>
          <StatGroup label="Commission totals">
            <StatTile label="Commission" value={txns.data ? total : "…"} />
            <StatTile label="Invoice lines" value={txns.data ? lines : "…"} />
          </StatGroup>
          <DataTable
            label="Commission transactions"
            columns={columns((id) => names.get(id) ?? id)}
            rows={rows}
            getRowKey={(t) => t.id}
            loading={txns.loading}
            empty={{ title: "No commission transactions", description: "Transactions appear when posted invoices of a sales group's orders reach pricing." }}
          />
          {rows.length >= 100 ? <p className={styles.note}>Showing the 100 newest rows; the totals cover those rows only. Filter by month or rep to narrow the list.</p> : null}
        </>
      )}
    </div>
  );
}
