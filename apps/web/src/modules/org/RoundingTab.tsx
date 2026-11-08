import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, Select, type DataTableColumn } from "../../shell/ui";
import { ALL, describeProblem, type LegalEntity, type Problem } from "./api/client";
import { listCurrencies } from "./api/reference";
import {
  deleteRoundingRule,
  listRoundingRules,
  putRoundingRule,
  roundAmount,
  roundingMethods,
  roundingPurposes,
  type CurrencyRounding,
  type RoundResult,
  type RoundingMethod,
  type RoundingPurpose,
} from "./api/rounding";
import { useLegalEntity } from "./legalEntityContext";
import styles from "./pages.module.css";
import { FilterSelect, useRows } from "./referenceParts";

const unitPattern = /^[0-9]{1,12}(\.[0-9]{1,6})?$/;
const amountPattern = /^-?[0-9]{1,18}(\.[0-9]+)?$/;

const methodLabels: Record<RoundingMethod, string> = {
  half_even: "Half even (banker's)",
  half_up: "Half up",
  down: "Down",
  up: "Up",
};

const sourceLabels: Record<RoundResult["source"], string> = {
  legal_entity: "Legal-entity rule",
  tenant: "Tenant rule",
  default: "Currency default (minor unit, half even)",
};

const ruleMessages: Record<string, string> = {
  "ORG-CUR-ROUNDING-UNIT": "The unit must be a power of ten no finer than the currency's minor unit, or 5 or 25 times one.",
  "ORG-LE-REFERENCE-INACTIVE": "This currency is inactive for the tenant.",
  "ORG-CUR-ROUNDING-NOT-FOUND": "That rule no longer exists.",
};

const formatProblem = (problem: Problem) => `${ruleMessages[problem.code] ?? problem.message} (${problem.code})`;

export function validateRule(unit: string): string | undefined {
  if (!unitPattern.test(unit) || !/[1-9]/.test(unit)) {
    return "Enter a positive decimal with at most 6 decimal places, such as 0.05.";
  }
  return undefined;
}

export function validateAmount(amount: string): string | undefined {
  return amountPattern.test(amount) ? undefined : "Enter a decimal amount such as 10.075.";
}

function EntitySelect({ id, label, entities, value, blank, onChange }: { id: string; label: string; entities: LegalEntity[]; value: string; blank: string; onChange(value: string): void }) {
  return (
    <FormField id={id} label={label}>
      {(control) => (
        <Select {...control} value={value} onChange={(event) => onChange(event.target.value)}>
          <option value="">{blank}</option>
          {entities.map((entity) => (
            <option key={entity.id} value={entity.id}>
              {entity.code} - {entity.name}
            </option>
          ))}
        </Select>
      )}
    </FormField>
  );
}

function PurposeSelect({ id, label, value, onChange }: { id: string; label: string; value: RoundingPurpose; onChange(value: RoundingPurpose): void }) {
  return (
    <FormField id={id} label={label}>
      {(control) => (
        <Select {...control} value={value} onChange={(event) => onChange(event.target.value as RoundingPurpose)}>
          {roundingPurposes.map((purpose) => (
            <option key={purpose} value={purpose}>
              {purpose}
            </option>
          ))}
        </Select>
      )}
    </FormField>
  );
}

function RuleForm({ currency, entities, onSaved }: { currency: string; entities: LegalEntity[]; onSaved(): void }) {
  const [purpose, setPurpose] = useState<RoundingPurpose>("general");
  const [unit, setUnit] = useState("");
  const [method, setMethod] = useState<RoundingMethod>("half_even");
  const [entityId, setEntityId] = useState("");
  const [unitError, setUnitError] = useState<string>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validateRule(unit);
    setUnitError(found);
    setFailure(null);
    if (found) {
      return;
    }
    setBusy(true);
    try {
      await putRoundingRule(currency, purpose, { unit, method, legalEntityId: entityId || undefined });
      setUnit("");
      onSaved();
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "ORG-CUR-ROUNDING-UNIT") {
        setUnitError(formatProblem(problem));
      } else {
        setFailure(problem);
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title={`Set a rounding rule for ${currency}`}>
      <form onSubmit={submit} noValidate className={styles.form}>
        <PurposeSelect id="rule-purpose" label="Purpose" value={purpose} onChange={setPurpose} />
        <FormField id="rule-unit" label="Rounding unit" hint="For example 0.05 rounds to the nearest 5 cents." error={unitError}>
          {(control) => <Input {...control} value={unit} onChange={(event) => setUnit(event.target.value)} inputMode="decimal" />}
        </FormField>
        <FormField id="rule-method" label="Method">
          {(control) => (
            <Select {...control} value={method} onChange={(event) => setMethod(event.target.value as RoundingMethod)}>
              {roundingMethods.map((name) => (
                <option key={name} value={name}>
                  {methodLabels[name]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <EntitySelect id="rule-entity" label="Applies to" entities={entities} value={entityId} blank="Whole tenant" onChange={setEntityId} />
        {failure ? (
          <Alert tone="danger" title="Could not save the rule" live="alert">
            {formatProblem(failure)}
          </Alert>
        ) : null}
        <div>
          <Button type="submit" variant="primary" loading={busy}>
            Save rule
          </Button>
        </div>
      </form>
    </Card>
  );
}

function RemoveRule({ rule, onRemoved }: { rule: CurrencyRounding; onRemoved(): void }) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const subject = `${rule.purpose} rule`;

  const confirm = async () => {
    setBusy(true);
    setFailure(null);
    try {
      await deleteRoundingRule(rule.currencyCode, rule.purpose, rule.legalEntityId);
      onRemoved();
    } catch (error) {
      setFailure(describeProblem(error));
      setBusy(false);
    }
  };

  return (
    <div className={styles.rowActions}>
      {confirming ? (
        <>
          <Button size="sm" variant="danger" loading={busy} aria-label={`Confirm remove ${subject}`} onClick={confirm}>
            Confirm
          </Button>
          <Button size="sm" variant="ghost" aria-label={`Cancel remove ${subject}`} onClick={() => setConfirming(false)}>
            Cancel
          </Button>
        </>
      ) : (
        <Button size="sm" aria-label={`Remove ${subject}`} onClick={() => setConfirming(true)}>
          Remove
        </Button>
      )}
      {failure ? (
        <span role="alert" className={styles.rowError}>
          {formatProblem(failure)}
        </span>
      ) : null}
    </div>
  );
}

function TestRoundPanel({ currency, entities }: { currency: string; entities: LegalEntity[] }) {
  const [amount, setAmount] = useState("");
  const [purpose, setPurpose] = useState<RoundingPurpose>("general");
  const [entityId, setEntityId] = useState("");
  const [amountError, setAmountError] = useState<string>();
  const [result, setResult] = useState<RoundResult | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validateAmount(amount);
    setAmountError(found);
    setResult(null);
    setFailure(null);
    if (found) {
      return;
    }
    setBusy(true);
    try {
      setResult(await roundAmount(currency, amount, purpose, entityId || undefined));
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="Test rounding">
      <form onSubmit={submit} noValidate className={styles.form}>
        <FormField id="round-amount" label="Amount" error={amountError}>
          {(control) => <Input {...control} value={amount} onChange={(event) => setAmount(event.target.value)} inputMode="decimal" />}
        </FormField>
        <PurposeSelect id="round-purpose" label="Test purpose" value={purpose} onChange={setPurpose} />
        <EntitySelect id="round-entity" label="Test legal entity" entities={entities} value={entityId} blank="Tenant default" onChange={setEntityId} />
        <div>
          <Button type="submit" variant="primary" loading={busy}>
            Round
          </Button>
        </div>
      </form>
      <div role="status">
        {result ? (
          <dl className={styles.result}>
            <dt>Rounded amount</dt>
            <dd>{result.rounded}</dd>
            <dt>Unit</dt>
            <dd>{result.unit}</dd>
            <dt>Method</dt>
            <dd>{methodLabels[result.method]}</dd>
            <dt>Rule used</dt>
            <dd>{sourceLabels[result.source]}</dd>
          </dl>
        ) : null}
      </div>
      {failure ? (
        <Alert tone="danger" title="Could not round" live="alert">
          {formatProblem(failure)}
        </Alert>
      ) : null}
    </Card>
  );
}

export function RoundingTab() {
  const { canWriteReference, entities, scope } = useLegalEntity();
  const [currency, setCurrency] = useState("");
  const currencies = useRows((signal) => listCurrencies("active", { signal }), "active");
  const rules = useRows((signal) => (currency === "" || scope === "" ? Promise.resolve([]) : listRoundingRules(currency, scope, { signal })), `${currency}|${scope}`);
  const scopedEntity = scope === ALL || scope === "" ? undefined : entities.find((entity) => entity.id === scope);
  const entityCode = (id: string | null) => (id === null ? null : entities.find((entity) => entity.id === id)?.code ?? id);

  const columns: DataTableColumn<CurrencyRounding>[] = [
    { key: "purpose", header: "Purpose" },
    { key: "legalEntityId", header: "Applies to", render: (row) => (row.legalEntityId === null ? <Badge tone="info">Whole tenant</Badge> : <span>{entityCode(row.legalEntityId)}</span>) },
    { key: "unit", header: "Unit", align: "end", mono: true },
    { key: "method", header: "Method", render: (row) => methodLabels[row.method] },
    { key: "updatedAt", header: "Updated", render: (row) => row.updatedAt.slice(0, 10) },
  ];
  if (canWriteReference) {
    columns.push({ key: "actions", header: "Actions", render: (row) => <RemoveRule rule={row} onRemoved={rules.reload} /> });
  }

  return (
    <>
      {currencies.error ? (
        <Alert tone="danger" title="Could not load currencies" live="alert">
          {formatProblem(currencies.error)}
        </Alert>
      ) : null}
      <div className={styles.filters}>
        <FilterSelect
          id="rounding-currency"
          label="Currency"
          value={currency}
          options={[{ value: "", label: "Select a currency" }, ...currencies.rows.map((row) => ({ value: row.code, label: `${row.code} - ${row.name}` }))]}
          onChange={setCurrency}
        />
      </div>
      {currency === "" ? (
        <DataTable label="Rounding rules" columns={columns} rows={[]} getRowKey={(row) => row.id} empty={{ title: "Select a currency", description: "Rounding rules are kept per currency." }} />
      ) : (
        <>
          {canWriteReference ? <RuleForm currency={currency} entities={entities} onSaved={rules.reload} /> : null}
          <TestRoundPanel currency={currency} entities={entities} />
          {rules.error ? (
            <Alert tone="danger" title="Could not load rounding rules" live="alert">
              {formatProblem(rules.error)}
            </Alert>
          ) : null}
          {scopedEntity ? (
            <p className={styles.editorHint}>
              Showing whole-tenant rules and rules for {scopedEntity.code}. Choose all entities in the switcher to see every legal entity's rules.
            </p>
          ) : null}
          <DataTable
            label="Rounding rules"
            columns={columns}
            rows={rules.rows}
            getRowKey={(row) => row.id}
            loading={rules.loading || scope === ""}
            empty={{ title: "No rounding rules", description: `${currency} rounds to its minor unit, half even, until a rule is set.` }}
          />
        </>
      )}
    </>
  );
}
