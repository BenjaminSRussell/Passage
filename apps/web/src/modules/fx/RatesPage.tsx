import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Alert, Button, DataTable, FormField, Input, PageHeader, StatusPill, type DataTableColumn } from "../../shell/ui";
import { fetchHistory, type HistoryEntry, type PairQuery } from "./api/client";
import { RateTypeField } from "./RateTypeField";
import styles from "./pages.module.css";
import { emptyFilter, normalizeCurrency, validateFilter, type Errors, type RateFilter } from "./validate";

const columns: DataTableColumn<HistoryEntry>[] = [
  { key: "validFrom", header: "Valid from", mono: true },
  { key: "version", header: "Version", align: "end" },
  { key: "rate", header: "Rate", align: "end", mono: true },
  { key: "source", header: "Source" },
  { key: "status", header: "Status", render: (e) => <StatusPill status={e.supersededBy ? "superseded" : "active"} /> },
  { key: "createdAt", header: "Recorded", mono: true, render: (e) => e.createdAt.slice(0, 16).replace("T", " ") },
  { key: "createdBy", header: "By" },
];

export function RatesPage() {
  const [values, setValues] = useState<RateFilter>(emptyFilter);
  const [errors, setErrors] = useState<Errors>({});
  const [query, setQuery] = useState<PairQuery | null>(null);
  const history = useQuery({
    queryKey: ["fx", "history", query],
    queryFn: () => fetchHistory(query as PairQuery),
    enabled: query !== null,
  });

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateFilter(values, { pair: true, dated: false });
    setErrors(found);
    if (Object.keys(found).length === 0) setQuery({ rateType: values.rateType, from: values.from, to: values.to });
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Exchange rates"
        description="Every stored version of the rates for one currency pair, including corrections that superseded earlier values. 1 unit of the first currency equals the rate in the second."
      />
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Find rates">
        <RateTypeField id="rates-type" value={values.rateType} error={errors.rateType} onChange={(rateType) => setValues({ ...values, rateType })} />
        <FormField id="rates-from" label="From currency" error={errors.from}>
          {(c) => <Input {...c} value={values.from} maxLength={3} placeholder="EUR" onChange={(e) => setValues({ ...values, from: normalizeCurrency(e.target.value) })} invalid={Boolean(errors.from)} />}
        </FormField>
        <FormField id="rates-to" label="To currency" error={errors.to}>
          {(c) => <Input {...c} value={values.to} maxLength={3} placeholder="USD" onChange={(e) => setValues({ ...values, to: normalizeCurrency(e.target.value) })} invalid={Boolean(errors.to)} />}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={history.isFetching}>
            Show rates
          </Button>
        </div>
      </form>
      {history.isError ? (
        <Alert tone="danger" title="Could not load the rates" live="alert">
          {(history.error as Error).message}
        </Alert>
      ) : null}
      {query ? (
        <DataTable
          label="Rate history"
          columns={columns}
          rows={history.data ?? []}
          getRowKey={(e) => e.id}
          loading={history.isPending}
          empty={{ title: "No rates for this pair", description: "Nothing is stored for this rate type and direction. Try the reverse direction, or use the lookup to see an inverse or triangulated rate." }}
        />
      ) : null}
    </div>
  );
}
