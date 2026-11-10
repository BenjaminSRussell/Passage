import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, Card, FormField, Input, PageHeader, Select, Skeleton, Textarea } from "../../shell/ui";
import { fetchValidationRule, putValidationRule, RULE_OPS, type RuleCondition, type RuleOp, type ValidationRule, type ValidationRuleInput } from "./api/client";
import { ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

const MAX_NAME = 80;
const MAX_MESSAGE = 200;
const MAX_CONDITIONS = 10;
const NO_VALUE: RuleOp[] = ["blank", "notblank"];

export interface RuleDraft {
  sequence: string;
  name: string;
  level: "line" | "header";
  severity: "error" | "warning";
  prerequisite: string;
  checks: string;
  message: string;
  active: boolean;
}

export const emptyRule: RuleDraft = { sequence: "10", name: "", level: "line", severity: "error", prerequisite: "", checks: "", message: "", active: true };

type Errors = Record<string, string>;

/**
 * Conditions are typed one per line as `FIELD operator value, value`, for example
 * `AMOUNT gt 1000`, `ACCOUNT between 6000, 6999` or `DEPT notblank`. Returns the
 * conditions or the first problem with its line number.
 */
export function parseConditions(text: string): { conditions: RuleCondition[]; error?: string } {
  const conditions: RuleCondition[] = [];
  const lines = text.split(/\r?\n/);
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim();
    if (line === "") continue;
    const where = `Line ${i + 1}: `;
    const match = line.match(/^(\S+)\s+(\S+)\s*(.*)$/);
    if (!match) return { conditions, error: `${where}write the field, the operator and the values, such as AMOUNT gt 1000.` };
    const field = match[1].toUpperCase();
    const op = match[2].toLowerCase() as RuleOp;
    if (!RULE_OPS.includes(op)) return { conditions, error: `${where}the operator is one of ${RULE_OPS.join(", ")}.` };
    const values = match[3].split(",").map((v) => v.trim()).filter((v) => v !== "");
    if (NO_VALUE.includes(op) && values.length > 0) return { conditions, error: `${where}${op} takes no values.` };
    if (op === "between" && values.length !== 2) return { conditions, error: `${where}between needs a lower and an upper value.` };
    if (op === "in" && (values.length < 1 || values.length > 20)) return { conditions, error: `${where}in needs 1 to 20 values.` };
    if (["eq", "ne", "gt", "gte", "lt", "lte"].includes(op) && values.length !== 1) return { conditions, error: `${where}${op} needs exactly one value.` };
    conditions.push({ field, op, ...(values.length > 0 ? { values } : {}) });
  }
  return { conditions };
}

/** One condition as the line the editor shows. */
export function formatCondition(c: RuleCondition): string {
  return [c.field, c.op, (c.values ?? []).join(", ")].join(" ").trim();
}

/** Client checks that mirror PUT .../validation-rules/{id}; the service checks the field names against the ledger. */
export function validateRule(v: RuleDraft): Errors {
  const errors: Errors = {};
  const name = v.name.trim();
  if (name === "" || name.length > MAX_NAME) errors.name = `Enter a name of up to ${MAX_NAME} characters.`;
  const seq = Number(v.sequence);
  if (!/^\d+$/.test(v.sequence.trim()) || seq < 1 || seq > 9999) errors.sequence = "Enter a whole number from 1 to 9999.";
  const message = v.message.trim();
  if (message === "" || message.length > MAX_MESSAGE) errors.message = `Enter the message users see, up to ${MAX_MESSAGE} characters.`;
  const checks = parseConditions(v.checks);
  if (checks.error) errors.checks = checks.error;
  else if (checks.conditions.length < 1) errors.checks = "Add at least one check.";
  else if (checks.conditions.length > MAX_CONDITIONS) errors.checks = `At most ${MAX_CONDITIONS} checks.`;
  const pre = parseConditions(v.prerequisite);
  if (pre.error) errors.prerequisite = pre.error;
  else if (pre.conditions.length > MAX_CONDITIONS) errors.prerequisite = `At most ${MAX_CONDITIONS} conditions.`;
  return errors;
}

export function toRuleInput(v: RuleDraft): ValidationRuleInput {
  return {
    sequence: Number(v.sequence),
    name: v.name.trim(),
    level: v.level,
    severity: v.severity,
    prerequisite: parseConditions(v.prerequisite).conditions,
    checks: parseConditions(v.checks).conditions,
    message: v.message.trim(),
    active: v.active,
  };
}

export function toRuleDraft(r: ValidationRule): RuleDraft {
  return {
    sequence: String(r.sequence),
    name: r.name,
    level: r.level,
    severity: r.severity,
    prerequisite: r.prerequisite.map(formatCondition).join("\n"),
    checks: r.checks.map(formatCondition).join("\n"),
    message: r.message,
    active: r.active,
  };
}

function RuleForm({ ledgerId, ruleId, initial, existing }: { ledgerId: string; ruleId: string; initial: RuleDraft; existing?: ValidationRule }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [values, setValues] = useState(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<ReactNode>(null);
  const [busy, setBusy] = useState(false);
  const update = (patch: Partial<RuleDraft>) => setValues((prev) => ({ ...prev, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateRule(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await putValidationRule(ledgerId, ruleId, toRuleInput(values), existing?.lockVersion);
      await queryClient.invalidateQueries({ queryKey: ["ledger", "validation-rules"] });
      navigate(`/ledger/validation-rules${ledgerQuery(ledgerId)}`);
    } catch (e) {
      setFailure(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label={existing ? `Edit validation rule ${initial.name}` : "New validation rule"}>
        <FormField id="vr-name" label="Name" error={errors.name}>
          {(c) => <Input {...c} value={values.name} maxLength={MAX_NAME} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
        </FormField>
        <FormField id="vr-sequence" label="Order" hint="Rules run in ascending order." error={errors.sequence}>
          {(c) => <Input {...c} inputMode="numeric" value={values.sequence} onChange={(e) => update({ sequence: e.target.value })} invalid={Boolean(errors.sequence)} />}
        </FormField>
        <FormField id="vr-level" label="Applies to" hint="A whole-entry rule can read header fields only.">
          {(c) => (
            <Select {...c} value={values.level} onChange={(e) => update({ level: e.target.value as RuleDraft["level"] })}>
              <option value="line">Each line</option>
              <option value="header">Whole entry</option>
            </Select>
          )}
        </FormField>
        <FormField id="vr-severity" label="When the check fails">
          {(c) => (
            <Select {...c} value={values.severity} onChange={(e) => update({ severity: e.target.value as RuleDraft["severity"] })}>
              <option value="error">Refuse the entry</option>
              <option value="warning">Warn only</option>
            </Select>
          )}
        </FormField>
        <div className={styles.wide}>
          <FormField
            id="vr-prerequisite"
            label="Only when"
            optional
            hint="One condition per line, all must hold: FIELD operator values. Fields: ACCOUNT, ACCOUNT_TYPE, AMOUNT, SIDE, TEXT, dimensions of the ledger, SOURCE_SYSTEM, SOURCE_TYPE, POSTING_DATE, DOCUMENT_DATE, TOTAL_DEBIT, LINE_COUNT. Operators: eq, ne, in, gt, gte, lt, lte, between, blank, notblank."
            error={errors.prerequisite}
          >
            {(c) => <Textarea {...c} mono rows={3} placeholder={"ACCOUNT between 6000, 6999\nAMOUNT gt 1000"} value={values.prerequisite} onChange={(e) => update({ prerequisite: e.target.value })} invalid={Boolean(errors.prerequisite)} />}
          </FormField>
        </div>
        <div className={styles.wide}>
          <FormField id="vr-checks" label="Must hold" hint="One condition per line, all must hold, otherwise the rule fails." error={errors.checks}>
            {(c) => <Textarea {...c} mono rows={3} placeholder="DEPT notblank" value={values.checks} onChange={(e) => update({ checks: e.target.value })} invalid={Boolean(errors.checks)} />}
          </FormField>
        </div>
        <div className={styles.wide}>
          <FormField id="vr-message" label="Message" hint="Shown to the person posting, with the rule and line." error={errors.message}>
            {(c) => <Input {...c} value={values.message} maxLength={MAX_MESSAGE} onChange={(e) => update({ message: e.target.value })} invalid={Boolean(errors.message)} />}
          </FormField>
        </div>
        <div className={styles.formActions}>
          <span className={styles.check}>
            <Input id="vr-active" type="checkbox" checked={values.active} onChange={(e) => update({ active: e.target.checked })} />
            <label htmlFor="vr-active">Active</label>
          </span>
          <span className={styles.note}>An inactive rule is kept but not run.</span>
        </div>
        {failure ? (
          <div className={styles.wide}>
            <Alert tone="danger" title="Could not save the validation rule" live="alert">{failure}</Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>Save validation rule</Button>
          <Link to={`/ledger/validation-rules${ledgerQuery(ledgerId)}`}>Cancel</Link>
        </div>
      </form>
    </Card>
  );
}

function NeedsLedger() {
  return <p className={styles.note}>Open this page from the validation rules list of a ledger so it knows which ledger to use.</p>;
}

export function NewValidationRulePage() {
  const [ledgerId] = useLedgerParam();
  const [id] = useState(() => crypto.randomUUID());
  return (
    <div className={styles.stack}>
      <PageHeader title="New validation rule" />
      {ledgerId ? <RuleForm ledgerId={ledgerId} ruleId={id} initial={emptyRule} /> : <NeedsLedger />}
    </div>
  );
}

export function EditValidationRulePage() {
  const { id = "" } = useParams();
  const [ledgerId] = useLedgerParam();
  const rule = useQuery({ queryKey: ["ledger", "validation-rules", ledgerId, id], queryFn: () => fetchValidationRule(ledgerId, id), enabled: ledgerId !== "" });
  return (
    <div className={styles.stack}>
      <PageHeader title={rule.data ? `Validation rule ${rule.data.name}` : "Validation rule"} />
      {ledgerId === "" ? (
        <NeedsLedger />
      ) : rule.isPending ? (
        <Skeleton height="tile" />
      ) : rule.isError ? (
        <Alert tone="danger" title="Could not load the validation rule" live="alert" action={<Button size="sm" onClick={() => void rule.refetch()}>Try again</Button>}>
          {errorText(rule.error)}
        </Alert>
      ) : (
        <RuleForm key={rule.data.lockVersion} ledgerId={ledgerId} ruleId={id} existing={rule.data} initial={toRuleDraft(rule.data)} />
      )}
    </div>
  );
}
