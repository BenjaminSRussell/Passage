import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Button, FormField, Input, PageHeader } from "../../shell/ui";
import { fetchHistory, fetchRateTypes, publishRate, type HistoryEntry, type RateEntryInput } from "./api/client";
import { ApiFailure, fieldsOwnFailure, mapServerFields, useFocusFirstInvalid, withoutFields } from "./formSupport";
import { ImportNow } from "./ImportNow";
import { RateTypeField } from "./RateTypeField";
import styles from "./pages.module.css";
import { deviationBeyondLimit, DEVIATION_LIMIT_PERCENT, emptyEntry, formatRate, normalizeCurrency, SAME_CURRENCY_ERROR, validateEntry, type Deviation, type Errors, type RateEntry } from "./validate";

const DUTY = "Maintain exchange rates";
const FIELDS = ["rateType", "from", "to", "validFrom", "rate"] as const;
const SERVER_PREFIX = "rates[0].";

interface Review {
  input: RateEntryInput;
  previous: HistoryEntry;
  deviation: Deviation;
}

function previousRate(entries: HistoryEntry[], validFrom: string): HistoryEntry | undefined {
  return entries
    .filter((e) => !e.supersededBy && e.validFrom <= validFrom)
    .sort((a, b) => b.validFrom.localeCompare(a.validFrom) || b.version - a.version)[0];
}

export function RateEntryPage() {
  const queryClient = useQueryClient();
  const form = useRef<HTMLFormElement>(null);
  const rateField = useRef<HTMLInputElement>(null);
  const confirmButton = useRef<HTMLButtonElement>(null);
  const saveButton = useRef<HTMLButtonElement>(null);
  const idempotencyKey = useRef<string | null>(null);
  const [values, setValues] = useState<RateEntry>(emptyEntry);
  const [errors, setErrors] = useState<Errors>({});
  const [review, setReview] = useState<Review | null>(null);
  const [saved, setSaved] = useState<{ version: number; unchanged: boolean } | null>(null);
  const types = useQuery({ queryKey: ["fx", "rate-types"], queryFn: () => fetchRateTypes() });
  useFocusFirstInvalid(form, errors);
  useEffect(() => {
    if (review) confirmButton.current?.focus();
  }, [review]);

  const publish = useMutation({
    mutationFn: (input: RateEntryInput) => {
      idempotencyKey.current ??= crypto.randomUUID();
      return publishRate(input, idempotencyKey.current);
    },
    onSuccess: async (response) => {
      const [posted] = response.rates;
      setSaved({ version: posted.version, unchanged: posted.unchanged });
      setReview(null);
      setValues({ ...values, rate: "" });
      idempotencyKey.current = null;
      await queryClient.invalidateQueries({ queryKey: ["fx", "history"] });
    },
    onError: (error) => {
      setErrors(mapServerFields(error, SERVER_PREFIX, FIELDS));
      setReview(null);
    },
  });

  const check = useMutation({
    mutationFn: async (input: RateEntryInput) => {
      const previous = previousRate(await fetchHistory(input), input.validFrom);
      return { input, previous, deviation: previous ? deviationBeyondLimit(previous.rate, input.rate) : null };
    },
    onSuccess: ({ input, previous, deviation }) => {
      if (previous && deviation) setReview({ input, previous, deviation });
      else publish.mutate(input);
    },
  });

  function change(next: Partial<RateEntry>) {
    setValues({ ...values, ...next });
    const changedFields = Object.keys(next);
    const cleared = withoutFields(errors, changedFields);
    if ("from" in next && cleared.to === SAME_CURRENCY_ERROR) delete cleared.to;
    setErrors(cleared);
    setSaved(null);
    setReview(null);
    idempotencyKey.current = null;
    check.reset();
    publish.reset();
  }

  function goBack() {
    setReview(null);
    rateField.current?.focus();
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    if (review) return;
    const found = validateEntry(values);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setSaved(null);
    publish.reset();
    check.mutate({ rateType: values.rateType, from: values.from, to: values.to, validFrom: values.validFrom, rate: values.rate.trim() });
  }

  const busy = check.isPending || publish.isPending;
  const publishFailure = publish.isError && !fieldsOwnFailure(publish.error, SERVER_PREFIX, FIELDS);
  useEffect(() => {
    if (busy) return;
    if (saved) rateField.current?.focus();
    else if (publishFailure) saveButton.current?.focus();
  }, [busy, saved, publishFailure]);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Enter a rate"
        description="Add a rate by hand. Saving a different rate for a pair and date that already has one keeps the earlier value in the history and supersedes it. 1 unit of the first currency equals the rate in the second."
      />
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Enter a rate" ref={form}>
        <fieldset className={styles.fields} disabled={busy}>
          <RateTypeField id="entry-type" value={values.rateType} error={errors.rateType} onChange={(rateType) => change({ rateType })} />
          <FormField id="entry-from" label="From currency" error={errors.from}>
            {(c) => <Input {...c} value={values.from} maxLength={3} placeholder="EUR" onChange={(e) => change({ from: normalizeCurrency(e.target.value) })} invalid={Boolean(errors.from)} />}
          </FormField>
          <FormField id="entry-to" label="To currency" error={errors.to}>
            {(c) => <Input {...c} value={values.to} maxLength={3} placeholder="USD" onChange={(e) => change({ to: normalizeCurrency(e.target.value) })} invalid={Boolean(errors.to)} />}
          </FormField>
          <FormField id="entry-date" label="Valid from" error={errors.validFrom}>
            {(c) => <Input {...c} type="date" value={values.validFrom} onChange={(e) => change({ validFrom: e.target.value })} invalid={Boolean(errors.validFrom)} />}
          </FormField>
          <FormField id="entry-rate" label="Rate" hint="Up to 10 decimal places." error={errors.rate}>
            {(c) => <Input {...c} ref={rateField} inputMode="decimal" value={values.rate} placeholder="1.0855" onChange={(e) => change({ rate: e.target.value })} invalid={Boolean(errors.rate)} />}
          </FormField>
        </fieldset>
        <div className={styles.formActions}>
          <Button ref={saveButton} type="submit" variant="primary" loading={busy} aria-disabled={review !== null || undefined}>
            Save rate
          </Button>
        </div>
      </form>
      <Alert
        tone="warning"
        live="status"
        title={review ? `This rate is ${review.deviation.percent}% ${review.deviation.direction} the previous rate` : undefined}
        action={
          review ? (
            <>
              <Button ref={confirmButton} variant="primary" loading={publish.isPending} onClick={() => publish.mutate(review.input)}>
                Confirm and save
              </Button>
              <Button variant="secondary" disabled={publish.isPending} onClick={goBack}>
                Go back and edit
              </Button>
            </>
          ) : undefined
        }
      >
        {review
          ? `${review.input.from} to ${review.input.to} was ${formatRate(review.previous.rate)} from ${review.previous.validFrom}. Changes of more than ${DEVIATION_LIMIT_PERCENT}% need confirmation, because they are often a typing mistake.`
          : undefined}
      </Alert>
      {check.isError ? <ApiFailure error={check.error} title="Could not compare with the previous rate" duty="View exchange rates" /> : null}
      {publishFailure ? <ApiFailure error={publish.error} title="Could not save the rate" duty={DUTY} /> : null}
      <ImportNow key={values.rateType} rateType={types.data?.find((t) => t.code === values.rateType)} />
      <Alert tone="success" live="status" title={saved ? "Rate saved" : undefined}>
        {saved ? (saved.unchanged ? "This rate was already stored, so nothing changed." : `Stored as version ${saved.version}.`) : undefined}
      </Alert>
    </div>
  );
}
