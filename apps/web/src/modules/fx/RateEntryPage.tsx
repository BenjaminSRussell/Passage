import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, FormField, Input, PageHeader } from "../../shell/ui";
import { fetchHistory, publishRate, type HistoryEntry, type RateEntryInput } from "./api/client";
import { ApiFailure, mapServerFields, useFocusFirstInvalid } from "./formSupport";
import { RateTypeField } from "./RateTypeField";
import styles from "./pages.module.css";
import { deviationBeyondLimit, DEVIATION_LIMIT_PERCENT, emptyEntry, normalizeCurrency, validateEntry, type Deviation, type Errors, type RateEntry } from "./validate";

const DUTY = "Maintain exchange rates";
const FIELDS = ["rateType", "from", "to", "validFrom", "rate"] as const;

interface Review {
  input: RateEntryInput;
  previous: HistoryEntry;
  deviation: Deviation;
}

function latestRate(entries: HistoryEntry[]): HistoryEntry | undefined {
  return entries
    .filter((e) => !e.supersededBy)
    .sort((a, b) => b.validFrom.localeCompare(a.validFrom) || b.version - a.version)[0];
}

export function RateEntryPage() {
  const queryClient = useQueryClient();
  const form = useRef<HTMLFormElement>(null);
  const idempotencyKey = useRef<string | null>(null);
  const [values, setValues] = useState<RateEntry>(emptyEntry);
  const [errors, setErrors] = useState<Errors>({});
  const [review, setReview] = useState<Review | null>(null);
  const [saved, setSaved] = useState<{ version: number; unchanged: boolean } | null>(null);
  useFocusFirstInvalid(form, errors);

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
      setErrors(mapServerFields(error, "rates[0].", FIELDS));
      setReview(null);
    },
  });

  const check = useMutation({
    mutationFn: async (input: RateEntryInput) => {
      const previous = latestRate(await fetchHistory(input));
      const deviation = previous ? deviationBeyondLimit(previous.rate, input.rate) : null;
      return { input, previous, deviation };
    },
    onSuccess: ({ input, previous, deviation }) => {
      if (previous && deviation) setReview({ input, previous, deviation });
      else publish.mutate(input);
    },
  });

  function change(next: Partial<RateEntry>) {
    setValues({ ...values, ...next });
    setSaved(null);
    setReview(null);
    idempotencyKey.current = null;
    check.reset();
    publish.reset();
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateEntry(values);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    check.mutate({ rateType: values.rateType, from: values.from, to: values.to, validFrom: values.validFrom, rate: values.rate.trim() });
  }

  const busy = check.isPending || publish.isPending;
  const fieldFailure = publish.isError && Object.keys(mapServerFields(publish.error, "rates[0].", FIELDS)).length === 0;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Enter a rate"
        description="Add a rate by hand. Saving a different rate for a pair and date that already has one keeps the earlier value in the history and supersedes it. 1 unit of the first currency equals the rate in the second."
      />
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Enter a rate" ref={form}>
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
        <FormField id="entry-rate" label="Rate" error={errors.rate}>
          {(c) => <Input {...c} inputMode="decimal" value={values.rate} placeholder="1.0855" onChange={(e) => change({ rate: e.target.value })} invalid={Boolean(errors.rate)} />}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy} disabled={review !== null}>
            Save rate
          </Button>
        </div>
      </form>
      {review ? (
        <Alert
          tone="warning"
          title={`This rate is ${review.deviation.percent}% ${review.deviation.direction} the previous rate`}
          live="alert"
          action={
            <>
              <Button variant="primary" loading={publish.isPending} onClick={() => publish.mutate(review.input)}>
                Confirm and save
              </Button>
              <Button variant="secondary" onClick={() => setReview(null)}>
                Go back and edit
              </Button>
            </>
          }
        >
          {`${review.input.from} to ${review.input.to} was ${review.previous.rate} from ${review.previous.validFrom}. Changes of more than ${DEVIATION_LIMIT_PERCENT}% need confirmation, because they are often a typing mistake.`}
        </Alert>
      ) : null}
      {check.isError ? <ApiFailure error={check.error} title="Could not compare with the previous rate" duty="View exchange rates" /> : null}
      {fieldFailure ? <ApiFailure error={publish.error} title="Could not save the rate" duty={DUTY} /> : null}
      {saved ? (
        <Alert tone="success" title="Rate saved" live="status">
          {saved.unchanged ? "This rate was already stored, so nothing changed." : `Stored as version ${saved.version}.`}
        </Alert>
      ) : null}
    </div>
  );
}
