import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { CODE, createRoutingRule, describeProblem, DOCUMENT_TYPES, newKey, type DocumentType, type NewRoutingRule, type Problem } from "./api/print";
import { DOCUMENT_LABELS } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export type Scope = "default" | "userId" | "zone" | "warehouseId";

export interface RuleValues {
  documentType: DocumentType;
  scope: Scope;
  scopeValue: string;
  queueCode: string;
  sequence: string;
}

type Field = "scopeValue" | "queueCode" | "sequence";
type Errors = Partial<Record<Field, string>>;

const SCOPE_LABELS: Record<Scope, string> = {
  default: "Every user and place (default)",
  userId: "One user",
  zone: "One zone",
  warehouseId: "One warehouse",
};

/** Checks that mirror the service, and the request the form sends. */
export function validateRule(values: RuleValues): { errors: Errors; input?: NewRoutingRule } {
  const errors: Errors = {};
  const scopeValue = values.scopeValue.trim();
  const queue = values.queueCode.trim();
  if (values.scope !== "default" && scopeValue === "") errors.scopeValue = "Enter the user, zone or warehouse the rule applies to.";
  if (!CODE.test(queue)) errors.queueCode = "Use 2 to 40 capital letters, digits, _ or -, such as DOCK.";
  if (!/^\d{1,6}$/.test(values.sequence.trim()) || Number(values.sequence) < 1) errors.sequence = "Enter a whole number of 1 or more.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewRoutingRule = { documentType: values.documentType, queueCode: queue, sequence: Number(values.sequence) };
  if (values.scope !== "default") input[values.scope] = scopeValue;
  return { errors, input };
}

/** Add a document routing rule (FR-32.13, BR-32.12). */
export function NewRoutingRulePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<RuleValues>({ documentType: "LP_LABEL", scope: "default", scopeValue: "", queueCode: "", sequence: "1" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<RuleValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateRule(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createRoutingRule(input, key.current);
      navigate("/mobile-flows/routing-rules");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      if (problem.fields.queueCode) fieldErrors.queueCode = problem.fields.queueCode;
      if (problem.fields.sequence) fieldErrors.sequence = problem.fields.sequence;
      if (problem.fields.scope) fieldErrors.scopeValue = problem.fields.scope;
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Add routing rule" description="A rule sends one document type to a queue for everyone, or for one user, zone or warehouse." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="rule-documentType" label="Document">
            {(control) => (
              <Select {...control} value={values.documentType} onChange={(event) => update({ documentType: event.target.value as DocumentType })}>
                {DOCUMENT_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {DOCUMENT_LABELS[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="rule-scope" label="Applies to">
            {(control) => (
              <Select {...control} value={values.scope} onChange={(event) => update({ scope: event.target.value as Scope, scopeValue: "" })}>
                {(Object.keys(SCOPE_LABELS) as Scope[]).map((s) => (
                  <option key={s} value={s}>
                    {SCOPE_LABELS[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {values.scope !== "default" ? (
            <FormField
              id="rule-scopeValue"
              label={values.scope === "userId" ? "User" : values.scope === "zone" ? "Zone" : "Warehouse"}
              error={errors.scopeValue}
            >
              {(control) => (
                <Input {...control} value={values.scopeValue} onChange={(event) => update({ scopeValue: event.target.value })} invalid={Boolean(errors.scopeValue)} />
              )}
            </FormField>
          ) : null}
          <FormField id="rule-queueCode" label="Queue" error={errors.queueCode}>
            {(control) => (
              <Input {...control} maxLength={40} value={values.queueCode} onChange={(event) => update({ queueCode: event.target.value })} invalid={Boolean(errors.queueCode)} />
            )}
          </FormField>
          <FormField id="rule-sequence" label="Sequence" hint="Breaks ties within the same scope; lowest wins." error={errors.sequence}>
            {(control) => (
              <Input {...control} inputMode="numeric" value={values.sequence} onChange={(event) => update({ sequence: event.target.value })} invalid={Boolean(errors.sequence)} />
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not add the rule" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Add rule
            </Button>
            <Link className={buttonClassName("ghost")} to="/mobile-flows/routing-rules">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
