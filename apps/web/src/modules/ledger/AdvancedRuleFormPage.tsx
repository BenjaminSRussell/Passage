import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, Card, FormField, Input, PageHeader, Select, Skeleton, Textarea } from "../../shell/ui";
import { fetchAdvancedRule, fetchStructures, putAdvancedRule, type AdvancedRule, type AdvancedRuleInput, type Constraint } from "./api/client";
import { structureLabel } from "./AdvancedRulesPage";
import { ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";
import { formatUnder, parseUnder, splitList, validToken } from "./StructureFormPage";

const ATTRIBUTE = /^[A-Z][A-Z0-9_]{0,19}$/;
const NAME = /^[A-Za-z0-9_.-]{1,60}$/;

export interface AdvancedRuleDraft {
  structureId: string;
  name: string;
  priority: string;
  accounts: string;
  dimensions: string;
  segments: string;
  constraints: string;
}

export const emptyAdvancedRule: AdvancedRuleDraft = { structureId: "", name: "", priority: "0", accounts: "", dimensions: "", segments: "", constraints: "" };

type Errors = Record<string, string>;

/**
 * Constraint rows are typed one per line as `SEGMENT: token, token` or
 * `SEGMENT under ATTR=token, ATTR=token: token, token`, for example
 * `PROJECT: *` or `PROJECT under MAIN_ACCOUNT=5000..5999: P1, P2, blank`.
 */
export function parseRuleConstraints(text: string): { rows: Constraint[]; error?: string } {
  const rows: Constraint[] = [];
  const lines = text.split(/\r?\n/);
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim();
    if (line === "") continue;
    const where = `Line ${i + 1}: `;
    const match = line.match(/^(\S+?)(?:\s+under\s+([^:]+?))?\s*:\s*(.*)$/i);
    if (!match) return { rows, error: `${where}write SEGMENT: allowed values, or SEGMENT under ATTR=value: allowed values.` };
    const segment = match[1].toUpperCase();
    const under = match[2] === undefined ? undefined : parseUnder(match[2]);
    if (under === null) return { rows, error: `${where}conditions are ATTR=value pairs separated by commas.` };
    const allowed = splitList(match[3]);
    if (allowed.length === 0) return { rows, error: `${where}list at least one allowed value.` };
    if (!allowed.every(validToken)) return { rows, error: `${where}allowed values are exact values, from..to ranges, * or blank.` };
    rows.push({ segment, ...(under && Object.keys(under).length > 0 ? { under } : {}), allowed });
  }
  return { rows };
}

export function formatRuleConstraint(c: Constraint): string {
  const under = formatUnder(c.under);
  return `${c.segment}${under ? ` under ${under}` : ""}: ${c.allowed.join(", ")}`;
}

/** Client checks that mirror PUT .../advanced-rules/{id}; the service checks attributes against the ledger and the structure. */
export function validateAdvancedRule(v: AdvancedRuleDraft): Errors {
  const errors: Errors = {};
  if (v.structureId === "") errors.structureId = "Choose the draft account structure version this rule belongs to.";
  if (!NAME.test(v.name.trim())) errors.name = "Enter 1 to 60 characters of letters, digits, '.', '_' or '-'.";
  if (!/^\d+$/.test(v.priority.trim())) errors.priority = "Enter a whole number, 0 or more.";
  const accounts = splitList(v.accounts);
  if (accounts.some((t) => t === "blank" || !validToken(t))) errors.accounts = "Use account numbers, from..to ranges or *.";
  const dims = parseUnder(v.dimensions);
  if (dims === null) errors.dimensions = "Write conditions as ATTR=value, separated by commas.";
  else if (Object.keys(dims).some((k) => !ATTRIBUTE.test(k)) || Object.values(dims).some((t) => !validToken(t))) errors.dimensions = "Attributes are upper case; values are exact, from..to, * or blank.";
  if (!errors.accounts && !errors.dimensions && accounts.length === 0 && Object.keys(dims ?? {}).length === 0) errors.accounts = "Give at least one account or dimension condition.";
  const segments = splitList(v.segments);
  if (segments.length === 0) errors.segments = "Add at least one dimension.";
  else if (segments.some((s) => !ATTRIBUTE.test(s))) errors.segments = "Dimensions are upper-case attribute names such as PROJECT.";
  else if (new Set(segments).size !== segments.length) errors.segments = "Each dimension once.";
  const rows = parseRuleConstraints(v.constraints);
  if (rows.error) errors.constraints = rows.error;
  else if (rows.rows.some((r) => !segments.includes(r.segment))) errors.constraints = "A row can constrain only a dimension this rule adds.";
  return errors;
}

export function toAdvancedRuleInput(v: AdvancedRuleDraft): AdvancedRuleInput {
  return {
    structureId: v.structureId,
    name: v.name.trim(),
    priority: Number(v.priority),
    accounts: splitList(v.accounts),
    dimensions: parseUnder(v.dimensions) ?? {},
    segments: splitList(v.segments),
    constraints: parseRuleConstraints(v.constraints).rows,
  };
}

export function toAdvancedRuleDraft(r: AdvancedRule): AdvancedRuleDraft {
  return {
    structureId: r.structureId,
    name: r.name,
    priority: String(r.priority),
    accounts: r.accounts.join(", "),
    dimensions: formatUnder(r.dimensions),
    segments: r.segments.join(", "),
    constraints: r.constraints.map(formatRuleConstraint).join("\n"),
  };
}

function RuleForm({ ledgerId, ruleId, initial, existing }: { ledgerId: string; ruleId: string; initial: AdvancedRuleDraft; existing?: AdvancedRule }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const structures = useQuery({ queryKey: ["ledger", "structures", ledgerId], queryFn: () => fetchStructures(ledgerId) });
  const [values, setValues] = useState(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<ReactNode>(null);
  const [busy, setBusy] = useState(false);
  const update = (patch: Partial<AdvancedRuleDraft>) => setValues((prev) => ({ ...prev, ...patch }));
  const all = structures.data ?? [];
  const chosen = all.find((s) => s.id === values.structureId);
  const locked = chosen !== undefined && chosen.status !== "draft";
  // A new rule can join a draft version only; an existing one keeps its version.
  const options = existing ? all.filter((s) => s.id === existing.structureId) : all.filter((s) => s.status === "draft");

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateAdvancedRule(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await putAdvancedRule(ledgerId, ruleId, toAdvancedRuleInput(values), existing?.lockVersion);
      await queryClient.invalidateQueries({ queryKey: ["ledger", "advanced-rules"] });
      navigate(`/ledger/advanced-rules${ledgerQuery(ledgerId)}`);
    } catch (e) {
      setFailure(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label={existing ? `Edit advanced rule ${initial.name}` : "New advanced rule"}>
        {locked ? (
          <div className={styles.wide}>
            <Alert tone="info" title="This structure version is no longer a draft">
              Its advanced rules cannot change. Create a new version of the account structure to change them.
            </Alert>
          </div>
        ) : null}
        {structures.isError ? (
          <div className={styles.wide}>
            <Alert tone="danger" title="Could not load the account structures" live="alert" action={<Button size="sm" onClick={() => void structures.refetch()}>Try again</Button>}>
              {errorText(structures.error)}
            </Alert>
          </div>
        ) : structures.isSuccess && !existing && options.length === 0 ? (
          <div className={styles.wide}>
            <Alert tone="info" title="No draft account structure version">
              Advanced rules join a draft version of an account structure. <Link to={`/ledger/structures${ledgerQuery(ledgerId)}`}>Create a draft version</Link> first.
            </Alert>
          </div>
        ) : null}
        <FormField id="ar-structure" label="Account structure version" hint="Only drafts take new rules." error={errors.structureId}>
          {(c) => (
            <Select {...c} value={values.structureId} disabled={existing !== undefined || structures.isPending} onChange={(e) => update({ structureId: e.target.value })}>
              <option value="">{structures.isPending ? "Loading…" : "Choose a version"}</option>
              {options.map((s) => (
                <option key={s.id} value={s.id}>
                  {structureLabel(all, s.id)} ({s.status})
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="ar-name" label="Name" error={errors.name}>
          {(c) => <Input {...c} value={values.name} maxLength={60} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
        </FormField>
        <FormField id="ar-priority" label="Priority" hint="When several rules match, they are added lowest first." error={errors.priority}>
          {(c) => <Input {...c} inputMode="numeric" value={values.priority} onChange={(e) => update({ priority: e.target.value })} invalid={Boolean(errors.priority)} />}
        </FormField>
        <FormField id="ar-accounts" label="Accounts" optional hint="Account numbers, from..to ranges or *, separated by commas." error={errors.accounts}>
          {(c) => <Input {...c} placeholder="5000..5999" value={values.accounts} onChange={(e) => update({ accounts: e.target.value })} invalid={Boolean(errors.accounts)} />}
        </FormField>
        <FormField id="ar-dimensions" label="Dimension conditions" optional hint="ATTR=value pairs that must all match, for example DEPT=D100." error={errors.dimensions}>
          {(c) => <Input {...c} placeholder="DEPT=D100" value={values.dimensions} onChange={(e) => update({ dimensions: e.target.value })} invalid={Boolean(errors.dimensions)} />}
        </FormField>
        <FormField id="ar-segments" label="Dimensions added" hint="Attributes the matching lines also carry, separated by commas." error={errors.segments}>
          {(c) => <Input {...c} placeholder="PROJECT" value={values.segments} onChange={(e) => update({ segments: e.target.value })} invalid={Boolean(errors.segments)} />}
        </FormField>
        <div className={styles.wide}>
          <FormField
            id="ar-constraints"
            label="Allowed values"
            optional
            hint="One row per line: DIMENSION: values, or DIMENSION under ATTR=value: values. Values are exact, from..to, * (any value, required) or blank (may stay empty). A dimension with no row is free."
            error={errors.constraints}
          >
            {(c) => <Textarea {...c} mono rows={4} placeholder="PROJECT: *" value={values.constraints} onChange={(e) => update({ constraints: e.target.value })} invalid={Boolean(errors.constraints)} />}
          </FormField>
        </div>
        {failure ? (
          <div className={styles.wide}>
            <Alert tone="danger" title="Could not save the advanced rule" live="alert">{failure}</Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy} disabled={locked}>Save advanced rule</Button>
          <Link to={`/ledger/advanced-rules${ledgerQuery(ledgerId)}`}>Cancel</Link>
        </div>
      </form>
    </Card>
  );
}

function NeedsLedger() {
  return <p className={styles.note}>Open this page from the advanced rules list of a ledger so it knows which ledger to use.</p>;
}

export function NewAdvancedRulePage() {
  const [ledgerId] = useLedgerParam();
  const [id] = useState(() => crypto.randomUUID());
  return (
    <div className={styles.stack}>
      <PageHeader title="New advanced rule" />
      {ledgerId ? <RuleForm ledgerId={ledgerId} ruleId={id} initial={emptyAdvancedRule} /> : <NeedsLedger />}
    </div>
  );
}

export function EditAdvancedRulePage() {
  const { id = "" } = useParams();
  const [ledgerId] = useLedgerParam();
  const rule = useQuery({ queryKey: ["ledger", "advanced-rules", ledgerId, id], queryFn: () => fetchAdvancedRule(ledgerId, id), enabled: ledgerId !== "" });
  return (
    <div className={styles.stack}>
      <PageHeader title={rule.data ? `Advanced rule ${rule.data.name}` : "Advanced rule"} />
      {ledgerId === "" ? (
        <NeedsLedger />
      ) : rule.isPending ? (
        <Skeleton height="tile" />
      ) : rule.isError ? (
        <Alert tone="danger" title="Could not load the advanced rule" live="alert" action={<Button size="sm" onClick={() => void rule.refetch()}>Try again</Button>}>
          {errorText(rule.error)}
        </Alert>
      ) : (
        <RuleForm key={rule.data.lockVersion} ledgerId={ledgerId} ruleId={id} existing={rule.data} initial={toAdvancedRuleDraft(rule.data)} />
      )}
    </div>
  );
}
