import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { EntityName } from "../../shell/lookup/EntityName";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import {
  addOwnership,
  addUnit,
  collectBalances,
  getAccountMap,
  getGroup,
  listCollections,
  newKey,
  setAccountMap,
  startRunFromCollection,
  type AccountMapRow,
  type AccountRole,
  type AccountType,
  type DerivedRun,
  type Collection,
  type GroupDetail,
  type Unit,
} from "./api/groups";
import { isCurrency, isDecimal, isPositive, percent } from "./format";
import styles from "./pages.module.css";
import { RowGrid, type RowField } from "./RowGrid";
import { useLegalEntityNames } from "./useEntityNames";

type MapKey = "mainAccount" | "consolidationAccount" | "accountType" | "role";
type RateKey = "currency" | "average" | "closing";

const mapFields: RowField<MapKey>[] = [
  { name: "mainAccount", label: "Main account", mono: true },
  { name: "consolidationAccount", label: "Consolidation account", mono: true },
  {
    name: "accountType",
    label: "Translates at",
    options: [
      { value: "balance_sheet", label: "Closing rate (balance sheet)" },
      { value: "income", label: "Average rate (income)" },
    ],
  },
  {
    name: "role",
    label: "Role in a run",
    optional: true,
    options: [
      { value: "", label: "None" },
      { value: "asset", label: "Asset" },
      { value: "liability", label: "Liability" },
      { value: "equity", label: "Equity" },
      { value: "ic_receivable", label: "Intercompany receivable" },
      { value: "ic_payable", label: "Intercompany payable" },
      { value: "ic_sales", label: "Intercompany sales" },
      { value: "ic_cost", label: "Intercompany cost" },
    ],
  },
];
const rateFields: RowField<RateKey>[] = [
  { name: "currency", label: "Currency", mono: true },
  { name: "average", label: "Average rate", decimal: true },
  { name: "closing", label: "Closing rate", decimal: true },
];

const emptyMapRow = (): Record<MapKey, string> => ({ mainAccount: "", consolidationAccount: "", accountType: "balance_sheet", role: "" });
const emptyRate = (): Record<RateKey, string> => ({ currency: "", average: "", closing: "" });

/** Checks of the chart mapping that mirror the service, and the request it sends. */
export function validateMap(rows: (Record<Exclude<MapKey, "role">, string> & { role?: string })[]): { errors: Record<string, string>; request?: AccountMapRow[] } {
  const errors: Record<string, string> = {};
  const seen = new Set<string>();
  const code = /^[A-Za-z0-9._:-]{1,64}$/;
  rows.forEach((r, i) => {
    const main = r.mainAccount.trim();
    if (!code.test(main)) errors[`accounts[${i}].mainAccount`] = "Enter an account code.";
    else if (seen.has(main)) errors[`accounts[${i}].mainAccount`] = "This account is mapped twice.";
    seen.add(main);
    if (!code.test(r.consolidationAccount.trim())) errors[`accounts[${i}].consolidationAccount`] = "Enter a consolidation account code.";
  });
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    request: rows.map((r) => ({
      mainAccount: r.mainAccount.trim(),
      consolidationAccount: r.consolidationAccount.trim(),
      accountType: r.accountType as AccountType,
      ...(r.role ? { role: r.role as AccountRole } : {}),
    })),
  };
}

/** The foreign currencies of a group's units, which need rates to collect. */
export function foreignCurrencies(group: GroupDetail): string[] {
  return [...new Set(group.units.map((u) => u.currency).filter((c) => c !== group.currency))].sort();
}

/** One group: its units, ownership, account mapping and the collections of unit balances (FR-40.7, FR-40.8). */
export function GroupDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [group, setGroup] = useState<GroupDetail | null>(null);
  const [collections, setCollections] = useState<Collection[]>([]);
  const [loadFailure, setLoadFailure] = useState<Problem | null>(null);
  const [mapRows, setMapRows] = useState<Record<MapKey, string>[]>([]);
  const [reload, setReload] = useState(0);
  const [run, setRun] = useState<DerivedRun | null>(null);
  const [runFailure, setRunFailure] = useState<Problem | null>(null);
  const names = useLegalEntityNames(group?.units.map((u) => u.legalEntityId) ?? []);

  useEffect(() => {
    const ctl = new AbortController();
    setLoadFailure(null);
    Promise.all([getGroup(id, ctl.signal), getAccountMap(id, ctl.signal), listCollections(id, ctl.signal)])
      .then(([g, map, cols]) => {
        setGroup(g);
        setMapRows(map.accounts.map((a) => ({ ...a, role: a.role ?? "" })));
        setCollections(cols.items);
      })
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) setLoadFailure(describeProblem(err));
      });
    return () => ctl.abort();
  }, [id, reload]);
  const refresh = useCallback(() => setReload((n) => n + 1), []);

  if (loadFailure) {
    return (
      <Alert tone="danger" title="We couldn't load this group" live="alert" action={<Button size="sm" onClick={refresh}>Try again</Button>}>
        <ProblemMessage problem={loadFailure} />
      </Alert>
    );
  }
  if (!group) return <p className={styles.note}>Loading the group…</p>;

  const unitColumns: DataTableColumn<Unit>[] = [
    { key: "entity", header: "Legal entity", render: (u) => <EntityName entity="legalEntity" id={u.legalEntityId} /> },
    { key: "ledger", header: "Ledger", mono: true, render: (u) => u.ledgerId },
    { key: "currency", header: "Currency", mono: true, render: (u) => u.currency },
    { key: "fiscalYear", header: "Fiscal year starts in month", align: "end", render: (u) => String(u.fiscalYearStartMonth ?? 1) },
    { key: "percent", header: "Held today (%)", align: "end", render: (u) => percent(u.currentPercent) },
    {
      key: "history",
      header: "Ownership history",
      render: (u) => (u.ownership.length === 0 ? "None recorded" : u.ownership.map((h) => `${percent(h.percent)} % from ${h.effectiveFrom}${h.effectiveTo ? ` to ${h.effectiveTo}` : ""}`).join("; ")),
    },
  ];
  const collectionColumns: DataTableColumn<Collection>[] = [
    { key: "period", header: "Period", mono: true, render: (c) => c.period },
    { key: "version", header: "Version", align: "end", render: (c) => String(c.version) },
    { key: "by", header: "Collected by", render: (c) => c.createdBy },
    { key: "at", header: "Collected", render: (c) => c.createdAt.slice(0, 16).replace("T", " ") },
    {
      key: "run",
      header: "Consolidate",
      render: (c) => (
        <Button
          size="sm"
          onClick={(e) => {
            e.stopPropagation();
            void runFrom(c);
          }} aria-label={`Run the consolidation from the collection of ${c.period} version ${c.version}`}>
          Run from this collection
        </Button>
      ),
    },
  ];
  async function runFrom(c: Collection) {
    setRunFailure(null);
    setRun(null);
    try {
      setRun(await startRunFromCollection({ collectionId: c.id }, newKey()));
    } catch (err) {
      setRunFailure(describeProblem(err));
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title={group.name} description={`Group ${group.code}, statements in ${group.currency}.`} actions={<Link to="/consolidation/groups">All groups</Link>} />
      <Card title="Units" headingLevel={3}>
        <div className={styles.stack}>
          <DataTable label="Units" columns={unitColumns} rows={group.units} getRowKey={(u) => u.id} empty={{ title: "No units yet", description: "Add the legal entities that make up the group." }} />
          <AddUnitForm groupId={group.id} groupCurrency={group.currency} onDone={refresh} />
        </div>
      </Card>
      {group.units.length > 0 ? (
        <Card title="Ownership" headingLevel={3}>
          <OwnershipForm group={group} names={names} onDone={refresh} />
        </Card>
      ) : null}
      <Card title="Account mapping" headingLevel={3}>
        <MapForm groupId={group.id} rows={mapRows} onRows={setMapRows} />
      </Card>
      <Card title="Collected balances" headingLevel={3}>
        <div className={styles.stack}>
          {run ? (
            <Alert tone="success" title={`Run version ${run.version} stored`} live="status">
              {`Consolidated ${run.unitCount} ${run.unitCount === 1 ? "unit" : "units"} for ${run.period} from the collection, with ${run.eliminationCount} elimination entries and a translation difference of ${run.ctaAmount} ${run.currency}.`}
            </Alert>
          ) : null}
          {runFailure ? (
            <Alert tone="danger" title="The run was not started" live="alert">
              <ProblemMessage problem={runFailure} />
            </Alert>
          ) : null}
          <DataTable
            label="Collections"
            columns={collectionColumns}
            rows={collections}
            getRowKey={(c) => c.id}
            onRowOpen={(c) => navigate(`/consolidation/collections/${c.id}`)}
            empty={{ title: "Nothing collected yet", description: "Collect each unit's balances from its ledger for a period." }}
          />
          <CollectForm group={group} onDone={(c) => navigate(`/consolidation/collections/${c.id}`)} />
        </div>
      </Card>
    </div>
  );
}

function AddUnitForm({ groupId, groupCurrency, onDone }: { groupId: string; groupCurrency: string; onDone(): void }) {
  const [values, setValues] = useState({ legalEntityId: "", ledgerId: "", currency: groupCurrency });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (values.legalEntityId === "") found.legalEntityId = "Choose the legal entity.";
    if (values.ledgerId.trim() === "") found.ledgerId = "Enter the ledger to collect from.";
    if (!isCurrency(values.currency.trim().toUpperCase())) found.currency = "Enter a three-letter currency code.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await addUnit(groupId, { legalEntityId: values.legalEntityId, ledgerId: values.ledgerId.trim(), currency: values.currency.trim().toUpperCase() });
      setValues({ legalEntityId: "", ledgerId: "", currency: groupCurrency });
      onDone();
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
      <FormField id="unit-entity" label="Legal entity" error={errors.legalEntityId}>
        {(c) => <EntityLookup entity="legalEntity" {...c} prefill={false} value={values.legalEntityId} onChange={(e) => setValues({ ...values, legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />}
      </FormField>
      <FormField id="unit-ledger" label="Ledger code" hint="The ledger of that legal entity the balances are read from." error={errors.ledgerId}>
        {(c) => <Input {...c} className={styles.mono} value={values.ledgerId} onChange={(e) => setValues({ ...values, ledgerId: e.target.value })} invalid={Boolean(errors.ledgerId)} />}
      </FormField>
      <FormField id="unit-currency" label="Currency of the ledger" error={errors.currency}>
        {(c) => <Input {...c} className={styles.mono} value={values.currency} onChange={(e) => setValues({ ...values, currency: e.target.value })} invalid={Boolean(errors.currency)} />}
      </FormField>
      {failure ? (
        <div className={styles.formActions}>
          <Alert tone="danger" title="The unit was not added" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" loading={busy}>
          Add unit
        </Button>
      </div>
    </form>
  );
}

function OwnershipForm({ group, names, onDone }: { group: GroupDetail; names: Record<string, string>; onDone(): void }) {
  const label = (u: Unit) => names[u.legalEntityId] ?? `Ledger ${u.ledgerId}`;
  const [values, setValues] = useState({ unitId: group.units[0]?.id ?? "", holderUnitId: "", effectiveFrom: "", percent: "" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!/^\d{4}-\d{2}-\d{2}$/.test(values.effectiveFrom)) found.effectiveFrom = "Choose the date the holding starts.";
    if (!isDecimal(values.percent, 4) || Number(values.percent) > 100) found.percent = "Enter a percentage from 0 to 100.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await addOwnership(group.id, values.unitId, { holderUnitId: values.holderUnitId || undefined, effectiveFrom: values.effectiveFrom, percent: values.percent });
      setValues({ ...values, effectiveFrom: "", percent: "" });
      onDone();
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
      <FormField id="own-unit" label="Unit held">
        {(c) => (
          <Select {...c} value={values.unitId} onChange={(e) => setValues({ ...values, unitId: e.target.value })}>
            {group.units.map((u) => (
              <option key={u.id} value={u.id}>
                {label(u)}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="own-holder" label="Held by" hint="Leave as the group parent unless another unit holds the stake.">
        {(c) => (
          <Select {...c} value={values.holderUnitId} onChange={(e) => setValues({ ...values, holderUnitId: e.target.value })}>
            <option value="">Group parent</option>
            {group.units
              .filter((u) => u.id !== values.unitId)
              .map((u) => (
                <option key={u.id} value={u.id}>
                  {label(u)}
                </option>
              ))}
          </Select>
        )}
      </FormField>
      <FormField id="own-from" label="Effective from" error={errors.effectiveFrom} hint="The holder's earlier holding ends on this date.">
        {(c) => <Input {...c} type="date" value={values.effectiveFrom} onChange={(e) => setValues({ ...values, effectiveFrom: e.target.value })} invalid={Boolean(errors.effectiveFrom)} />}
      </FormField>
      <FormField id="own-percent" label="Share held (%)" error={errors.percent}>
        {(c) => <Input {...c} inputMode="decimal" value={values.percent} onChange={(e) => setValues({ ...values, percent: e.target.value })} invalid={Boolean(errors.percent)} />}
      </FormField>
      {failure ? (
        <div className={styles.formActions}>
          <Alert tone="danger" title="The ownership was not saved" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" loading={busy}>
          Record ownership
        </Button>
      </div>
    </form>
  );
}

function MapForm({ groupId, rows, onRows }: { groupId: string; rows: Record<MapKey, string>[]; onRows(rows: Record<MapKey, string>[]): void }) {
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, request } = validateMap(rows);
    setErrors(found);
    setFailure(null);
    setSaved(false);
    if (!request) return;
    setBusy(true);
    try {
      await setAccountMap(groupId, request);
      setSaved(true);
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
      <p className={styles.note}>
        Each main account of the units' ledgers maps to a consolidation account. Balance sheet accounts translate at the closing rate and income accounts at the average rate. Accounts without a row are listed as unmapped when balances are collected.
      </p>
      <RowGrid idPrefix="map" path="accounts" noun="Account" fields={mapFields} rows={rows} errors={errors} newRow={emptyMapRow} onChange={onRows} />
      {failure ? (
        <div className={styles.formActions}>
          <Alert tone="danger" title="The mapping was not saved" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        </div>
      ) : null}
      {saved ? (
        <div className={styles.formActions}>
          <Alert tone="success" title="Mapping saved" live="status">
            The mapping replaced the earlier one. The next collection uses it.
          </Alert>
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" loading={busy}>
          Save mapping
        </Button>
      </div>
    </form>
  );
}

function CollectForm({ group, onDone }: { group: GroupDetail; onDone(c: Collection): void }) {
  const [period, setPeriod] = useState("");
  const [rates, setRates] = useState<Record<RateKey, string>[]>(() => foreignCurrencies(group).map((currency) => ({ ...emptyRate(), currency })));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!/^\d{4}-(0[1-9]|1[0-2])$/.test(period.trim())) found.period = "Enter a period such as 2026-11.";
    // A row with neither rate is left to the rate tables.
    const named = rates.filter((r) => r.average.trim() !== "" || r.closing.trim() !== "");
    rates.forEach((r, i) => {
      if (!named.includes(r)) return;
      if (!isCurrency(r.currency.trim().toUpperCase())) found[`rates[${i}].currency`] = "Enter a three-letter currency code.";
      if (!isPositive(r.average)) found[`rates[${i}].average`] = "Enter a rate above 0.";
      if (!isPositive(r.closing)) found[`rates[${i}].closing`] = "Enter a rate above 0.";
    });
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const c = await collectBalances(group.id, { period: period.trim(), rates: named.map((r) => ({ currency: r.currency.trim().toUpperCase(), average: r.average.trim(), closing: r.closing.trim() })) }, key.current);
      key.current = newKey();
      onDone(c);
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  if (group.units.length === 0) {
    return <EmptyState icon="receipt" headingLevel={3} title="Add a unit first" description="Balances are collected from the ledgers of the group's units." />;
  }
  return (
    <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
      <FormField id="collect-period" label="Period" hint="Balance sheet balances run up to the period; income runs from January." error={errors.period}>
        {(c) => <Input {...c} className={styles.mono} value={period} onChange={(e) => setPeriod(e.target.value)} invalid={Boolean(errors.period)} />}
      </FormField>
      <h4 className={styles.section}>Rates to {group.currency}</h4>
      <p className={styles.note}>Leave a currency without rates to use the rate tables&apos; average and closing rates for the period.</p>
      <RowGrid idPrefix="rate" path="rates" noun="Rate" fields={rateFields} rows={rates} errors={errors} newRow={emptyRate} onChange={setRates} />
      {failure ? (
        <div className={styles.formActions}>
          <Alert tone="danger" title="The balances were not collected" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          Collect balances
        </Button>
      </div>
    </form>
  );
}
