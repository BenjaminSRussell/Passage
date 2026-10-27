import { useQuery } from "@tanstack/react-query";
import { useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { Alert, Button, Card, FormField, Input, Select, Textarea } from "../../shell/ui";
import {
  fetchReasonCodes,
  LedgerApiError,
  newKey,
  reverseEntry,
  type Entry,
  type ReasonCode,
  type ReverseInput,
} from "./api/client";
import styles from "./pages.module.css";

const DATE = /^\d{4}-\d{2}-\d{2}$/;
const MAX_TEXT = 255;

export interface ReverseDraft {
  reasonCode: string;
  reasonComment: string;
  postingDate: string;
  description: string;
}

type Errors = Record<string, string>;

/** Reason codes a ledger reversal may use: active and applying to the ledger module (FR-11.15). */
export function ledgerReasons(codes: ReasonCode[]): ReasonCode[] {
  return codes.filter((c) => c.active && c.appliesTo.includes("ledger"));
}

/** Client checks that mirror the service; the service has the last word (it knows whether a reason is required). */
export function validateReverse(v: ReverseDraft, entry: Entry, reason: ReasonCode | undefined): Errors {
  const errors: Errors = {};
  if (reason?.requiresComment && v.reasonComment.trim() === "") errors.reasonComment = `Reason code ${reason.code} needs a comment.`;
  if (v.reasonComment.length > MAX_TEXT) errors.reasonComment = `Keep the comment to ${MAX_TEXT} characters.`;
  if (v.description.length > MAX_TEXT) errors.description = `Keep the description to ${MAX_TEXT} characters.`;
  if (v.postingDate !== "") {
    if (!DATE.test(v.postingDate)) errors.postingDate = "Enter the date as YYYY-MM-DD.";
    else if (v.postingDate < entry.postingDate) errors.postingDate = `The reversal can't be dated before the original (${entry.postingDate}).`;
    else if (reason && !reason.alternativePostingDateAllowed && v.postingDate !== entry.postingDate) {
      errors.postingDate = `Reason code ${reason.code} keeps the original posting date (${entry.postingDate}).`;
    }
  }
  return errors;
}

export function toReverseInput(v: ReverseDraft): ReverseInput {
  const input: ReverseInput = {};
  if (v.reasonCode) input.reasonCode = v.reasonCode;
  if (v.reasonComment.trim()) input.reasonComment = v.reasonComment.trim();
  if (v.postingDate) input.postingDate = v.postingDate;
  if (v.description.trim()) input.description = v.description.trim();
  return input;
}

const FORM_FIELDS = new Set(["reasonCode", "reasonComment", "postingDate", "description"]);

/** Reverse a posted entry with a reason (FR-11.8, FR-11.15); a storno code posts negative lines (BR-11.16). */
export function ReverseEntryForm({ entry }: { entry: Entry }) {
  const navigate = useNavigate();
  const reasons = useQuery({ queryKey: ["ledger", "reason-codes"], queryFn: () => fetchReasonCodes() });
  const [values, setValues] = useState<ReverseDraft>({ reasonCode: "", reasonComment: "", postingDate: "", description: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  const options = ledgerReasons(reasons.data ?? []);
  const reason = options.find((r) => r.code === values.reasonCode);

  function update(patch: Partial<ReverseDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  function chooseReason(code: string) {
    const next = options.find((r) => r.code === code);
    // A code that forbids an alternative date keeps the original one, so drop any date typed earlier.
    update(next && !next.alternativePostingDateAllowed ? { reasonCode: code, postingDate: "" } : { reasonCode: code });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateReverse(values, entry, reason);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const [reversing] = await reverseEntry(entry.id, toReverseInput(values), key.current);
      navigate(`/ledger/entries/${reversing.id}`);
    } catch (error) {
      if (error instanceof LedgerApiError && error.violations.length > 0) {
        const fieldErrors: Errors = {};
        const other: string[] = [];
        for (const v of error.violations) {
          if (v.field && FORM_FIELDS.has(v.field) && !fieldErrors[v.field]) fieldErrors[v.field] = v.message;
          else other.push(v.message);
        }
        setErrors(fieldErrors);
        setFailure(other.length > 0 ? `${other.join(" ")} (${error.code})` : null);
      } else if (error instanceof LedgerApiError) {
        setFailure(`${error.message} (${error.code})`);
        if (error.status === 409) key.current = newKey();
      } else {
        setFailure((error as Error).message);
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Reverse this entry">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Reverse entry">
        <p className={`${styles.note} ${styles.wide}`}>
          Posts an exact negating entry linked to this one. Your ledger may require a reason code.
        </p>
        <FormField
          id="rev-reason"
          label="Reason code"
          optional
          hint={reasons.isError ? "Reason codes could not be loaded; you can still reverse without one if your ledger allows it." : undefined}
          error={errors.reasonCode}
        >
          {(c) => (
            <Select {...c} value={values.reasonCode} onChange={(e) => chooseReason(e.target.value)} invalid={Boolean(errors.reasonCode)}>
              <option value="">{reasons.isPending ? "Loading reason codes…" : "No reason code"}</option>
              {options.map((r) => (
                <option key={r.code} value={r.code}>
                  {`${r.code} – ${r.name}`}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField
          id="rev-date"
          label="Reversal date"
          optional
          hint={
            reason && !reason.alternativePostingDateAllowed
              ? `This reason code keeps the original posting date (${entry.postingDate}).`
              : `Leave empty to use the original posting date (${entry.postingDate}).`
          }
          error={errors.postingDate}
        >
          {(c) => (
            <Input
              {...c}
              type="date"
              value={values.postingDate}
              min={entry.postingDate}
              disabled={Boolean(reason && !reason.alternativePostingDateAllowed)}
              onChange={(e) => update({ postingDate: e.target.value })}
              invalid={Boolean(errors.postingDate)}
            />
          )}
        </FormField>
        <FormField id="rev-description" label="Description" optional error={errors.description}>
          {(c) => <Input {...c} value={values.description} placeholder={`Reversal of ${entry.voucher}`} onChange={(e) => update({ description: e.target.value })} invalid={Boolean(errors.description)} />}
        </FormField>
        <div className={styles.wide}>
          <FormField id="rev-comment" label="Reason comment" optional={!reason?.requiresComment} error={errors.reasonComment}>
            {(c) => <Textarea {...c} value={values.reasonComment} maxLength={MAX_TEXT} onChange={(e) => update({ reasonComment: e.target.value })} invalid={Boolean(errors.reasonComment)} />}
          </FormField>
        </div>
        {reason?.negativePostingAllowed ? (
          <div className={styles.formActions}>
            <Alert tone="info" title="Storno reversal">
              {`${reason.code} posts negative lines: the reversal reduces the original debit and credit turnover instead of adding to the opposite side, if your ledger allows negative postings.`}
            </Alert>
          </div>
        ) : null}
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not reverse the entry" live="alert">
              {failure}
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="danger" loading={busy}>
            Reverse entry
          </Button>
        </div>
      </form>
    </Card>
  );
}
