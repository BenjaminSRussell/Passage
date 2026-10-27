import { useMutation } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { convertAmounts, FxApiError, type ConvertInput } from "./api/client";
import { Detail } from "./Detail";
import { RateTypeField } from "./RateTypeField";
import styles from "./pages.module.css";
import { emptyFilter, normalizeCurrency, amountError, formatRate, validateFilter, type Errors, type RateFilter } from "./validate";

const SERVER_FIELD: Record<string, string> = { rateType: "rateType", to: "to", date: "date", "amounts[0].value": "amount", "amounts[0].currency": "from" };

function serverErrors(error: unknown): Errors {
  if (!(error instanceof FxApiError)) return {};
  const mapped: Errors = {};
  for (const [field, message] of Object.entries(error.fields)) {
    if (SERVER_FIELD[field]) mapped[SERVER_FIELD[field]] = message;
  }
  return mapped;
}

export function ConvertPage() {
  const [values, setValues] = useState<RateFilter>(emptyFilter);
  const [amount, setAmount] = useState("");
  const [errors, setErrors] = useState<Errors>({});
  const convert = useMutation({ mutationFn: (input: ConvertInput) => convertAmounts(input), onError: (error) => setErrors(serverErrors(error)) });

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateFilter(values, { pair: true, dated: true });
    const invalidAmount = amountError(amount);
    if (invalidAmount) found.amount = invalidAmount;
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    convert.mutate({
      rateType: values.rateType,
      to: values.to,
      date: values.date || undefined,
      via: values.via || undefined,
      amounts: [{ value: amount.trim(), currency: values.from }],
    });
  }

  const result = convert.data?.results[0];
  const failure = convert.error && Object.keys(serverErrors(convert.error)).length === 0 ? (convert.error as Error).message : null;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Convert an amount"
        description="Convert an amount between two currencies at the rate valid on a date. The result is rounded once to the smallest unit of the target currency. Nothing is saved."
      />
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Convert an amount">
        <RateTypeField id="convert-type" value={values.rateType} error={errors.rateType} onChange={(rateType) => setValues({ ...values, rateType })} />
        <FormField id="convert-amount" label="Amount" error={errors.amount}>
          {(c) => <Input {...c} inputMode="decimal" value={amount} placeholder="1250.50" onChange={(e) => setAmount(e.target.value)} invalid={Boolean(errors.amount)} />}
        </FormField>
        <FormField id="convert-from" label="From currency" error={errors.from}>
          {(c) => <Input {...c} value={values.from} maxLength={3} placeholder="EUR" onChange={(e) => setValues({ ...values, from: normalizeCurrency(e.target.value) })} invalid={Boolean(errors.from)} />}
        </FormField>
        <FormField id="convert-to" label="To currency" error={errors.to}>
          {(c) => <Input {...c} value={values.to} maxLength={3} placeholder="USD" onChange={(e) => setValues({ ...values, to: normalizeCurrency(e.target.value) })} invalid={Boolean(errors.to)} />}
        </FormField>
        <FormField id="convert-date" label="Date" optional hint="Leave empty for today (UTC)." error={errors.date}>
          {(c) => <Input {...c} type="date" value={values.date} onChange={(e) => setValues({ ...values, date: e.target.value })} invalid={Boolean(errors.date)} />}
        </FormField>
        <FormField id="convert-via" label="Reference currency" optional hint="Used only when no direct or inverse rate exists." error={errors.via}>
          {(c) => <Input {...c} value={values.via} maxLength={3} placeholder="EUR" onChange={(e) => setValues({ ...values, via: normalizeCurrency(e.target.value) })} invalid={Boolean(errors.via)} />}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={convert.isPending}>
            Convert
          </Button>
        </div>
      </form>
      {failure ? (
        <Alert tone="danger" title="Could not convert" live="alert">
          {failure}
        </Alert>
      ) : null}
      {result && convert.data ? (
        <Card title="Result">
          <dl className={styles.details}>
            <Detail term="Converted">{`${result.converted.value} ${result.converted.currency}`}</Detail>
            <Detail term="Original">{`${result.amount.value} ${result.amount.currency}`}</Detail>
            <Detail term="Rate" title={result.rate}>{`${formatRate(result.rate)} (${result.path}) on ${convert.data.date}`}</Detail>
          </dl>
        </Card>
      ) : null}
    </div>
  );
}
