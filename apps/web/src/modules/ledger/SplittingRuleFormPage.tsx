import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, Card, FormField, Input, PageHeader, Skeleton } from "../../shell/ui";
import { fetchSplittingRule, putSplittingRule, type SplittingRule, type SplittingRuleInput } from "./api/client";
import { ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

const DIM_VALUE = /^[A-Za-z0-9._-]{1,20}$/;
const ACCOUNT = /^[A-Za-z0-9._-]{1,20}$/;

export interface SplittingDraft {
  sourceType: string;
  dimensions: string;
  clearingAccount: string;
  defaults: string;
  active: boolean;
}

export const emptySplitting: SplittingDraft = { sourceType: "", dimensions: "", clearingAccount: "", defaults: "", active: true };

type Errors = Record<string, string>;

function names(text: string): string[] {
  return text.split(",").map((p) => p.trim()).filter((p) => p !== "");
}

/** "PROFIT_CENTER=PC9, SEGMENT=S0" to a map; returns the first malformed pair as an error. */
export function parseDefaults(text: string): { values: Record<string, string>; error?: string } {
  const values: Record<string, string> = {};
  for (const pair of names(text)) {
    const [dim, value, ...rest] = pair.split("=").map((p) => p.trim());
    if (!dim || value === undefined || rest.length > 0 || !DIM_VALUE.test(value)) {
      return { values, error: `"${pair}" must be written DIMENSION=value, the value up to 20 letters, digits, dots, underscores or dashes.` };
    }
    values[dim] = value;
  }
  return { values };
}

/** Client checks that mirror PUT .../splitting-rules/{id}; the service checks the names against the ledger and chart. */
export function validateSplitting(v: SplittingDraft): Errors {
  const errors: Errors = {};
  const source = v.sourceType.trim();
  if (source === "" || source.length > 60) errors.sourceType = "Enter the source type, up to 60 characters, such as vendor_invoice.";
  const dims = names(v.dimensions);
  if (dims.length < 1 || dims.length > 5) errors.dimensions = "List 1 to 5 dimensions, separated by commas.";
  else if (new Set(dims).size !== dims.length) errors.dimensions = "A dimension is listed twice.";
  if (v.clearingAccount.trim() !== "" && !ACCOUNT.test(v.clearingAccount.trim())) errors.clearingAccount = "Enter an account number of up to 20 letters, digits, dots, underscores or dashes.";
  const parsed = parseDefaults(v.defaults);
  if (parsed.error) errors.defaults = parsed.error;
  else if (!errors.dimensions && Object.keys(parsed.values).some((d) => !dims.includes(d))) errors.defaults = "A default can only name one of the splitting dimensions.";
  return errors;
}

export function toSplittingInput(v: SplittingDraft): SplittingRuleInput {
  const account = v.clearingAccount.trim();
  return {
    sourceType: v.sourceType.trim(),
    splittingDimensions: names(v.dimensions),
    ...(account ? { zeroBalanceClearingAccount: account } : {}),
    defaultSplittingValue: parseDefaults(v.defaults).values,
    active: v.active,
  };
}

export function toSplittingDraft(r: SplittingRule): SplittingDraft {
  return {
    sourceType: r.sourceType,
    dimensions: r.splittingDimensions.join(", "),
    clearingAccount: r.zeroBalanceClearingAccount ?? "",
    defaults: Object.entries(r.defaultSplittingValue).map(([d, v]) => `${d}=${v}`).join(", "),
    active: r.active,
  };
}

function SplittingForm({ ledgerId, ruleId, initial, existing }: { ledgerId: string; ruleId: string; initial: SplittingDraft; existing?: SplittingRule }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [values, setValues] = useState(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const update = (patch: Partial<SplittingDraft>) => setValues((prev) => ({ ...prev, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateSplitting(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await putSplittingRule(ledgerId, ruleId, toSplittingInput(values), existing?.version);
      await queryClient.invalidateQueries({ queryKey: ["ledger", "splitting-rules"] });
      navigate(`/ledger/splitting-rules${ledgerQuery(ledgerId)}`);
    } catch (e) {
      setFailure(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label={existing ? `Edit splitting rule ${initial.sourceType}` : "New splitting rule"}>
        <FormField id="sp-source" label="Source type" hint="The source type of the entries this rule applies to. One rule per source type." error={errors.sourceType}>
          {(c) => <Input {...c} className={styles.mono} maxLength={60} placeholder="vendor_invoice" value={values.sourceType} onChange={(e) => update({ sourceType: e.target.value })} invalid={Boolean(errors.sourceType)} />}
        </FormField>
        <FormField id="sp-dims" label="Splitting dimensions" hint="Dimensions of this ledger that lines without a value inherit, such as PROFIT_CENTER." error={errors.dimensions}>
          {(c) => <Input {...c} className={styles.mono} placeholder="PROFIT_CENTER" value={values.dimensions} onChange={(e) => update({ dimensions: e.target.value.toUpperCase() })} invalid={Boolean(errors.dimensions)} />}
        </FormField>
        <FormField id="sp-clearing" label="Zero-balance clearing account" optional hint="A balance-sheet account that takes the lines making each dimension value net to zero. Needed when an entry moves value between dimension values." error={errors.clearingAccount}>
          {(c) => <Input {...c} className={styles.mono} value={values.clearingAccount} onChange={(e) => update({ clearingAccount: e.target.value })} invalid={Boolean(errors.clearingAccount)} />}
        </FormField>
        <FormField id="sp-defaults" label="Default values" optional hint="Used when no line carries a value, written DIMENSION=value and separated by commas." error={errors.defaults}>
          {(c) => <Input {...c} className={styles.mono} placeholder="PROFIT_CENTER=PC9" value={values.defaults} onChange={(e) => update({ defaults: e.target.value })} invalid={Boolean(errors.defaults)} />}
        </FormField>
        <div className={styles.formActions}>
          <span className={styles.check}>
            <Input id="sp-active" type="checkbox" checked={values.active} onChange={(e) => update({ active: e.target.checked })} />
            <label htmlFor="sp-active">Active</label>
          </span>
          <span className={styles.note}>An inactive rule is kept but entries are not split.</span>
        </div>
        {failure ? (
          <div className={styles.wide}>
            <Alert tone="danger" title="Could not save the splitting rule" live="alert">{failure}</Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>Save splitting rule</Button>
          <Link to={`/ledger/splitting-rules${ledgerQuery(ledgerId)}`}>Cancel</Link>
        </div>
      </form>
    </Card>
  );
}

function NeedsLedger() {
  return <p className={styles.note}>Open this page from the splitting rules list of a ledger so it knows which ledger to use.</p>;
}

export function NewSplittingRulePage() {
  const [ledgerId] = useLedgerParam();
  const [id] = useState(() => crypto.randomUUID());
  return (
    <div className={styles.stack}>
      <PageHeader title="New splitting rule" />
      {ledgerId ? <SplittingForm ledgerId={ledgerId} ruleId={id} initial={emptySplitting} /> : <NeedsLedger />}
    </div>
  );
}

export function EditSplittingRulePage() {
  const { id = "" } = useParams();
  const [ledgerId] = useLedgerParam();
  const rule = useQuery({ queryKey: ["ledger", "splitting-rules", ledgerId, id], queryFn: () => fetchSplittingRule(ledgerId, id), enabled: ledgerId !== "" });
  return (
    <div className={styles.stack}>
      <PageHeader title={rule.data ? `Splitting rule ${rule.data.sourceType}` : "Splitting rule"} />
      {ledgerId === "" ? (
        <NeedsLedger />
      ) : rule.isPending ? (
        <Skeleton height="tile" />
      ) : rule.isError ? (
        <Alert tone="danger" title="Could not load the splitting rule" live="alert" action={<Button size="sm" onClick={() => void rule.refetch()}>Try again</Button>}>
          {errorText(rule.error)}
        </Alert>
      ) : (
        <SplittingForm key={rule.data.version} ledgerId={ledgerId} ruleId={id} existing={rule.data} initial={toSplittingDraft(rule.data)} />
      )}
    </div>
  );
}
