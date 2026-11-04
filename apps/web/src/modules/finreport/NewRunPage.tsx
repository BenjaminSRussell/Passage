import { useRef, useState, type FormEvent } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createRun, CURRENCY_TYPES, describeProblem, listDefinitions, listHierarchies, listTrees, newKey, type CurrencyType, type Problem, type RunRequest } from "./api/client";
import { CURRENCY_TYPE_LABELS, fiscalCalendar, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export interface RunValues {
  definition: string;
  version: string;
  ledgerId: string;
  currencyType: CurrencyType;
  year: string;
  period: string;
  periodsPerYear: string;
  firstYear: string;
  includeAdjustmentPeriods: boolean;
  /** Accept a read model behind the lag gate (REPORT-0004); the run then carries a stale warning. */
  allowStale?: boolean;
  /** Code of a published hierarchy whose nodes become the rows; empty runs the definition's own rows. */
  hierarchy: string;
  hierarchyVersion: string;
  normalBalance: "debit" | "credit";
  accountFrom: string;
  accountTo: string;
  /** Code of a reporting tree the run is limited to; empty runs the whole ledger. */
  tree?: string;
  /** Unit of the tree; empty is the whole tree (the roll-up of every unit). */
  treeUnit?: string;
}

type Errors = Partial<Record<keyof RunValues, string>>;

const INTEGER = /^\d+$/;

/** Client checks that mirror the service (POST /runs); the service has the last word. */
export function validateRun(v: RunValues): Errors {
  const errors: Errors = {};
  if (v.definition === "") errors.definition = "Choose a published definition.";
  if (v.version !== "" && (!INTEGER.test(v.version) || Number(v.version) < 1)) errors.version = "Enter a published version number, or leave it empty for the latest.";
  if (!UUID.test(v.ledgerId.trim())) errors.ledgerId = "Enter the ledger id (a UUID).";
  if (!INTEGER.test(v.year) || Number(v.year) < 1900) errors.year = "Enter the fiscal year, such as 2026.";
  if (!INTEGER.test(v.periodsPerYear) || Number(v.periodsPerYear) < 1 || Number(v.periodsPerYear) > 16) errors.periodsPerYear = "Enter 1 to 16 periods per year.";
  else if (!INTEGER.test(v.period) || Number(v.period) > Number(v.periodsPerYear)) errors.period = `Enter a period from 0 to ${v.periodsPerYear}.`;
  if (!INTEGER.test(v.firstYear) || (INTEGER.test(v.year) && Number(v.firstYear) > Number(v.year))) errors.firstYear = "Enter a year on or before the fiscal year.";
  else if (INTEGER.test(v.year) && Number(v.year) - Number(v.firstYear) > 40) errors.firstYear = "The calendar may span at most 40 years.";
  if (v.hierarchy !== "") {
    if (v.hierarchyVersion !== "" && (!INTEGER.test(v.hierarchyVersion) || Number(v.hierarchyVersion) < 1)) errors.hierarchyVersion = "Enter a published version number, or leave it empty for the latest.";
    if ((v.accountFrom.trim() === "") !== (v.accountTo.trim() === "")) errors.accountTo = "Enter both ends of the account range, or neither.";
    else if (v.accountFrom.trim() > v.accountTo.trim()) errors.accountTo = "The first account must not come after the last.";
  }
  return errors;
}

/** The request the form sends; the calendar covers firstYear..year so prior-year columns resolve. */
export function toRunRequest(v: RunValues): RunRequest {
  const year = Number(v.year);
  const no = Number(v.period);
  const calendar = fiscalCalendar(Number(v.firstYear), year, Number(v.periodsPerYear));
  const period = calendar.find((p) => p.year === year && p.no === no) ?? { year, no, quarter: 1 };
  return {
    definition: v.definition,
    ...(v.version !== "" ? { definitionVersion: Number(v.version) } : {}),
    ledgerId: v.ledgerId.trim(),
    currencyType: v.currencyType,
    period,
    calendar,
    ...(v.includeAdjustmentPeriods ? { includeAdjustmentPeriods: true } : {}),
    ...(v.allowStale ? { allowStale: true } : {}),
    ...(v.hierarchy !== ""
      ? {
          hierarchy: {
            code: v.hierarchy,
            ...(v.hierarchyVersion !== "" ? { version: Number(v.hierarchyVersion) } : {}),
            ...(v.normalBalance === "credit" ? { normalBalance: "credit" as const } : {}),
            ...(v.accountFrom.trim() !== "" ? { accountFrom: v.accountFrom.trim(), accountTo: v.accountTo.trim() } : {}),
          },
        }
      : {}),
    ...(v.tree ? { tree: { code: v.tree, ...(v.treeUnit ? { unit: v.treeUnit } : {}) } } : {}),
  };
}

/** Run a published definition for a ledger and period (FR-15.6). */
export function NewRunPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const thisYear = new Date().getFullYear();
  const [values, setValues] = useState<RunValues>({
    definition: params.get("definition") ?? "",
    version: "",
    ledgerId: "",
    currencyType: "acct",
    year: String(thisYear),
    period: "1",
    periodsPerYear: "12",
    firstYear: String(thisYear - 1),
    includeAdjustmentPeriods: false,
    allowStale: false,
    hierarchy: "",
    hierarchyVersion: "",
    normalBalance: "debit",
    accountFrom: "",
    accountTo: "",
    tree: "",
    treeUnit: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const definitions = useLoad((signal) => listDefinitions(signal), "definitions");
  const published = (definitions.data ?? []).filter((d) => d.latestVersion > 0);
  const hierarchies = useLoad((signal) => listHierarchies(signal), "hierarchies");
  // Categories need master data this service does not have yet, so only account and dimension hierarchies can drive a run.
  const usable = (hierarchies.data ?? []).filter((h) => h.latestVersion > 0 && h.subject !== "main_account_category");
  const chosen = usable.find((h) => h.code === values.hierarchy);
  const trees = useLoad((signal) => listTrees(signal), "trees");
  const chosenTree = (trees.data ?? []).find((t) => t.code === values.tree);

  function update(patch: Partial<RunValues>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    return start(values);
  }

  async function start(sent: RunValues) {
    const found = validateRun(sent);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const run = await createRun(toRunRequest(sent), key.current);
      navigate(`/finreport/runs/${run.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "REPORT-0003") setErrors({ period: problem.message });
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (id: string, name: keyof RunValues, label: string, extra: { hint?: string; optional?: boolean; mono?: boolean; inputMode?: "numeric" } = {}) => (
    <FormField id={id} label={label} hint={extra.hint} optional={extra.optional} error={errors[name]}>
      {(control) => (
        <Input
          {...control}
          inputMode={extra.inputMode}
          className={extra.mono ? styles.mono : undefined}
          value={String(values[name])}
          onChange={(e) => update({ [name]: e.target.value } as Partial<RunValues>)}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Run a statement"
        description="Evaluates a published definition over the reporting read model. The run records the posting sequence it reflects and a hash of its result."
      />
      {definitions.error ? (
        <Alert tone="danger" title="Could not load the definitions" live="alert" action={<Button size="sm" onClick={definitions.reload}>Try again</Button>}>
          {definitions.error.message} ({definitions.error.code})
        </Alert>
      ) : null}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="fr-run-definition" label="Definition" error={errors.definition}>
            {(control) => (
              <Select {...control} value={values.definition} onChange={(e) => update({ definition: e.target.value })} invalid={Boolean(errors.definition)}>
                <option value="">{definitions.loading ? "Loading definitions…" : published.length === 0 ? "No published definitions" : "Choose a definition"}</option>
                {published.map((d) => (
                  <option key={d.code} value={d.code}>
                    {d.code} {d.name} (v{d.latestVersion})
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("fr-run-version", "version", "Version", { optional: true, hint: "Empty runs the latest published version.", inputMode: "numeric" })}
          {text("fr-run-ledger", "ledgerId", "Ledger id", { mono: true })}
          <FormField id="fr-run-currency" label="Currency type" hint="Columns that do not name a currency type use this one.">
            {(control) => (
              <Select {...control} value={values.currencyType} onChange={(e) => update({ currencyType: e.target.value as CurrencyType })}>
                {CURRENCY_TYPES.map((c) => (
                  <option key={c} value={c}>
                    {CURRENCY_TYPE_LABELS[c]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField
            id="fr-run-hierarchy"
            label="Rows from hierarchy"
            optional
            hint={
              hierarchies.error
                ? `The hierarchies could not be loaded (${hierarchies.error.code}). The run uses the definition's own rows.`
                : "One row per node of a published hierarchy, with the definition's columns. Empty uses the definition's own rows."
            }
            error={errors.hierarchy}
          >
            {(control) => (
              <Select {...control} value={values.hierarchy} onChange={(e) => update({ hierarchy: e.target.value, hierarchyVersion: "", accountFrom: "", accountTo: "" })} disabled={usable.length === 0}>
                <option value="">{hierarchies.loading ? "Loading hierarchies…" : usable.length === 0 ? "No published hierarchies" : "Definition rows"}</option>
                {usable.map((h) => (
                  <option key={h.code} value={h.code}>
                    {h.code} {h.name} (v{h.latestVersion})
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {chosen ? (
            <>
              {text("fr-run-hierarchy-version", "hierarchyVersion", "Hierarchy version", { optional: true, hint: "Empty uses the latest published version.", inputMode: "numeric" })}
              <FormField id="fr-run-normal" label="Amounts shown as" hint="Credit shows credit balances as positive amounts.">
                {(control) => (
                  <Select {...control} value={values.normalBalance} onChange={(e) => update({ normalBalance: e.target.value as "debit" | "credit" })}>
                    <option value="debit">Debit balances</option>
                    <option value="credit">Credit balances</option>
                  </Select>
                )}
              </FormField>
              {chosen.subject === "dimension_value" ? (
                <>
                  {text("fr-run-account-from", "accountFrom", "First account", { optional: true, mono: true, hint: "Limits the rows to a range of accounts, such as 6000 to 6999." })}
                  {text("fr-run-account-to", "accountTo", "Last account", { optional: true, mono: true })}
                </>
              ) : null}
            </>
          ) : null}
          <FormField
            id="fr-run-tree"
            label="Limit to reporting tree"
            optional
            hint={
              trees.error
                ? `The trees could not be loaded (${trees.error.code}). The run covers the whole ledger.`
                : "Run the statement for one unit of a tree, rolled up over the units below it. Empty covers the whole ledger."
            }
          >
            {(control) => (
              <Select {...control} value={values.tree ?? ""} onChange={(e) => update({ tree: e.target.value, treeUnit: "" })} disabled={(trees.data ?? []).length === 0}>
                <option value="">{trees.loading ? "Loading trees…" : (trees.data ?? []).length === 0 ? "No reporting trees" : "Whole ledger"}</option>
                {(trees.data ?? []).map((t) => (
                  <option key={t.code} value={t.code}>
                    {t.code} {t.name}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {chosenTree ? (
            <FormField id="fr-run-tree-unit" label="Tree unit" optional hint="A parent unit gives the roll-up of its children. Empty is the whole tree.">
              {(control) => (
                <Select {...control} value={values.treeUnit ?? ""} onChange={(e) => update({ treeUnit: e.target.value })}>
                  <option value="">Whole tree</option>
                  {chosenTree.units.map((u) => (
                    <option key={u.code} value={u.code}>
                      {u.code} {u.name}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          ) : null}
          {text("fr-run-year", "year", "Fiscal year", { inputMode: "numeric" })}
          {text("fr-run-period", "period", "Period", { hint: "0 is the opening period.", inputMode: "numeric" })}
          {text("fr-run-periods", "periodsPerYear", "Periods per year", { inputMode: "numeric" })}
          {text("fr-run-first-year", "firstYear", "Calendar starts in", { hint: "Include the prior year for PY and PYTD columns.", inputMode: "numeric" })}
          <span className={styles.check}>
            <Input
              id="fr-run-adjustment"
              type="checkbox"
              checked={values.includeAdjustmentPeriods}
              onChange={(e) => update({ includeAdjustmentPeriods: e.target.checked })}
            />
            <label htmlFor="fr-run-adjustment">Include adjustment periods</label>
          </span>
          <span className={styles.check}>
            <Input id="fr-run-stale" type="checkbox" checked={values.allowStale} onChange={(e) => update({ allowStale: e.target.checked })} />
            <label htmlFor="fr-run-stale">Accept stale results</label>
          </span>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run statement
            </Button>
            <Button variant="ghost" onClick={() => navigate("/finreport")}>
              Cancel
            </Button>
          </div>
        </form>
      </Card>
      {failure?.code === "REPORT-0004" ? (
        <Alert
          tone="warning"
          title="The read model is behind the ledger"
          live="alert"
          action={
            <Button
              size="sm"
              onClick={() => {
                const next = { ...values, allowStale: true };
                update({ allowStale: true });
                void start(next);
              }}
            >
              Run anyway
            </Button>
          }
        >
          {failure.message} ({failure.code})
        </Alert>
      ) : failure ? (
        <Alert tone="danger" title="The statement did not run" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
    </div>
  );
}
