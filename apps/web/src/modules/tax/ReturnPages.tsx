import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { downloadCsv, toCsv } from "../../shell/ui/csv";
import styles from "../../shell/pageLayout.module.css";
import { Link } from "react-router-dom";
import { getEsl, getVatReturn, TaxApiError, type Esl, type VatReturn } from "./api/client";
import { countryLabel, firstOfMonth, lastOfMonth, LoadAlert, taxKey } from "./shared";

export interface PeriodRange {
  from: string;
  to: string;
}

/** The period of a return or sales list: both dates, the end not before the start. */
export function validatePeriod(p: PeriodRange): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!/^\d{4}-\d{2}-\d{2}$/.test(p.from)) errors.from = "Enter the first day of the period.";
  if (!/^\d{4}-\d{2}-\d{2}$/.test(p.to)) errors.to = "Enter the last day of the period.";
  else if (!errors.from && p.to < p.from) errors.to = "The last day cannot be before the first day.";
  return errors;
}

function PeriodForm({ label, initial, onShow }: { label: string; initial: PeriodRange; onShow(p: PeriodRange): void }) {
  const [draft, setDraft] = useState(initial);
  const [errors, setErrors] = useState<Record<string, string>>({});
  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validatePeriod(draft);
    setErrors(found);
    if (Object.keys(found).length === 0) onShow(draft);
  }
  return (
    <Card>
      <form className={styles.form} onSubmit={submit} noValidate aria-label={label}>
        <FormField id="period-from" label="From" error={errors.from}>
          {(c) => <Input {...c} type="date" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} invalid={Boolean(errors.from)} />}
        </FormField>
        <FormField id="period-to" label="To" error={errors.to}>
          {(c) => <Input {...c} type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} invalid={Boolean(errors.to)} />}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary">
            Show {label.toLowerCase()}
          </Button>
        </div>
      </form>
    </Card>
  );
}

const lastMonth = (): PeriodRange => ({ from: firstOfMonth(-1), to: lastOfMonth(-1) });

const boxColumns: DataTableColumn<VatReturn["boxes"][number]>[] = [
  { key: "box", header: "Box", mono: true },
  { key: "label", header: "What it shows" },
  { key: "amount", header: "Amount", align: "end", mono: true },
];

function Warnings({ children }: { children: ReactNode }) {
  return (
    <Alert tone="warning" title="Check these before filing">
      {children}
    </Alert>
  );
}

export function VatReturnPage() {
  const [period, setPeriod] = useState<PeriodRange>(lastMonth);
  const ret = useQuery({ queryKey: taxKey("vat-return", period.from, period.to), queryFn: () => getVatReturn(period.from, period.to) });
  const data = ret.data;
  return (
    <div className={styles.stack}>
      <PageHeader title="VAT return" description="The box totals of the return, worked out from the tax recorded on invoices and bills in the period." />
      <PeriodForm label="VAT return" initial={period} onShow={setPeriod} />
      {ret.isError ? <LoadAlert title="Could not work out the VAT return" error={ret.error} onRetry={() => void ret.refetch()} /> : null}
      {data && data.missing_base > 0 ? (
        <Warnings>
          {data.missing_base} recorded {data.missing_base === 1 ? "transaction has" : "transactions have"} no taxable amount, so the boxes that add up taxable amounts are too low.
        </Warnings>
      ) : null}
      {data && data.unmapped > 0 ? (
        <Warnings>
          {data.unmapped} recorded {data.unmapped === 1 ? "transaction is" : "transactions are"} not counted in any box. Add the tax code to a return box.
        </Warnings>
      ) : null}
      {!ret.isError ? (
        <DataTable
          label="VAT return boxes"
          columns={boxColumns}
          rows={data?.boxes ?? []}
          getRowKey={(b) => b.box}
          loading={ret.isPending}
          empty={{ title: "No return boxes", description: "Set up the boxes of the return first, under Return boxes." }}
        />
      ) : null}
      {data ? <p className={styles.note}>{data.transactions} recorded tax transactions in this period.</p> : null}
    </div>
  );
}

const eslColumns: DataTableColumn<Esl["rows"][number]>[] = [
  { key: "country", header: "Country", render: (r) => countryLabel(r.country) },
  { key: "vat_id", header: "Customer VAT number", mono: true },
  { key: "indicator", header: "Type", render: (r) => ({ L: "Goods", S: "Services", T: "Triangular trade" })[r.indicator] ?? r.indicator },
  { key: "base", header: "Value", align: "end", mono: true },
  { key: "transactions", header: "Invoices", align: "end" },
];

export function EslPage() {
  const [period, setPeriod] = useState<PeriodRange>(lastMonth);
  const esl = useQuery({ queryKey: taxKey("esl", period.from, period.to), queryFn: () => getEsl(period.from, period.to), retry: false });
  const data = esl.data;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="EU sales list"
        description="Sales to VAT-registered businesses in other EU countries, grouped by customer VAT number."
        actions={
          data && data.rows.length > 0 ? (
            <Button
              onClick={() =>
                downloadCsv(
                  `eu-sales-list-${period.from}-${period.to}.csv`,
                  toCsv(["Country", "Customer VAT number", "Type", "Value"], data.rows.map((r) => [r.country, r.vat_id, r.indicator, r.base])),
                )
              }
            >
              Download CSV
            </Button>
          ) : undefined
        }
      />
      <PeriodForm label="EU sales list" initial={period} onShow={setPeriod} />
      {esl.isError && esl.error instanceof TaxApiError && esl.error.code === "COM-0503" ? (
        <Alert tone="warning" title="Set the country of the VAT return first">
          The EU sales list leaves out sales to your own country. Enter it under <Link to="/tax">Tax</Link>, then show the list again.
        </Alert>
      ) : esl.isError ? (
        <LoadAlert title="Could not work out the EU sales list" error={esl.error} onRetry={() => void esl.refetch()} />
      ) : null}
      {data && data.missing_vat_id > 0 ? (
        <Warnings>
          {data.missing_vat_id} {data.missing_vat_id === 1 ? "sale has" : "sales have"} no customer VAT number and {data.missing_vat_id === 1 ? "is" : "are"} left off the list. Add the number to the customer.
        </Warnings>
      ) : null}
      {data && data.missing_base > 0 ? <Warnings>{data.missing_base} sales have no value recorded and are left off the list.</Warnings> : null}
      {!esl.isError ? (
        <DataTable
          label="EU sales list"
          columns={eslColumns}
          rows={data?.rows ?? []}
          getRowKey={(r) => `${r.country}|${r.vat_id}|${r.supply_kind}`}
          loading={esl.isPending}
          empty={{ title: "No EU sales in this period", description: "Sales to other EU countries with a customer VAT number appear here." }}
        />
      ) : null}
      {data ? (
        <p className={styles.note}>
          Filing from {countryLabel(data.home_country)}
          {data.vat_id ? `, VAT number ${data.vat_id}` : ""}. Total {data.total}.
        </p>
      ) : null}
    </div>
  );
}
