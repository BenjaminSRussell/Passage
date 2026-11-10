import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  type DataTableColumn,
} from "../../shell/ui";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import {
  checkStoredRequirements as defaultCheck,
  createCustomerRequirement,
  describeProblem,
  getCustomerRequirement,
  listBatches as defaultListBatches,
  listCustomerRequirements,
  newKey,
  retireCustomerRequirement,
  type Batch,
  type CustomerRequirement,
  type NewCustomerRequirement,
  type Problem,
  type StoredRequirementCheck,
  type StoredRequirementCheckRequest,
} from "./api/client";
import { dash, QUANTITY, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Duty that defines and retires customer batch requirements; enforced by the service (FR-24.19). */
export const REQUIREMENT_DUTY = "production.batch-requirement.write";

const NUMBER = /^-?\d{1,12}(\.\d{1,6})?$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

const SCOPE: Record<number, string> = { 1: "Item default", 2: "Customer group", 3: "Customer" };

function scopeText(r: CustomerRequirement): string {
  if (r.customerId) return `Customer ${shortId(r.customerId)}`;
  if (r.customerGroupId) return `Customer group ${shortId(r.customerGroupId)}`;
  return SCOPE[1];
}

function rangeText(r: CustomerRequirement): string {
  if (!r.attribute) return dash;
  return `${r.attribute} ${r.min ?? "…"} to ${r.max ?? "…"}`;
}

function validText(r: CustomerRequirement): string {
  if (!r.validFrom && !r.validTo) return "Always";
  return `${r.validFrom ?? "…"} to ${r.validTo ?? "…"}`;
}

function StatusBadge({ rule }: { rule: CustomerRequirement }) {
  return rule.retiredAt ? <Badge tone="neutral">Retired</Badge> : <Badge tone="success">Active</Badge>;
}

function LoadError({ title, error, reload }: { title: string; error: Problem; reload: () => void }) {
  return (
    <Alert tone="danger" title={title} live="alert" action={<Button size="sm" onClick={reload}>Try again</Button>}>
      {error.message} ({error.code})
    </Alert>
  );
}

const columns: DataTableColumn<CustomerRequirement>[] = [
  { key: "level", header: "Applies to", render: scopeText },
  { key: "itemId", header: "Item", mono: true, render: (r) => shortId(r.itemId) },
  { key: "sellableDays", header: "Sellable days", align: "end", render: (r) => (r.sellableDays === undefined ? dash : String(r.sellableDays)) },
  { key: "attribute", header: "Attribute range", render: rangeText },
  { key: "sameBatch", header: "Same batch", render: (r) => (r.sameBatch === undefined ? dash : r.sameBatch ? "Yes" : "No") },
  { key: "validFrom", header: "Valid", render: validText },
  { key: "retiredAt", header: "Status", render: (r) => <StatusBadge rule={r} /> },
];

/** FR-24.19: the customer batch requirements, filtered by item and customer. */
export function CustomerRequirementsPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), REQUIREMENT_DUTY);
  const [itemId, setItemId] = useState("");
  const [customerId, setCustomerId] = useState("");
  const item = itemId.trim();
  const customer = customerId.trim();
  const filterError = (item !== "" && !UUID.test(item)) || (customer !== "" && !UUID.test(customer));
  const state = useLoad(
    (signal) => (filterError ? Promise.resolve([]) : listCustomerRequirements({ itemId: item, customerId: customer }, signal)),
    `requirements|${item}|${customer}|${filterError}`,
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Customer batch requirements"
        description="Rules a batch must meet to be sold to a customer: the shelf life left at shipment, an attribute range and whether a line must come from one batch. A customer's rule wins over their group's, which wins over the item default."
        actions={
          canWrite ? (
            <Link className={buttonClassName("primary")} to="/processmfg/customer-requirements/new">
              Define requirement
            </Link>
          ) : undefined
        }
      />
      <div className={styles.toolbar}>
        <FormField id="req-filter-item" label="Item" error={item !== "" && !UUID.test(item) ? "Enter the item id as a UUID." : undefined}>
          {(c) => <EntityLookup entity="product" {...c} value={itemId} onChange={(e) => setItemId(e.target.value)} invalid={item !== "" && !UUID.test(item)} />}
        </FormField>
        <FormField id="req-filter-customer" label="Customer" error={customer !== "" && !UUID.test(customer) ? "Choose a customer." : undefined}>
          {(c) => <EntityLookup entity="customer" {...c} value={customerId} onChange={(e) => setCustomerId(e.target.value)} invalid={customer !== "" && !UUID.test(customer)} />}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load the requirements" error={state.error} reload={state.reload} />
      ) : (
        <DataTable
          label="Customer batch requirements"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(r) => r.id}
          loading={state.loading}
          empty={
            item || customer
              ? { title: "No requirements match", description: "Clear the filters to see every rule." }
              : { title: "No customer batch requirements yet", description: "Without a rule, every available, unexpired batch qualifies." }
          }
          onRowOpen={(r) => navigate(`/processmfg/customer-requirements/${r.id}`)}
        />
      )}
    </div>
  );
}

/** Defines a customer batch requirement. */
export function NewCustomerRequirementPage() {
  const navigate = useNavigate();
  const allowed = holdsDuty(usePrincipalDuties(), REQUIREMENT_DUTY);
  const [itemId, setItemId] = useState("");
  const [scope, setScope] = useState<"item" | "group" | "customer">("customer");
  const [partyId, setPartyId] = useState("");
  const [days, setDays] = useState("");
  const [attribute, setAttribute] = useState("");
  const [min, setMin] = useState("");
  const [max, setMax] = useState("");
  const [sameBatch, setSameBatch] = useState(false);
  const [validFrom, setValidFrom] = useState("");
  const [validTo, setValidTo] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function on<T>(set: (v: T) => void) {
    return (v: T) => {
      set(v);
      key.current = newKey();
    };
  }

  if (!allowed) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Define requirement" />
        <Card title="Customer batch requirement">
          <p className={styles.note}>Defining requirements needs the customer batch requirements duty. Ask your administrator if you should have it.</p>
        </Card>
      </div>
    );
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!UUID.test(itemId.trim())) found.itemId = "Enter the item id as a UUID.";
    if (scope !== "item" && !UUID.test(partyId.trim())) found.partyId = `Choose a ${scope === "group" ? "customer group" : "customer"}.`;
    const d = days.trim();
    if (d !== "" && (!/^\d{1,4}$/.test(d) || Number(d) > 3650)) found.days = "Enter whole days from 0 to 3650.";
    const name = attribute.trim();
    if (name.length > 40) found.attribute = "At most 40 characters.";
    if (name !== "" && min.trim() === "" && max.trim() === "") found.min = "Enter a minimum or a maximum for the attribute.";
    if (name === "" && (min.trim() !== "" || max.trim() !== "")) found.attribute = "Name the attribute the range is for.";
    for (const [field, value] of [["min", min], ["max", max]] as const) {
      if (value.trim() !== "" && !NUMBER.test(value.trim())) found[field] = "Enter a number with at most 6 decimals.";
    }
    if (!found.min && !found.max && min.trim() !== "" && max.trim() !== "" && Number(min) > Number(max)) found.max = "The maximum is below the minimum.";
    for (const [field, value] of [["validFrom", validFrom], ["validTo", validTo]] as const) {
      if (value.trim() !== "" && !DATE.test(value.trim())) found[field] = "Enter a date as yyyy-mm-dd.";
    }
    if (!found.validFrom && !found.validTo && validFrom.trim() !== "" && validTo.trim() !== "" && validFrom.trim() > validTo.trim()) {
      found.validTo = "The end is before the start.";
    }
    if (d === "" && name === "" && !sameBatch) found.form = "Set the sellable days, an attribute range or the same-batch flag.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const input: NewCustomerRequirement = {
      itemId: itemId.trim(),
      ...(scope === "customer" ? { customerId: partyId.trim() } : {}),
      ...(scope === "group" ? { customerGroupId: partyId.trim() } : {}),
      ...(d !== "" ? { sellableDays: Number(d) } : {}),
      ...(name !== "" ? { attribute: name } : {}),
      ...(min.trim() !== "" ? { min: min.trim() } : {}),
      ...(max.trim() !== "" ? { max: max.trim() } : {}),
      ...(sameBatch ? { sameBatch: true } : {}),
      ...(validFrom.trim() !== "" ? { validFrom: validFrom.trim() } : {}),
      ...(validTo.trim() !== "" ? { validTo: validTo.trim() } : {}),
    };
    setBusy(true);
    try {
      const rule = await createCustomerRequirement(input, key.current);
      navigate(`/processmfg/customer-requirements/${rule.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      // A refused command can be corrected and retried under a new key; an answer lost in transit is retried under the same one.
      if (problem.code !== "COM-0500") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Define requirement" description="A rule is never edited: retire it, or define its successor with a later valid-from date, which takes over from that day. Leave a field empty to leave that decision to a less specific rule." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="req-item" label="Item" error={errors.itemId}>
            {(c) => <EntityLookup entity="product" {...c} value={itemId} onChange={(e) => on(setItemId)(e.target.value)} invalid={Boolean(errors.itemId)} />}
          </FormField>
          <FormField id="req-scope" label="Applies to">
            {(c) => (
              <Select {...c} value={scope} onChange={(e) => on(setScope)(e.target.value as typeof scope)}>
                <option value="customer">A customer</option>
                <option value="group">A customer group</option>
                <option value="item">Every customer (item default)</option>
              </Select>
            )}
          </FormField>
          {scope !== "item" ? (
            <FormField id="req-party" label={scope === "group" ? "Customer group id" : "Customer id"} error={errors.partyId}>
              {(c) => <Input {...c} value={partyId} onChange={(e) => on(setPartyId)(e.target.value)} invalid={Boolean(errors.partyId)} />}
            </FormField>
          ) : null}
          <FormField id="req-days" label="Sellable days" hint="Minimum shelf life left at shipment, in days." error={errors.days}>
            {(c) => <Input {...c} inputMode="numeric" value={days} onChange={(e) => on(setDays)(e.target.value)} invalid={Boolean(errors.days)} />}
          </FormField>
          <FormField id="req-attribute" label="Attribute" hint="The batch attribute a value range applies to." error={errors.attribute}>
            {(c) => <Input {...c} value={attribute} onChange={(e) => on(setAttribute)(e.target.value)} invalid={Boolean(errors.attribute)} />}
          </FormField>
          <FormField id="req-min" label="Attribute minimum" error={errors.min}>
            {(c) => <Input {...c} inputMode="decimal" value={min} onChange={(e) => on(setMin)(e.target.value)} invalid={Boolean(errors.min)} />}
          </FormField>
          <FormField id="req-max" label="Attribute maximum" error={errors.max}>
            {(c) => <Input {...c} inputMode="decimal" value={max} onChange={(e) => on(setMax)(e.target.value)} invalid={Boolean(errors.max)} />}
          </FormField>
          <FormField id="req-valid-from" label="Valid from" hint="yyyy-mm-dd; empty for no start." error={errors.validFrom}>
            {(c) => <Input {...c} value={validFrom} onChange={(e) => on(setValidFrom)(e.target.value)} invalid={Boolean(errors.validFrom)} />}
          </FormField>
          <FormField id="req-valid-to" label="Valid to" hint="yyyy-mm-dd; the last day the rule applies." error={errors.validTo}>
            {(c) => <Input {...c} value={validTo} onChange={(e) => on(setValidTo)(e.target.value)} invalid={Boolean(errors.validTo)} />}
          </FormField>
          <div className={styles.lineChecks}>
            <label>
              <input type="checkbox" checked={sameBatch} onChange={(e) => on(setSameBatch)(e.target.checked)} /> The whole line must come from one batch
            </label>
          </div>
          {errors.form ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Nothing to enforce" live="alert">{errors.form}</Alert>
            </div>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not define the requirement" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Define requirement
            </Button>
            <Link className={buttonClassName("ghost")} to="/processmfg/customer-requirements">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

/** One requirement, with retire for a holder of the duty. */
export function CustomerRequirementDetailPage() {
  const { id = "" } = useParams();
  const canWrite = holdsDuty(usePrincipalDuties(), REQUIREMENT_DUTY);
  const state = useLoad((signal) => getCustomerRequirement(id, signal), id);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Customer batch requirement" />
        <LoadError title="Could not load the requirement" error={state.error} reload={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Customer batch requirement" />
        <span className="visually-hidden">Loading the requirement…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const rule = state.data;

  async function retire() {
    setBusy(true);
    setFailure(null);
    try {
      await retireCustomerRequirement(rule.id, rule.version, key.current);
      key.current = newKey();
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code !== "COM-0500") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title={scopeText(rule)} description={`Item ${rule.itemId}`} meta={<StatusBadge rule={rule} />} />
      <StatGroup label="Requirement">
        <StatTile label="Applies to" value={SCOPE[rule.level] ?? dash} />
        <StatTile label="Sellable days" value={rule.sellableDays === undefined ? dash : String(rule.sellableDays)} />
        <StatTile label="Attribute range" value={rangeText(rule)} />
        <StatTile label="Same batch" value={rule.sameBatch === undefined ? dash : rule.sameBatch ? "Yes" : "No"} />
        <StatTile label="Valid" value={validText(rule)} />
      </StatGroup>
      {rule.retiredAt ? (
        <Alert tone="warning" title="This requirement is retired" live="status">
          Retired by {rule.retiredBy ?? "unknown"}; it no longer applies.
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="Could not retire the requirement" live="alert" action={failure.code === "PMF-0104" ? <Button size="sm" onClick={state.reload}>Reload</Button> : undefined}>
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <div className={styles.formActions}>
        {canWrite && !rule.retiredAt ? (
          <Button variant="danger" onClick={retire} loading={busy}>
            Retire requirement
          </Button>
        ) : null}
        <Link className={buttonClassName("ghost")} to="/processmfg/customer-requirements">
          Back to the list
        </Link>
      </div>
    </div>
  );
}

interface CheckProps {
  check?: (req: StoredRequirementCheckRequest) => Promise<StoredRequirementCheck>;
  list?: (filter: { itemId?: string }, signal?: AbortSignal) => Promise<Batch[]>;
}

/**
 * FR-24.19, AC-24.14: checks stored batches against the stored requirements of
 * a customer. The rules, dispositions, expiry and attribute values come from
 * the service; only the on-hand quantity per batch is typed, because the
 * register keeps none.
 */
export function StoredRequirementCheckPage({ check = defaultCheck, list = defaultListBatches }: CheckProps) {
  const [itemId, setItemId] = useState("");
  const [customerId, setCustomerId] = useState("");
  const [groupId, setGroupId] = useState("");
  const [shipDate, setShipDate] = useState("");
  const [qty, setQty] = useState("");
  const [onHand, setOnHand] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<StoredRequirementCheck | null>(null);
  const [busy, setBusy] = useState(false);

  const item = itemId.trim();
  const itemValid = UUID.test(item);
  const batches = useLoad<Batch[]>((signal) => (itemValid ? list({ itemId: item }, signal) : Promise.resolve([])), `req-check|${itemValid ? item : ""}`);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!itemValid) found.itemId = "Enter the item id as a UUID.";
    if (customerId.trim() !== "" && !UUID.test(customerId.trim())) found.customerId = "Choose a customer.";
    if (groupId.trim() !== "" && !UUID.test(groupId.trim())) found.groupId = "Enter the customer group id as a UUID.";
    if (!DATE.test(shipDate.trim())) found.shipDate = "Enter the ship date as yyyy-mm-dd.";
    if (!QUANTITY.test(qty.trim()) || Number(qty) === 0) found.qty = "Enter the line quantity above zero.";
    const chosen = Object.entries(onHand).filter(([, v]) => v.trim() !== "");
    if (itemValid && chosen.length === 0) found.batches = "Enter the on-hand quantity of at least one batch.";
    if (chosen.some(([, v]) => !QUANTITY.test(v.trim()))) found.batches = "On-hand quantities are numbers with at most 6 decimals.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setResult(
        await check({
          itemId: item,
          ...(customerId.trim() ? { customerId: customerId.trim() } : {}),
          ...(groupId.trim() ? { customerGroupId: groupId.trim() } : {}),
          shipDate: shipDate.trim(),
          qty: qty.trim(),
          onHand: chosen.map(([batchId, v]) => ({ batchId, qty: v.trim() })),
        }),
      );
    } catch (e) {
      setFailure(describeProblem(e));
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  const batchColumns: DataTableColumn<Batch>[] = [
    { key: "batchNumber", header: "Batch", mono: true },
    { key: "disposition", header: "Disposition" },
    { key: "expiryDate", header: "Expiry", render: (b) => b.expiryDate ?? dash },
    {
      key: "id",
      header: "On hand",
      render: (b) => (
        <Input
          aria-label={`On hand of batch ${b.batchNumber}`}
          inputMode="decimal"
          value={onHand[b.id] ?? ""}
          onChange={(e) => setOnHand({ ...onHand, [b.id]: e.target.value })}
        />
      ),
    },
  ];
  const numbers = result?.batchNumbers ?? {};
  const name = (id: string) => numbers[id] ?? shortId(id);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Check stored batches against customer requirements"
        description="Splits the batches of an item into those that qualify for a customer's line and those that do not, with the reason. The rules in force on the ship date are read from the stored requirements."
      />
      <form className={styles.stack} onSubmit={submit} noValidate>
        <Card title="Line">
          <div className={styles.form}>
            <FormField id="rc-item" label="Item" error={errors.itemId}>
              {(c) => <EntityLookup entity="product" {...c} value={itemId} onChange={(e) => setItemId(e.target.value)} invalid={Boolean(errors.itemId)} />}
            </FormField>
            <FormField id="rc-customer" label="Customer" hint="Leave empty to apply the item default only." error={errors.customerId}>
              {(c) => <EntityLookup entity="customer" {...c} value={customerId} onChange={(e) => setCustomerId(e.target.value)} invalid={Boolean(errors.customerId)} />}
            </FormField>
            <FormField id="rc-group" label="Customer group id" error={errors.groupId}>
              {(c) => <Input {...c} value={groupId} onChange={(e) => setGroupId(e.target.value)} invalid={Boolean(errors.groupId)} />}
            </FormField>
            <FormField id="rc-ship" label="Ship date" hint="yyyy-mm-dd" error={errors.shipDate}>
              {(c) => <Input {...c} value={shipDate} onChange={(e) => setShipDate(e.target.value)} invalid={Boolean(errors.shipDate)} />}
            </FormField>
            <FormField id="rc-qty" label="Line quantity" error={errors.qty}>
              {(c) => <Input {...c} inputMode="decimal" value={qty} onChange={(e) => setQty(e.target.value)} invalid={Boolean(errors.qty)} />}
            </FormField>
          </div>
        </Card>
        {itemValid ? (
          <Card title="Batches of the item">
            {batches.error ? (
              <LoadError title="Could not load batches" error={batches.error} reload={batches.reload} />
            ) : (
              <DataTable
                label="Batches of the item"
                columns={batchColumns}
                rows={batches.data ?? []}
                getRowKey={(b) => b.id}
                loading={batches.loading}
                empty={{ title: "No batches registered for this item", description: "Register the batches and record their attributes first." }}
              />
            )}
            {errors.batches ? <Alert tone="danger" title="Nothing to check">{errors.batches}</Alert> : null}
          </Card>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Check batches
          </Button>
        </div>
      </form>
      {failure ? (
        <Alert tone="danger" title="Check refused" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {result ? (
        <>
          <StatGroup label="Resolved requirement">
            <StatTile label="Sellable days" value={String(result.requirement.sellableDays)} />
            <StatTile label="Same batch" value={result.requirement.sameBatch ? "Yes" : "No"} />
            <StatTile label="Qualifying quantity" value={result.qtyAvailable} />
            <StatTile label="Rules applied" value={String(result.applied.length)} />
          </StatGroup>
          {result.sameBatchId ? (
            <Alert tone="success" title={`Same-batch pick: ${name(result.sameBatchId)}`} live="status" />
          ) : null}
          <Card title="Qualifying batches">
            <DataTable
              label="Qualifying batches"
              columns={[
                { key: "batchId", header: "Batch", mono: true, render: (q) => <Link to={`/processmfg/batches/${q.batchId}`}>{name(q.batchId)}</Link> },
                { key: "qty", header: "On hand", align: "end" },
                { key: "expiry", header: "Expiry", render: (q) => q.expiry ?? dash },
              ]}
              rows={result.qualifying}
              getRowKey={(q) => q.batchId}
              empty={{ title: "No batch qualifies", description: "Every batch fails a rule; see the excluded batches." }}
            />
          </Card>
          <Card title="Excluded batches">
            <DataTable
              label="Excluded batches"
              columns={[
                { key: "batchId", header: "Batch", mono: true, render: (x) => <Link to={`/processmfg/batches/${x.batchId}`}>{name(x.batchId)}</Link> },
                { key: "warningCode", header: "Code", mono: true },
                { key: "reason", header: "Reason" },
              ]}
              rows={result.excluded}
              getRowKey={(x) => x.batchId}
              empty={{ title: "No batch is excluded", description: "Every batch meets the requirement." }}
            />
          </Card>
        </>
      ) : null}
    </div>
  );
}
