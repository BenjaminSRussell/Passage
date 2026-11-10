import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, Card, FormField, Input, PageHeader, Select, Skeleton, Textarea } from "../../shell/ui";
import {
  AUTO_CRITERION,
  fetchAutoClearingRule,
  fetchToleranceGroups,
  MAX_AUTO_CRITERIA,
  newKey,
  putAutoClearingRule,
  TOLERANCE_CODE,
  type AutoClearingRule,
} from "./api/client";
import { ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

const MAX_NAME = 80;
const ACCOUNT = /^[A-Za-z0-9._-]{1,20}$/;

export interface AutoRuleDraft {
  name: string;
  accountFrom: string;
  accountTo: string;
  criteria: string;
  allowTolerance: boolean;
  toleranceGroup: string;
  sequence: string;
  active: boolean;
}

export const emptyAutoRule: AutoRuleDraft = { name: "", accountFrom: "", accountTo: "", criteria: "assignment", allowTolerance: false, toleranceGroup: "", sequence: "100", active: true };

/** The criteria field is one criterion per line, in order. */
export function parseCriteria(text: string): string[] {
  return text.split(/[\n,]/).map((c) => c.trim()).filter((c) => c !== "");
}

type Errors = Record<string, string>;

/** Client checks that mirror PUT .../auto-clearing-rules/{id}; the service has the last word. */
export function validateAutoRule(v: AutoRuleDraft): Errors {
  const errors: Errors = {};
  const name = v.name.trim();
  if (name === "" || name.length > MAX_NAME) errors.name = `Enter a name of up to ${MAX_NAME} characters.`;
  if (!ACCOUNT.test(v.accountFrom)) errors.accountFrom = "Enter the first account number, up to 20 letters, digits, dots, underscores or dashes.";
  if (!ACCOUNT.test(v.accountTo)) errors.accountTo = "Enter the last account number, up to 20 letters, digits, dots, underscores or dashes.";
  else if (!errors.accountFrom && v.accountFrom > v.accountTo) errors.accountTo = "The last account can't come before the first.";
  const criteria = parseCriteria(v.criteria);
  if (criteria.length < 1 || criteria.length > MAX_AUTO_CRITERIA) errors.criteria = `Give 1 to ${MAX_AUTO_CRITERIA} criteria, one per line.`;
  else if (criteria.some((c) => !AUTO_CRITERION.test(c)) || new Set(criteria).size !== criteria.length)
    errors.criteria = "Use each of assignment, source_line_ref, document_id, amount_txn, posting_date or dim.ATTRIBUTE at most once.";
  if (v.allowTolerance && !TOLERANCE_CODE.test(v.toleranceGroup)) errors.toleranceGroup = "Choose the tolerance group that limits the write-off.";
  const sequence = Number(v.sequence);
  if (!Number.isInteger(sequence) || sequence < 1 || sequence > 9999) errors.sequence = "Enter a whole number from 1 to 9999.";
  return errors;
}

function RuleForm({ ledgerId, id, initial, existing }: { ledgerId: string; id: string; initial: AutoRuleDraft; existing?: AutoClearingRule }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [values, setValues] = useState(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<ReactNode>(null);
  const [busy, setBusy] = useState(false);
  const groups = useQuery({ queryKey: ["ledger", "tolerance-groups", ledgerId], queryFn: () => fetchToleranceGroups(ledgerId) });
  const update = (patch: Partial<AutoRuleDraft>) => setValues((prev) => ({ ...prev, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateAutoRule(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await putAutoClearingRule(
        ledgerId,
        id,
        {
          name: values.name.trim(),
          accountFrom: values.accountFrom,
          accountTo: values.accountTo,
          criteria: parseCriteria(values.criteria),
          allowTolerance: values.allowTolerance,
          ...(values.allowTolerance ? { toleranceGroup: values.toleranceGroup } : {}),
          sequence: Number(values.sequence),
          active: values.active,
        },
        existing?.version,
      );
      await queryClient.invalidateQueries({ queryKey: ["ledger", "auto-clearing-rules"] });
      navigate(`/ledger/auto-clearing-rules${ledgerQuery(ledgerId)}`);
    } catch (e) {
      setFailure(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label={existing ? `Edit rule ${initial.name}` : "New automatic clearing rule"}>
        <FormField id="acr-name" label="Name" error={errors.name}>
          {(c) => <Input {...c} value={values.name} maxLength={MAX_NAME} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
        </FormField>
        <FormField id="acr-from" label="First account" hint="Account numbers are compared as text." error={errors.accountFrom}>
          {(c) => <Input {...c} className={styles.mono} value={values.accountFrom} onChange={(e) => update({ accountFrom: e.target.value })} invalid={Boolean(errors.accountFrom)} />}
        </FormField>
        <FormField id="acr-to" label="Last account" error={errors.accountTo}>
          {(c) => <Input {...c} className={styles.mono} value={values.accountTo} onChange={(e) => update({ accountTo: e.target.value })} invalid={Boolean(errors.accountTo)} />}
        </FormField>
        <FormField
          id="acr-criteria"
          label="Criteria"
          hint="One per line, up to 5: assignment, source_line_ref, document_id (the posting's document), amount_txn (absolute amount), posting_date (month) or dim.ATTRIBUTE. Items with no value for a criterion never group."
          error={errors.criteria}
        >
          {(c) => <Textarea {...c} className={styles.mono} rows={4} value={values.criteria} onChange={(e) => update({ criteria: e.target.value })} invalid={Boolean(errors.criteria)} />}
        </FormField>
        <FormField id="acr-sequence" label="Order" hint="Rules run from the lowest number up." error={errors.sequence}>
          {(c) => <Input {...c} className={styles.mono} inputMode="numeric" value={values.sequence} onChange={(e) => update({ sequence: e.target.value })} invalid={Boolean(errors.sequence)} />}
        </FormField>
        <span className={styles.check}>
          <Input id="acr-allow" type="checkbox" checked={values.allowTolerance} onChange={(e) => update({ allowTolerance: e.target.checked })} />
          <label htmlFor="acr-allow">Allow a tolerance group to write off a small difference</label>
        </span>
        {values.allowTolerance ? (
          <FormField id="acr-tolerance" label="Tolerance group" hint="Active groups of this ledger." error={errors.toleranceGroup}>
            {(c) => (
              <Select {...c} value={values.toleranceGroup} onChange={(e) => update({ toleranceGroup: e.target.value })} invalid={Boolean(errors.toleranceGroup)}>
                <option value="">Choose a group</option>
                {(groups.data ?? []).filter((g) => g.active || g.code === values.toleranceGroup).map((g) => (
                  <option key={g.code} value={g.code}>{g.code} - {g.name}</option>
                ))}
              </Select>
            )}
          </FormField>
        ) : null}
        <span className={styles.check}>
          <Input id="acr-active" type="checkbox" checked={values.active} onChange={(e) => update({ active: e.target.checked })} />
          <label htmlFor="acr-active">Active</label>
        </span>
        {failure ? (
          <div className={styles.wide}>
            <Alert tone="danger" title="Could not save the rule" live="alert">{failure}</Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>Save rule</Button>
          <Link to={`/ledger/auto-clearing-rules${ledgerQuery(ledgerId)}`}>Cancel</Link>
        </div>
      </form>
    </Card>
  );
}

function NeedsLedger() {
  return <p className={styles.note}>Open this page from the automatic clearing rules list of a ledger so it knows which ledger to use.</p>;
}

export function NewAutoClearingRulePage() {
  const [ledgerId] = useLedgerParam();
  // One id per opened form: a retry after a failed save replays the same create.
  const [id] = useState(newKey);
  return (
    <div className={styles.stack}>
      <PageHeader title="New automatic clearing rule" />
      {ledgerId ? <RuleForm ledgerId={ledgerId} id={id} initial={emptyAutoRule} /> : <NeedsLedger />}
    </div>
  );
}

export function EditAutoClearingRulePage() {
  const { id = "" } = useParams();
  const [ledgerId] = useLedgerParam();
  const rule = useQuery({ queryKey: ["ledger", "auto-clearing-rules", ledgerId, id], queryFn: () => fetchAutoClearingRule(ledgerId, id), enabled: ledgerId !== "" });
  return (
    <div className={styles.stack}>
      <PageHeader title={rule.data ? `Rule ${rule.data.name}` : "Automatic clearing rule"} />
      {ledgerId === "" ? (
        <NeedsLedger />
      ) : rule.isPending ? (
        <Skeleton height="tile" />
      ) : rule.isError ? (
        <Alert tone="danger" title="Could not load the rule" live="alert" action={<Button size="sm" onClick={() => void rule.refetch()}>Try again</Button>}>
          {errorText(rule.error)}
        </Alert>
      ) : (
        <RuleForm
          key={rule.data.version}
          ledgerId={ledgerId}
          id={id}
          existing={rule.data}
          initial={{
            name: rule.data.name,
            accountFrom: rule.data.accountFrom,
            accountTo: rule.data.accountTo,
            criteria: rule.data.criteria.join("\n"),
            allowTolerance: rule.data.allowTolerance,
            toleranceGroup: rule.data.toleranceGroup ?? "",
            sequence: String(rule.data.sequence),
            active: rule.data.active,
          }}
        />
      )}
    </div>
  );
}
