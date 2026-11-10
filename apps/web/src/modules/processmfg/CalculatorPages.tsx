import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  ALLOCATION_METHODS,
  allocateCoProducts as defaultAllocate,
  BY_PRODUCT_COST_TYPES,
  consumePotency as defaultConsume,
  describeProblem,
  type AllocateRequest,
  type AllocateResult,
  type Allocated,
  type AllocationMethod,
  type ByProductCostType,
  type PotencyRequest,
  type PotencyResult,
  type Problem,
} from "./api/client";
import { QUANTITY } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const POTENCY_FIELDS: { name: keyof PotencyRequest; label: string; hint?: string }[] = [
  { name: "planned", label: "Planned active ingredient", hint: "Quantity at standard potency." },
  { name: "standardPotency", label: "Standard potency (%)" },
  { name: "batchPotency", label: "Batch potency (%)", hint: "Recorded on the picked batch." },
  { name: "compensatingPlanned", label: "Planned filler", hint: "The compensating line that keeps the batch size." },
];

/** BR-24.2: consumption of an active ingredient by the potency of the picked batch. */
export function PotencyPage({ consume = defaultConsume }: { consume?: (req: PotencyRequest) => Promise<PotencyResult> }) {
  // The BR-24.2 example: 100 kg at 50% standard, batch at 40%.
  const [form, setForm] = useState<PotencyRequest>({ planned: "100", standardPotency: "50", batchPotency: "40", compensatingPlanned: "100" });
  const [errors, setErrors] = useState<Partial<Record<keyof PotencyRequest, string>>>({});
  const [result, setResult] = useState<PotencyResult | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: typeof errors = {};
    for (const f of POTENCY_FIELDS) {
      if (!QUANTITY.test(form[f.name])) found[f.name] = "Enter a number with at most 6 decimals.";
    }
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setResult(await consume(form));
    } catch (error) {
      setResult(null);
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Potency consumption" description="Consumption is planned × standard potency ÷ batch potency; the filler line absorbs the difference so the batch size is kept." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {POTENCY_FIELDS.map((f) => (
            <FormField key={f.name} id={`potency-${f.name}`} label={f.label} hint={f.hint} error={errors[f.name]}>
              {(c) => <Input {...c} inputMode="decimal" value={form[f.name]} onChange={(e) => setForm({ ...form, [f.name]: e.target.value })} invalid={Boolean(errors[f.name])} />}
            </FormField>
          ))}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Calculate consumption
            </Button>
          </div>
        </form>
      </Card>
      {failure ? (
        <Alert tone="danger" title="Consumption refused" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {result ? (
        <StatGroup label="Consumption">
          <StatTile label="Active ingredient" value={result.qty} />
          <StatTile label="Filler" value={result.compensatingQty} />
        </StatGroup>
      ) : null}
    </div>
  );
}

const METHOD_LABELS: Record<AllocationMethod, string> = {
  tca: "Total cost allocation (standard cost × quantity)",
  quantity: "By quantity",
  manual: "Manual percentages",
  none: "None (all cost to the main product)",
};

interface ProductRow {
  itemId: string;
  standardCost: string;
  rafQty: string;
  percent: string;
}

const allocatedColumns: DataTableColumn<Allocated & { role: string }>[] = [
  { key: "role", header: "Role" },
  { key: "itemId", header: "Item", mono: true },
  { key: "percent", header: "Share %", align: "end" },
  { key: "amount", header: "Amount", align: "end" },
];

/** FR-24.4 / BR-24.3: preview how a batch's cost splits over main, co- and by-products. */
export function AllocationPage({ allocate = defaultAllocate }: { allocate?: (req: AllocateRequest) => Promise<AllocateResult> }) {
  // The BR-24.3 fixture: USD 10,000; A 200 at 40, B 100 at 20; by-product C 50 at 1.50 per unit.
  const [batchCost, setBatchCost] = useState("10000.00");
  const [currency, setCurrency] = useState("USD");
  const [method, setMethod] = useState<AllocationMethod>("tca");
  const [main, setMain] = useState<ProductRow>({ itemId: "A", standardCost: "40", rafQty: "200", percent: "80" });
  const [co, setCo] = useState<ProductRow>({ itemId: "B", standardCost: "20", rafQty: "100", percent: "20" });
  const [by, setBy] = useState<{ itemId: string; costType: ByProductCostType; qty: string; rate: string }>({ itemId: "C", costType: "per-quantity", qty: "50", rate: "1.50" });
  const [result, setResult] = useState<AllocateResult | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setBusy(true);
    try {
      setResult(
        await allocate({
          batchCost,
          currency: currency.trim().toUpperCase(),
          method,
          main,
          coProducts: co.itemId.trim() ? [co] : [],
          byProducts: by.itemId.trim() ? [by] : [],
        }),
      );
    } catch (error) {
      setResult(null);
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  function productFields(prefix: string, row: ProductRow, set: (r: ProductRow) => void) {
    return (
      <>
        <FormField id={`${prefix}-item`} label="Item">
          {(c) => <Input {...c} value={row.itemId} onChange={(e) => set({ ...row, itemId: e.target.value })} />}
        </FormField>
        <FormField id={`${prefix}-qty`} label="Quantity reported">
          {(c) => <Input {...c} inputMode="decimal" value={row.rafQty} onChange={(e) => set({ ...row, rafQty: e.target.value })} />}
        </FormField>
        <FormField id={`${prefix}-cost`} label="Standard cost">
          {(c) => <Input {...c} inputMode="decimal" value={row.standardCost} onChange={(e) => set({ ...row, standardCost: e.target.value })} />}
        </FormField>
        <FormField id={`${prefix}-pct`} label="Manual %">
          {(c) => <Input {...c} inputMode="decimal" value={row.percent} onChange={(e) => set({ ...row, percent: e.target.value })} />}
        </FormField>
      </>
    );
  }

  const rows = result
    ? [
        { ...result.main, role: "Main product" },
        ...result.coProducts.map((r) => ({ ...r, role: "Co-product" })),
        ...result.byProductBurdens.map((r) => ({ ...r, role: "By-product burden" })),
      ]
    : [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Co-product allocation"
        description="By-product burdens come off the batch cost first; the rest splits by the method, and the rounding residual goes to the main product so the parts add up exactly."
      />
      <form className={styles.stack} onSubmit={submit} noValidate>
        <Card title="Batch">
          <div className={styles.form}>
            <FormField id="alloc-cost" label="Batch cost">
              {(c) => <Input {...c} inputMode="decimal" value={batchCost} onChange={(e) => setBatchCost(e.target.value)} />}
            </FormField>
            <FormField id="alloc-currency" label="Currency">
              {(c) => <Input {...c} value={currency} maxLength={3} onChange={(e) => setCurrency(e.target.value)} />}
            </FormField>
            <FormField id="alloc-method" label="Method">
              {(c) => (
                <Select {...c} value={method} onChange={(e) => setMethod(e.target.value as AllocationMethod)}>
                  {ALLOCATION_METHODS.map((m) => (
                    <option key={m} value={m}>
                      {METHOD_LABELS[m]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
        </Card>
        <Card title="Main product">
          <div className={styles.form}>{productFields("main", main, setMain)}</div>
        </Card>
        <Card title="Co-product">
          <div className={styles.form}>{productFields("co", co, setCo)}</div>
        </Card>
        <Card title="By-product">
          <div className={styles.form}>
            <FormField id="by-item" label="Item" hint="Leave empty for none.">
              {(c) => <Input {...c} value={by.itemId} onChange={(e) => setBy({ ...by, itemId: e.target.value })} />}
            </FormField>
            <FormField id="by-type" label="Burden type">
              {(c) => (
                <Select {...c} value={by.costType} onChange={(e) => setBy({ ...by, costType: e.target.value as ByProductCostType })}>
                  {BY_PRODUCT_COST_TYPES.map((t) => (
                    <option key={t} value={t}>
                      {t}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="by-qty" label="Quantity">
              {(c) => <Input {...c} inputMode="decimal" value={by.qty} onChange={(e) => setBy({ ...by, qty: e.target.value })} />}
            </FormField>
            <FormField id="by-rate" label="Rate" hint="Per unit, or % of batch cost for percent.">
              {(c) => <Input {...c} inputMode="decimal" value={by.rate} onChange={(e) => setBy({ ...by, rate: e.target.value })} />}
            </FormField>
          </div>
        </Card>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Preview allocation
          </Button>
        </div>
      </form>
      {failure ? (
        <Alert tone="danger" title="Allocation refused" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {result ? (
        <Card title={`Allocation in ${result.currency}`}>
          <div className={styles.stack}>
            <DataTable label="Allocated cost" columns={allocatedColumns} rows={rows} getRowKey={(r, i) => `${r.role}-${r.itemId}-${i}`} />
            <p className={styles.note} role="status">{`Total ${result.total} ${result.currency}`}</p>
          </div>
        </Card>
      ) : null}
    </div>
  );
}
