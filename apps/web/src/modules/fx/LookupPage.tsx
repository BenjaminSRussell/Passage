import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { FxApiError, lookupRate, type LookupQuery, type Resolution } from "./api/client";
import { Detail } from "./Detail";
import { RateTypeField } from "./RateTypeField";
import styles from "./pages.module.css";
import { emptyFilter, formatRate, normalizeCurrency, validateFilter, type Errors, type RateFilter } from "./validate";

type Hop = Resolution["hops"][number];

const hopColumns: DataTableColumn<Hop>[] = [
  { key: "from", header: "From", mono: true },
  { key: "to", header: "To", mono: true },
  { key: "rate", header: "Rate", align: "end", mono: true, render: (h) => <span title={h.rate}>{formatRate(h.rate)}</span> },
  { key: "validFrom", header: "Valid from", mono: true },
];

const PATH_LABEL: Record<Resolution["path"], string> = {
  direct: "Direct: a stored rate for this pair",
  inverse: "Inverse: the reciprocal of the opposite pair",
  triangulated: "Triangulated: two stored rates through the reference currency",
};

export function LookupPage() {
  const [values, setValues] = useState<RateFilter>(emptyFilter);
  const [errors, setErrors] = useState<Errors>({});
  const [query, setQuery] = useState<LookupQuery | null>(null);
  const result = useQuery({ queryKey: ["fx", "lookup", query], queryFn: () => lookupRate(query as LookupQuery), enabled: query !== null });

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateFilter(values, { pair: true, dated: true });
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setQuery({ rateType: values.rateType, from: values.from, to: values.to, date: values.date || undefined, via: values.via || undefined });
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Rate lookup"
        description="See which rate applies to a currency pair on a date and how it was found: a stored rate, the inverse of one, or two rates chained through a reference currency."
      />
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Look up a rate">
        <RateTypeField id="lookup-type" value={values.rateType} error={errors.rateType} onChange={(rateType) => setValues({ ...values, rateType })} />
        <FormField id="lookup-from" label="From currency" error={errors.from}>
          {(c) => <Input {...c} value={values.from} maxLength={3} placeholder="EUR" onChange={(e) => setValues({ ...values, from: normalizeCurrency(e.target.value) })} invalid={Boolean(errors.from)} />}
        </FormField>
        <FormField id="lookup-to" label="To currency" error={errors.to}>
          {(c) => <Input {...c} value={values.to} maxLength={3} placeholder="USD" onChange={(e) => setValues({ ...values, to: normalizeCurrency(e.target.value) })} invalid={Boolean(errors.to)} />}
        </FormField>
        <FormField id="lookup-date" label="Date" optional hint="Leave empty for today (UTC)." error={errors.date}>
          {(c) => <Input {...c} type="date" value={values.date} onChange={(e) => setValues({ ...values, date: e.target.value })} invalid={Boolean(errors.date)} />}
        </FormField>
        <FormField id="lookup-via" label="Reference currency" optional hint="Used only when no direct or inverse rate exists." error={errors.via}>
          {(c) => <Input {...c} value={values.via} maxLength={3} placeholder="EUR" onChange={(e) => setValues({ ...values, via: normalizeCurrency(e.target.value) })} invalid={Boolean(errors.via)} />}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={result.isFetching}>
            Look up rate
          </Button>
        </div>
      </form>
      {result.isError ? (
        <Alert tone="danger" title={result.error instanceof FxApiError && result.error.code === "LEDGER_RATE_MISSING" ? "No rate found" : "Could not look up the rate"} live="alert">
          {(result.error as Error).message}
        </Alert>
      ) : null}
      {result.data ? (
        <Card title="Result">
          <dl className={styles.details}>
            <Detail term="Rate" title={result.data.rate}>{`1 ${result.data.from} = ${formatRate(result.data.rate)} ${result.data.to}`}</Detail>
            <Detail term="Date">{result.data.date}</Detail>
            <Detail term="Resolution">{PATH_LABEL[result.data.path]}</Detail>
          </dl>
          <DataTable label="Rates used" columns={hopColumns} rows={result.data.hops} getRowKey={(h, i) => `${h.from}${h.to}${i}`} />
        </Card>
      ) : null}
    </div>
  );
}
