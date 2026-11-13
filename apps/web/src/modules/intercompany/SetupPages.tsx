import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { LookupFormField } from "../../shell/lookup/LookupFormField";
import { Alert, Button, buttonClassName, Card, FormField, Input, ListPage, loadedRows, PageHeader, Select, Skeleton, type DataTableColumn, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { describeProblem, type ChargeCategory, type MatrixRow, type Problem, type TransferMethod } from "./api/client";
import { addTransferPrice, getMatrix, getSettings, listEntities, listTransferPrices, putEntity, putMatrix, putSettings, runLedgerReconciliation, type IcSettings, type RegistryEntity, type StoredTransferPrice } from "./api/registry";
import { newKey } from "./api/postings";
import { amount, dash, isCurrency, isDecimal } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const ACCOUNT = /^[A-Za-z0-9._:-]{1,64}$/;
const ENTITY = /^[A-Za-z0-9._-]{1,32}$/;
const PERIOD = /^\d{4}-(0[1-9]|1[0-2])$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

function Failure({ title, problem, onRetry }: { title: string; problem: Problem; onRetry?: () => void }) {
  return (
    <Alert tone="danger" title={title} live="alert" action={onRetry ? <Button size="sm" onClick={onRetry}>Try again</Button> : undefined}>
      <ProblemMessage problem={problem} />
    </Alert>
  );
}

/** The registry's entity codes, for the selects of the matrix and the transfer prices. */
function useEntities() {
  return useLoad((signal) => listEntities(signal).then((r) => r.items), "entities");
}

function EntitySelect({ id, label, value, entities, error, onChange }: { id: string; label: string; value: string; entities: RegistryEntity[]; error?: string; onChange(v: string): void }) {
  return (
    <FormField id={id} label={label} error={error}>
      {(c) => (
        <Select {...c} value={value} onChange={(e) => onChange(e.target.value)}>
          <option value="">Choose an entity</option>
          {entities.map((e) => (
            <option key={e.entity} value={e.entity}>
              {e.entity} ({e.currency})
            </option>
          ))}
        </Select>
      )}
    </FormField>
  );
}

// ---- entity registry ----

const entityColumns: DataTableColumn<RegistryEntity>[] = [
  { key: "entity", header: "Entity", mono: true },
  { key: "legalEntityId", header: "Legal entity", render: (e) => <EntityName entity="legalEntity" id={e.legalEntityId} /> },
  { key: "ledgerId", header: "Ledger", mono: true },
  { key: "currency", header: "Currency", mono: true },
  { key: "country", header: "Country", mono: true, render: (e) => e.country || dash },
  { key: "fxDifferenceAccount", header: "FX difference account", mono: true, render: (e) => e.fxDifferenceAccount || dash },
];

/** Where each intercompany entity code keeps its books (FR-40.6): the reconciliation, the FX adjustment and posting read it. */
export function EntitiesPage() {
  const navigate = useNavigate();
  const state = useEntities();
  return (
    <ListPage
      id="intercompany-entities"
      title="Intercompany entities"
      description="Each entity code names the legal entity, ledger and currency its books are in, and the accounts the service books exchange differences and tax to. The accounting matrix pairs these codes."
      actions={
        <Link className={buttonClassName("primary")} to="/intercompany/entities/new">
          Add an entity
        </Link>
      }
      noun="entities"
      singular="entity"
      columns={entityColumns}
      useRows={() => loadedRows({ data: state.data, loading: state.loading, error: state.error, reload: state.reload })}
      getRowKey={(e) => e.entity}
      onRowOpen={(e) => navigate(`/intercompany/entities/${encodeURIComponent(e.entity)}`)}
      search={(e) => ({ number: e.entity, name: e.ledgerId })}
      empty={{ title: "No entities yet", description: "Add each legal entity that trades with another one, with its ledger and currency." }}
    />
  );
}

type EntityValues = Required<RegistryEntity>;

const blankEntity: EntityValues = { entity: "", legalEntityId: "", ledgerId: "", currency: "", country: "", fxDifferenceAccount: "", outputTaxAccount: "", inputTaxAccount: "", taxGroup: "" };

export function validateEntity(v: EntityValues): Record<string, string> {
  const e: Record<string, string> = {};
  if (!ENTITY.test(v.entity.trim())) e.entity = "Enter a code of 1 to 32 letters, digits, dots, dashes or underscores, such as CRP-US.";
  if (v.legalEntityId === "") e.legalEntityId = "Choose the legal entity.";
  if (v.ledgerId.trim() === "" || v.ledgerId.trim().length > 64) e.ledgerId = "Enter the ledger the entity's books are in.";
  if (!isCurrency(v.currency.trim().toUpperCase())) e.currency = "Enter a three-letter currency code, such as USD.";
  if (v.country.trim() !== "" && !/^[A-Za-z]{2}$/.test(v.country.trim())) e.country = "Enter a two-letter country code, such as DE.";
  for (const k of ["fxDifferenceAccount", "outputTaxAccount", "inputTaxAccount"] as const) {
    if (v[k].trim() !== "" && !ACCOUNT.test(v[k].trim())) e[k] = "Use 1 to 64 letters, digits or . _ : -";
  }
  return e;
}

/** Add an entity code, or change where an existing one keeps its books. */
export function EntityFormPage() {
  const { entity = "" } = useParams();
  const editing = entity !== "";
  const navigate = useNavigate();
  const state = useEntities();
  const [values, setValues] = useState<EntityValues | null>(editing ? null : blankEntity);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  if (editing && values === null) {
    if (state.error) return <Failure title="We couldn't load the entity" problem={describeProblem(state.error)} onRetry={state.reload} />;
    if (!state.data) return <Skeleton height="tile" />;
    const found = state.data.find((e) => e.entity === entity);
    if (!found) return <Failure title="No such entity" problem={{ status: 404, code: "IC-0404", message: `The registry has no entity ${entity}.`, fields: {} }} />;
    setValues({ ...blankEntity, ...found });
    return null;
  }
  const v = values ?? blankEntity;
  const set = (patch: Partial<EntityValues>) => setValues({ ...v, ...patch });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateEntity(v);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const trim = (s: string) => s.trim();
      await putEntity({
        entity: trim(v.entity),
        legalEntityId: v.legalEntityId,
        ledgerId: trim(v.ledgerId),
        currency: trim(v.currency).toUpperCase(),
        country: trim(v.country).toUpperCase(),
        fxDifferenceAccount: trim(v.fxDifferenceAccount),
        outputTaxAccount: trim(v.outputTaxAccount),
        inputTaxAccount: trim(v.inputTaxAccount),
        taxGroup: trim(v.taxGroup),
      });
      navigate("/intercompany/entities");
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const text = (name: keyof EntityValues, label: string, hint?: string, optional = false) => (
    <FormField id={`ent-${name}`} label={label} hint={hint} optional={optional} error={errors[name]}>
      {(c) => <Input {...c} className={styles.mono} value={v[name]} disabled={name === "entity" && editing} onChange={(e) => set({ [name]: e.target.value })} invalid={Boolean(errors[name])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title={editing ? `Entity ${entity}` : "Add an intercompany entity"} actions={<Link to="/intercompany/entities">All entities</Link>} />
      <Card>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label="Intercompany entity">
          {text("entity", "Entity code", "The code the matrix, charges and reconciliation use, such as CRP-US.")}
          <LookupFormField id="ent-legalEntityId" label="Legal entity" entity="legalEntity" value={v.legalEntityId} error={errors.legalEntityId} prefill={!editing} onChange={(legalEntityId) => set({ legalEntityId })} />
          {text("ledgerId", "Ledger", "The ledger the entity's books are in.")}
          {text("currency", "Currency", "The ledger's currency.")}
          {text("country", "Country", "Two letters; cross-border tax needs it.", true)}
          {text("fxDifferenceAccount", "FX difference account", "An FX adjustment books the exchange difference of a pair here.", true)}
          {text("outputTaxAccount", "Output tax account", undefined, true)}
          {text("inputTaxAccount", "Input tax account", undefined, true)}
          {text("taxGroup", "Tax group", undefined, true)}
          {failure ? (
            <div className={styles.formActions}>
              <Failure title="The entity was not saved" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save entity
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}

// ---- accounting matrix ----

export function validateMatrix(rows: MatrixRow[]): Record<string, string> {
  const e: Record<string, string> = {};
  const pairs = new Set<string>();
  rows.forEach((r, i) => {
    if (r.entityA === "") e[`rows[${i}].entityA`] = "Choose the entity that holds the receivable.";
    if (r.entityB === "") e[`rows[${i}].entityB`] = "Choose the entity that holds the payable.";
    if (r.entityA !== "" && r.entityA === r.entityB) e[`rows[${i}].entityB`] = "A pair needs two different entities.";
    if (!ACCOUNT.test(r.dueFrom.trim())) e[`rows[${i}].dueFrom`] = "Enter the receivable account.";
    if (!ACCOUNT.test(r.dueTo.trim())) e[`rows[${i}].dueTo`] = "Enter the payable account.";
    const key = `${r.entityA}|${r.entityB}`;
    if (pairs.has(key)) e[`rows[${i}].entityB`] = "This pair appears twice.";
    pairs.add(key);
  });
  return e;
}

const emptyMatrixRow = (): MatrixRow => ({ entityA: "", entityB: "", dueFrom: "", dueTo: "" });

/** The intercompany accounting matrix (BR-40.4): for each ordered pair, A's receivable account and B's payable account. */
export function MatrixPage() {
  const entities = useEntities();
  const matrix = useLoad((signal) => getMatrix(signal).then((m) => m.rows), "matrix");
  const [rows, setRows] = useState<MatrixRow[] | null>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);

  const header = (
    <PageHeader
      title="Intercompany accounting matrix"
      description="Each row says which account entity A holds its receivable from entity B on, and which account B holds the payable on. Posting, the ledger reconciliation and the FX adjustment read these accounts. Saving replaces the whole matrix."
      actions={<Link to="/intercompany/entities">Entities</Link>}
    />
  );
  if (matrix.error || entities.error) {
    return (
      <div className={styles.stack}>
        {header}
        <Failure title="We couldn't load the matrix" problem={describeProblem(matrix.error ?? entities.error)} onRetry={() => { matrix.reload(); entities.reload(); }} />
      </div>
    );
  }
  if (!matrix.data || !entities.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        {header}
        <Skeleton height="tile" />
      </div>
    );
  }
  const list = rows ?? (matrix.data.length > 0 ? matrix.data : [emptyMatrixRow()]);
  const known = entities.data;
  const set = (i: number, patch: Partial<MatrixRow>) => setRows(list.map((r, j) => (j === i ? { ...r, ...patch } : r)));

  async function save(event: FormEvent) {
    event.preventDefault();
    const found = validateMatrix(list);
    setErrors(found);
    setFailure(null);
    setNotice("");
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const out = await putMatrix(list.map((r) => ({ ...r, dueFrom: r.dueFrom.trim(), dueTo: r.dueTo.trim() })));
      setRows(out.rows);
      setNotice(`The matrix was saved with ${out.rows.length} ${out.rows.length === 1 ? "pair" : "pairs"}.`);
      matrix.reload();
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      {header}
      {known.length < 2 ? (
        <Alert tone="info" title="Add the entities first">
          The matrix pairs entity codes of the registry. <Link to="/intercompany/entities/new">Add an entity</Link>.
        </Alert>
      ) : null}
      {notice ? (
        <Alert tone="success" title="Saved" live="status">
          {notice}
        </Alert>
      ) : null}
      <Card>
        <form className={styles.form} onSubmit={(e) => void save(e)} noValidate aria-label="Accounting matrix">
          {list.map((r, i) => (
            <fieldset key={i} className={styles.lineCard}>
              <legend className={styles.lineLegend}>Pair {i + 1}</legend>
              <EntitySelect id={`mx-${i}-a`} label="Entity A (receivable)" value={r.entityA} entities={known} error={errors[`rows[${i}].entityA`]} onChange={(entityA) => set(i, { entityA })} />
              <FormField id={`mx-${i}-from`} label="A's receivable account" error={errors[`rows[${i}].dueFrom`]}>
                {(c) => <Input {...c} className={styles.mono} value={r.dueFrom} onChange={(e) => set(i, { dueFrom: e.target.value })} invalid={Boolean(errors[`rows[${i}].dueFrom`])} />}
              </FormField>
              <EntitySelect id={`mx-${i}-b`} label="Entity B (payable)" value={r.entityB} entities={known} error={errors[`rows[${i}].entityB`]} onChange={(entityB) => set(i, { entityB })} />
              <FormField id={`mx-${i}-to`} label="B's payable account" error={errors[`rows[${i}].dueTo`]}>
                {(c) => <Input {...c} className={styles.mono} value={r.dueTo} onChange={(e) => set(i, { dueTo: e.target.value })} invalid={Boolean(errors[`rows[${i}].dueTo`])} />}
              </FormField>
              {list.length > 1 ? (
                <div className={styles.formActions}>
                  <Button size="sm" onClick={() => setRows(list.filter((_, j) => j !== i))}>
                    Remove pair {i + 1}
                  </Button>
                </div>
              ) : null}
            </fieldset>
          ))}
          <div className={styles.formActions}>
            <Button onClick={() => setRows([...list, emptyMatrixRow()])}>Add a pair</Button>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Failure title="The matrix was not saved" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save the matrix
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}

// ---- transfer prices ----

const METHOD_LABEL: Record<TransferMethod, string> = { Cost: "At cost", CostPlus: "Cost plus", Table: "Price table" };

function priceText(p: StoredTransferPrice): string {
  if (p.method === "CostPlus") return `Cost plus ${p.markupPct ?? "0"} %`;
  if (p.method === "Table") return `${amount(p.unitPrice)} ${p.currency} a unit`;
  return "At cost";
}

const priceColumns: DataTableColumn<StoredTransferPrice>[] = [
  { key: "pair", header: "Lender to borrower", mono: true, render: (p) => `${p.lendingEntity} → ${p.borrowingEntity}` },
  { key: "category", header: "Category", render: (p) => (p.resourceRole ? `${p.category}, ${p.resourceRole}` : p.category) },
  { key: "method", header: "Price", render: priceText },
  { key: "currency", header: "Currency", mono: true },
  { key: "effectiveFrom", header: "Effective from", mono: true },
  { key: "createdBy", header: "Added by", render: (p) => p.createdBy ?? dash },
];

const priceFilters: ListFilterDef[] = [
  { kind: "text", key: "lender", label: "Lending entity" },
  { kind: "text", key: "borrower", label: "Borrowing entity" },
];

function usePriceRows(query: ListQuery) {
  const { lender = "", borrower = "" } = query.filters;
  const state = useLoad((signal) => listTransferPrices({ lender: lender.trim() || undefined, borrower: borrower.trim() || undefined }, signal).then((r) => r.items), `${lender}|${borrower}`);
  return loadedRows({ data: state.data, loading: state.loading, error: state.error, reload: state.reload });
}

/** The stored, effective-dated transfer prices (BR-40.12). A price is never edited: the next one takes over from its date. */
export function TransferPricesPage() {
  return (
    <ListPage
      id="intercompany-transfer-prices"
      title="Transfer prices"
      description="A charge is priced with the price of its lender, borrower, category and role in force on its date. To change a price, add a new one from the date it applies."
      actions={
        <Link className={buttonClassName("primary")} to="/intercompany/transfer-prices/new">
          Add a price
        </Link>
      }
      noun="transfer prices"
      singular="transfer price"
      columns={priceColumns}
      filters={priceFilters}
      useRows={usePriceRows}
      getRowKey={(p, i) => p.id ?? `${p.lendingEntity}-${p.borrowingEntity}-${p.category}-${p.effectiveFrom}-${i}`}
      empty={{ title: "No transfer prices yet", description: "Without a price, a charge goes to suspense until one is added." }}
    />
  );
}

interface PriceValues {
  lendingEntity: string;
  borrowingEntity: string;
  category: ChargeCategory;
  resourceRole: string;
  method: TransferMethod;
  markupPct: string;
  unitPrice: string;
  currency: string;
  effectiveFrom: string;
}

export function validatePrice(v: PriceValues): Record<string, string> {
  const e: Record<string, string> = {};
  if (v.lendingEntity === "") e.lendingEntity = "Choose the entity that lends the resource.";
  if (v.borrowingEntity === "") e.borrowingEntity = "Choose the entity that is charged.";
  if (v.lendingEntity !== "" && v.lendingEntity === v.borrowingEntity) e.borrowingEntity = "The lender and borrower must be different entities.";
  if (v.method === "CostPlus" && !isDecimal(v.markupPct, 4)) e.markupPct = "Enter the markup in percent, such as 10.";
  if (v.method === "Table" && (!isDecimal(v.unitPrice, 4) || Number(v.unitPrice) <= 0)) e.unitPrice = "Enter the price of one unit, such as 85.";
  if (!isCurrency(v.currency.trim().toUpperCase())) e.currency = "Enter a three-letter currency code.";
  if (!DATE.test(v.effectiveFrom)) e.effectiveFrom = "Enter the date the price applies from.";
  return e;
}

export function NewTransferPricePage() {
  const navigate = useNavigate();
  const entities = useEntities();
  const [v, setV] = useState<PriceValues>({ lendingEntity: "", borrowingEntity: "", category: "Hour", resourceRole: "", method: "CostPlus", markupPct: "", unitPrice: "", currency: "", effectiveFrom: "" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const set = (patch: Partial<PriceValues>) => setV({ ...v, ...patch });
  const known = entities.data ?? [];

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validatePrice(v);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await addTransferPrice({
        lendingEntity: v.lendingEntity,
        borrowingEntity: v.borrowingEntity,
        category: v.category,
        resourceRole: v.resourceRole.trim(),
        method: v.method,
        ...(v.method === "CostPlus" ? { markupPct: v.markupPct.trim() } : {}),
        ...(v.method === "Table" ? { unitPrice: v.unitPrice.trim() } : {}),
        currency: v.currency.trim().toUpperCase(),
        effectiveFrom: v.effectiveFrom,
      });
      navigate("/intercompany/transfer-prices");
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Add a transfer price" description="The price applies to charges dated on or after its date, until a later price for the same pair, category and role." actions={<Link to="/intercompany/transfer-prices">All prices</Link>} />
      {entities.error ? <Failure title="We couldn't load the entities" problem={describeProblem(entities.error)} onRetry={entities.reload} /> : null}
      <Card>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label="Transfer price">
          <EntitySelect id="tp-lender" label="Lending entity" value={v.lendingEntity} entities={known} error={errors.lendingEntity} onChange={(lendingEntity) => set({ lendingEntity })} />
          <EntitySelect id="tp-borrower" label="Borrowing entity" value={v.borrowingEntity} entities={known} error={errors.borrowingEntity} onChange={(borrowingEntity) => set({ borrowingEntity })} />
          <FormField id="tp-category" label="Category">
            {(c) => (
              <Select {...c} value={v.category} onChange={(e) => set({ category: e.target.value as ChargeCategory })}>
                <option value="Hour">Hours</option>
                <option value="Expense">Expenses</option>
                <option value="Item">Items</option>
                <option value="Fee">Fees</option>
              </Select>
            )}
          </FormField>
          <FormField id="tp-role" label="Resource role" optional hint="Such as Engineer; leave empty for every role." error={errors.resourceRole}>
            {(c) => <Input {...c} value={v.resourceRole} onChange={(e) => set({ resourceRole: e.target.value })} />}
          </FormField>
          <FormField id="tp-method" label="Pricing">
            {(c) => (
              <Select {...c} value={v.method} onChange={(e) => set({ method: e.target.value as TransferMethod })}>
                {(Object.keys(METHOD_LABEL) as TransferMethod[]).map((m) => (
                  <option key={m} value={m}>
                    {METHOD_LABEL[m]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {v.method === "CostPlus" ? (
            <FormField id="tp-markup" label="Markup percent" error={errors.markupPct}>
              {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={v.markupPct} onChange={(e) => set({ markupPct: e.target.value })} invalid={Boolean(errors.markupPct)} />}
            </FormField>
          ) : null}
          {v.method === "Table" ? (
            <FormField id="tp-unit" label="Price of one unit" error={errors.unitPrice}>
              {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={v.unitPrice} onChange={(e) => set({ unitPrice: e.target.value })} invalid={Boolean(errors.unitPrice)} />}
            </FormField>
          ) : null}
          <FormField id="tp-currency" label="Currency" error={errors.currency}>
            {(c) => <Input {...c} className={styles.mono} value={v.currency} onChange={(e) => set({ currency: e.target.value })} invalid={Boolean(errors.currency)} />}
          </FormField>
          <FormField id="tp-from" label="Effective from" error={errors.effectiveFrom}>
            {(c) => <Input {...c} type="date" value={v.effectiveFrom} onChange={(e) => set({ effectiveFrom: e.target.value })} invalid={Boolean(errors.effectiveFrom)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Failure title="The price was not added" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Add the price
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}

// ---- settings ----

export function validateSettings(s: IcSettings): Record<string, string> {
  const e: Record<string, string> = {};
  if (s.reconCurrency.trim() !== "" && !isCurrency(s.reconCurrency.trim().toUpperCase())) e.reconCurrency = "Enter a three-letter currency code, or leave it empty to switch the schedule off.";
  if (!isDecimal(s.fxTolerancePct, 4) || Number(s.fxTolerancePct) > 100) e.fxTolerancePct = "Enter a percentage from 0 to 100, such as 0.5.";
  if (!isDecimal(s.fxAdjustMaxPct, 4) || Number(s.fxAdjustMaxPct) > 10) e.fxAdjustMaxPct = "Enter a percentage from 0 to 10, such as 2.";
  return e;
}

/** The tenant's intercompany settings: how charges settle, cross-border tax, and the scheduled ledger reconciliation. */
export function SettingsPage() {
  const state = useLoad((signal) => getSettings(signal), "settings");
  const [values, setValues] = useState<IcSettings | null>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const header = <PageHeader title="Intercompany settings" description="These apply to every entity of the tenant." />;
  if (state.error) {
    return (
      <div className={styles.stack}>
        {header}
        <Failure title="We couldn't load the settings" problem={describeProblem(state.error)} onRetry={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        {header}
        <Skeleton height="tile" />
      </div>
    );
  }
  const v = values ?? state.data;
  const set = (patch: Partial<IcSettings>) => setValues({ ...v, ...patch });

  async function save(event: FormEvent) {
    event.preventDefault();
    const found = validateSettings(v);
    setErrors(found);
    setFailure(null);
    setNotice("");
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const out = await putSettings({ ...v, reconCurrency: v.reconCurrency.trim().toUpperCase(), fxTolerancePct: v.fxTolerancePct.trim(), fxAdjustMaxPct: v.fxAdjustMaxPct.trim() });
      setValues(out);
      setNotice("The settings were saved.");
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      {header}
      {notice ? (
        <Alert tone="success" title="Saved" live="status">
          {notice}
        </Alert>
      ) : null}
      <Card>
        <form className={styles.form} onSubmit={(e) => void save(e)} noValidate aria-label="Intercompany settings">
          <FormField id="set-mode" label="Charges settle by" hint="A journal posts both sides at once; an invoice asks billing and payables to raise the invoice and the bill.">
            {(c) => (
              <Select {...c} value={v.chargeSettlementMode} onChange={(e) => set({ chargeSettlementMode: e.target.value as IcSettings["chargeSettlementMode"] })}>
                <option value="Journal">Journal</option>
                <option value="Invoice">Invoice</option>
              </Select>
            )}
          </FormField>
          <FormField id="set-tax" label="Cross-border tax" hint="Ask the tax service for the tax of a charge between entities in different countries.">
            {(c) => (
              <Select {...c} value={v.crossBorderTax ? "yes" : "no"} onChange={(e) => set({ crossBorderTax: e.target.value === "yes" })}>
                <option value="no">Off</option>
                <option value="yes">On</option>
              </Select>
            )}
          </FormField>
          <FormField id="set-recon" label="Scheduled reconciliation currency" optional hint="The nightly reconciliation compares both sides of each pair in this currency. Leave empty to switch it off." error={errors.reconCurrency}>
            {(c) => <Input {...c} className={styles.mono} value={v.reconCurrency} onChange={(e) => set({ reconCurrency: e.target.value })} invalid={Boolean(errors.reconCurrency)} />}
          </FormField>
          <FormField id="set-tol" label="FX tolerance percent" hint="A pair in two currencies within this percent of the larger side matches." error={errors.fxTolerancePct}>
            {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={v.fxTolerancePct} onChange={(e) => set({ fxTolerancePct: e.target.value })} invalid={Boolean(errors.fxTolerancePct)} />}
          </FormField>
          <FormField id="set-adj" label="Largest FX adjustment percent" hint="An FX adjustment books at most this percent of the larger side; a bigger difference needs a correcting entry." error={errors.fxAdjustMaxPct}>
            {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={v.fxAdjustMaxPct} onChange={(e) => set({ fxAdjustMaxPct: e.target.value })} invalid={Boolean(errors.fxAdjustMaxPct)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Failure title="The settings were not saved" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save settings
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}

// ---- reconciliation read from the ledgers ----

/** Starts a reconciliation that reads each pair of the stored matrix from the ledgers, then opens the stored run. */
export function LedgerReconcilePage() {
  const navigate = useNavigate();
  const [v, setV] = useState({ period: "", currency: "", fxTolerancePct: "" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const e: Record<string, string> = {};
    if (!PERIOD.test(v.period)) e.period = "Enter the period, such as 2026-11.";
    if (!isCurrency(v.currency.trim().toUpperCase())) e.currency = "Enter the currency both sides are compared in.";
    if (v.fxTolerancePct.trim() !== "" && !isDecimal(v.fxTolerancePct, 4)) e.fxTolerancePct = "Enter a percentage, such as 0.5.";
    setErrors(e);
    setFailure(null);
    if (Object.keys(e).length > 0) return;
    setBusy(true);
    try {
      const run = await runLedgerReconciliation({ period: v.period, currency: v.currency.trim().toUpperCase(), ...(v.fxTolerancePct.trim() ? { fxTolerancePct: v.fxTolerancePct.trim() } : {}) }, key.current);
      navigate(`/intercompany/reconciliation-runs/${run.id}`);
    } catch (err) {
      const problem = describeProblem(err);
      if (problem.status > 0 && problem.status < 500) key.current = newKey();
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Reconcile from the ledgers"
        description="Reads each pair of the accounting matrix from both entities' ledgers up to the end of the period, converts both sides at the closing rate and stores the run. Open items in two currencies can then have their exchange difference booked."
        actions={<Link to="/intercompany/reconciliation-runs">All runs</Link>}
      />
      <Card>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label="Reconcile from the ledgers">
          <FormField id="lr-period" label="Period" hint="Such as 2026-11." error={errors.period}>
            {(c) => <Input {...c} className={styles.mono} value={v.period} onChange={(e) => setV({ ...v, period: e.target.value })} invalid={Boolean(errors.period)} />}
          </FormField>
          <FormField id="lr-currency" label="Compare in currency" error={errors.currency}>
            {(c) => <Input {...c} className={styles.mono} value={v.currency} onChange={(e) => setV({ ...v, currency: e.target.value })} invalid={Boolean(errors.currency)} />}
          </FormField>
          <FormField id="lr-tol" label="FX tolerance percent" optional hint="Leave empty for the tenant's setting." error={errors.fxTolerancePct}>
            {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={v.fxTolerancePct} onChange={(e) => setV({ ...v, fxTolerancePct: e.target.value })} invalid={Boolean(errors.fxTolerancePct)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Failure title="The reconciliation did not run" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Read the ledgers and reconcile
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}
