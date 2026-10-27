import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Skeleton } from "../../shell/ui";
import {
  fetchReasonCode,
  LedgerApiError,
  putReasonCode,
  REASON_CODE,
  REASON_MODULES,
  type ReasonCode,
  type ReasonCodeInput,
} from "./api/client";
import { moduleLabel } from "./ReasonCodesPage";
import styles from "./pages.module.css";

const MAX_NAME = 60;

export interface ReasonDraft extends ReasonCodeInput {
  code: string;
}

type Errors = Record<string, string>;

export const emptyReason: ReasonDraft = {
  code: "",
  name: "",
  appliesTo: ["ledger"],
  requiresComment: false,
  negativePostingAllowed: false,
  alternativePostingDateAllowed: true,
  active: true,
};

/** Client checks that mirror PUT /reason-codes/{code} (FR-11.15); the service has the last word. */
export function validateReason(v: ReasonDraft): Errors {
  const errors: Errors = {};
  if (!REASON_CODE.test(v.code)) errors.code = "Use 1 to 10 capital letters, digits or underscores.";
  const name = v.name.trim();
  if (name === "" || name.length > MAX_NAME) errors.name = `Enter a name of up to ${MAX_NAME} characters.`;
  if (v.appliesTo.length === 0) errors.appliesTo = "Pick at least one module.";
  return errors;
}

export function toReasonInput(v: ReasonDraft): ReasonCodeInput {
  return {
    name: v.name.trim(),
    appliesTo: REASON_MODULES.filter((m) => v.appliesTo.includes(m)),
    requiresComment: v.requiresComment,
    negativePostingAllowed: v.negativePostingAllowed,
    alternativePostingDateAllowed: v.alternativePostingDateAllowed,
    active: v.active,
  };
}

const flags: { key: "requiresComment" | "negativePostingAllowed" | "alternativePostingDateAllowed" | "active"; label: string; note: string }[] = [
  { key: "requiresComment", label: "Comment required", note: "Reversals with this code need a comment." },
  { key: "negativePostingAllowed", label: "Negative posting", note: "Reversals post storno lines that reduce the original side (the ledger must allow it)." },
  { key: "alternativePostingDateAllowed", label: "Other reversal date allowed", note: "When off, a reversal keeps the original posting date." },
  { key: "active", label: "Active", note: "Inactive codes stay on posted entries but can't be picked." },
];

function ReasonCodeForm({ initial, existing }: { initial: ReasonDraft; existing: boolean }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [values, setValues] = useState<ReasonDraft>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const update = (patch: Partial<ReasonDraft>) => setValues((prev) => ({ ...prev, ...patch }));
  const toggleModule = (m: string, on: boolean) =>
    update({ appliesTo: on ? [...values.appliesTo, m] : values.appliesTo.filter((x) => x !== m) });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateReason(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await putReasonCode(values.code, toReasonInput(values));
      await queryClient.invalidateQueries({ queryKey: ["ledger", "reason-codes"] });
      navigate("/ledger/reason-codes");
    } catch (error) {
      setFailure(error instanceof LedgerApiError ? `${error.message} (${error.code})` : (error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={submit} noValidate aria-label={existing ? `Edit reason code ${initial.code}` : "New reason code"}>
        <FormField id="rc-code" label="Code" hint={existing ? "The code can't be changed." : "Up to 10 capital letters, digits or underscores."} error={errors.code}>
          {(c) => (
            <Input
              {...c}
              className={styles.mono}
              value={values.code}
              readOnly={existing}
              maxLength={10}
              onChange={(e) => update({ code: e.target.value.toUpperCase() })}
              invalid={Boolean(errors.code)}
            />
          )}
        </FormField>
        <FormField id="rc-name" label="Name" error={errors.name}>
          {(c) => <Input {...c} value={values.name} maxLength={MAX_NAME} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
        </FormField>
        <fieldset className={styles.group} aria-describedby={errors.appliesTo ? "rc-modules-error" : undefined}>
          <legend className={styles.legend}>Applies to</legend>
          {REASON_MODULES.map((m) => (
            <span key={m} className={styles.check}>
              <Input id={`rc-module-${m}`} type="checkbox" checked={values.appliesTo.includes(m)} onChange={(e) => toggleModule(m, e.target.checked)} />
              <label htmlFor={`rc-module-${m}`}>{moduleLabel(m)}</label>
            </span>
          ))}
        </fieldset>
        {errors.appliesTo ? (
          <p id="rc-modules-error" role="alert" className={`${styles.note} ${styles.wide}`}>
            {errors.appliesTo}
          </p>
        ) : null}
        {flags.map((f) => (
          <div key={f.key} className={styles.formActions}>
            <span className={styles.check}>
              <Input id={`rc-${f.key}`} type="checkbox" checked={values[f.key]} onChange={(e) => update({ [f.key]: e.target.checked })} />
              <label htmlFor={`rc-${f.key}`}>{f.label}</label>
            </span>
            <span className={styles.note}>{f.note}</span>
          </div>
        ))}
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not save the reason code" live="alert">
              {failure}
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save reason code
          </Button>
          <Link className={buttonClassName("ghost")} to="/ledger/reason-codes">
            Cancel
          </Link>
        </div>
      </form>
    </Card>
  );
}

/** Create a shared reason code (FR-11.15). */
export function NewReasonCodePage() {
  return (
    <div className={styles.stack}>
      <PageHeader title="New reason code" description="Pick the modules that may use it and how reversals with it behave." />
      <ReasonCodeForm initial={emptyReason} existing={false} />
    </div>
  );
}

function toDraft(r: ReasonCode): ReasonDraft {
  return {
    code: r.code,
    name: r.name,
    appliesTo: r.appliesTo,
    requiresComment: r.requiresComment,
    negativePostingAllowed: r.negativePostingAllowed,
    alternativePostingDateAllowed: r.alternativePostingDateAllowed,
    active: r.active,
  };
}

/** Edit or deactivate an existing reason code. */
export function EditReasonCodePage() {
  const { code = "" } = useParams();
  const result = useQuery({ queryKey: ["ledger", "reason-codes", code], queryFn: () => fetchReasonCode(code) });

  return (
    <div className={styles.stack}>
      <PageHeader title={`Reason code ${code}`} description="Changes apply to future reversals; posted entries keep the code they were reversed with." />
      {result.isError ? (
        <Alert tone="danger" title="Could not load the reason code" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : result.isPending ? (
        <Card>
          <div aria-busy="true">
            <span className="visually-hidden">Loading the reason code…</span>
            <Skeleton height="control" />
            <Skeleton height="control" width="1/2" />
          </div>
        </Card>
      ) : (
        <ReasonCodeForm initial={toDraft(result.data)} existing />
      )}
    </div>
  );
}
