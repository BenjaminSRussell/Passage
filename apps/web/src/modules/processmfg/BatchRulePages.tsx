import { useState, type FormEvent, type ReactNode } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  AGGREGATIONS,
  checkApproval as defaultCheckApproval,
  checkRequirements as defaultCheckRequirements,
  describeProblem,
  DISPOSITIONS,
  EXCEPTION_DISPOSITIONS,
  previewConsolidation as defaultConsolidate,
  previewDerivation as defaultDerive,
  previewMerge as defaultMerge,
  REQUIREMENT_LEVELS,
  type Aggregation,
  type ApproveCheckRequest,
  type ApproveCheckResult,
  type AttributeValue,
  type CheckRequest,
  type CheckResult,
  type ConsolidateRequest,
  type ConsolidateResult,
  type DeriveRequest,
  type DeriveResult,
  type Disposition,
  type ExceptionDisposition,
  type MergeRequest,
  type MergeResult,
  type Problem,
  type RequirementLevel,
} from "./api/client";
import { dash, QUANTITY } from "./format";
import styles from "./pages.module.css";

/** A signed decimal with at most 12 integer digits and 6 decimals. */
const NUMBER = /^-?\d{1,12}(\.\d{1,6})?$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const DAYS = /^\d{1,4}$/;

/** Runs one preview call and keeps its busy, failure and result state. */
function usePreview<Req, Res>(call: (req: Req) => Promise<Res>) {
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<Res | null>(null);
  async function run(req: Req) {
    setBusy(true);
    setFailure(null);
    try {
      setResult(await call(req));
    } catch (error) {
      setResult(null);
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }
  return { busy, failure, result, run };
}

/** Editable rows with add and remove. */
function useRows<T>(initial: T[], blank: () => T) {
  const [rows, setRows] = useState<T[]>(initial);
  return {
    rows,
    update: (i: number, patch: Partial<T>) => setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r))),
    add: () => setRows((rs) => [...rs, blank()]),
    remove: (i: number) => setRows((rs) => rs.filter((_, j) => j !== i)),
  };
}

/** The value kind an aggregation reads; copy and fixed take whatever was typed. */
function kindOf(aggregation: Aggregation): "num" | "date" | "any" {
  if (aggregation === "earliest-date" || aggregation === "latest-date") return "date";
  if (aggregation === "copy" || aggregation === "fixed") return "any";
  return "num";
}

/** Turns typed text into an attribute value; "" is a missing value. */
export function toAttributeValue(raw: string, kind: "num" | "date" | "any"): AttributeValue {
  const v = raw.trim();
  if (v === "") return {};
  if (kind === "num" || (kind === "any" && NUMBER.test(v))) return { num: v };
  if (kind === "date" || (kind === "any" && DATE.test(v))) return { date: v };
  return { text: v };
}

export function showValue(v: AttributeValue | undefined): string {
  return v?.num ?? v?.date ?? v?.text ?? dash;
}

function badValue(raw: string, kind: "num" | "date" | "any"): string | undefined {
  const v = raw.trim();
  if (v === "" || kind === "any") return undefined;
  if (kind === "num" && !NUMBER.test(v)) return "Enter a number with at most 6 decimals.";
  if (kind === "date" && !DATE.test(v)) return "Enter a date as yyyy-mm-dd.";
  return undefined;
}

function Refusal({ title, failure }: { title: string; failure: Problem | null }) {
  if (!failure) return null;
  return (
    <Alert tone="danger" title={title} live="alert">
      {failure.message} ({failure.code})
    </Alert>
  );
}

function RowCard({ title, addLabel, onAdd, children }: { title: string; addLabel: string; onAdd(): void; children: ReactNode }) {
  return (
    <Card
      title={title}
      actions={
        <Button size="sm" onClick={onAdd}>
          {addLabel}
        </Button>
      }
    >
      <div className={styles.stack}>{children}</div>
    </Card>
  );
}

function RemoveRow({ label, show, onRemove }: { label: string; show: boolean; onRemove(): void }) {
  if (!show) return null;
  return (
    <div className={styles.lineChecks}>
      <Button size="sm" variant="ghost" onClick={onRemove}>
        {label}
      </Button>
    </div>
  );
}

const AGGREGATION_LABELS: Record<Aggregation, string> = {
  "weighted-average": "Weighted average by consumed quantity",
  min: "Minimum",
  max: "Maximum",
  copy: "Copy (one sender value)",
  "earliest-date": "Earliest date",
  "latest-date": "Latest date",
  fixed: "Fixed value",
};

interface SenderRow {
  batchId: string;
  value: string;
  qty: string;
}

/** FR-24.15, BR-24.11: derive a receiver batch attribute from its sender batches. */
export function DerivationPage({ derive = defaultDerive }: { derive?: (req: DeriveRequest) => Promise<DeriveResult> }) {
  // AC-24.11: 200 kg at 40% and 300 kg at 45% potency.
  const [attribute, setAttribute] = useState("potency");
  const [aggregation, setAggregation] = useState<Aggregation>("weighted-average");
  const [fixed, setFixed] = useState("");
  const senders = useRows<SenderRow>(
    [
      { batchId: "B-101", value: "40", qty: "200" },
      { batchId: "B-102", value: "45", qty: "300" },
    ],
    () => ({ batchId: "", value: "", qty: "" }),
  );
  const [errors, setErrors] = useState<Record<string, string>>({});
  const preview = usePreview(derive);
  const kind = kindOf(aggregation);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!attribute.trim()) found.attribute = "Name the attribute to derive.";
    if (aggregation === "fixed" && !fixed.trim()) found.fixed = "Enter the fixed value.";
    senders.rows.forEach((s, i) => {
      if (!s.batchId.trim()) found[`batch-${i}`] = "Enter the sender batch.";
      const bad = badValue(s.value, kind);
      if (bad) found[`value-${i}`] = bad;
      if (s.qty.trim() !== "" && !QUANTITY.test(s.qty.trim())) found[`qty-${i}`] = "Enter a quantity with at most 6 decimals.";
    });
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void preview.run({
      rule: { attribute: attribute.trim(), aggregation, ...(aggregation === "fixed" ? { fixed: toAttributeValue(fixed, "any") } : {}) },
      senders: aggregation === "fixed" ? [] : senders.rows.map((s) => ({ batchId: s.batchId.trim(), value: toAttributeValue(s.value, kind), qty: s.qty.trim() || "0" })),
    });
  }

  const result = preview.result;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Batch derivation"
        description="Preview how an inheritance rule sets a product batch attribute from the ingredient batches it consumed. Senders without a value are left out and reported."
      />
      <form className={styles.stack} onSubmit={submit} noValidate>
        <Card title="Rule">
          <div className={styles.form}>
            <FormField id="derive-attribute" label="Attribute" error={errors.attribute}>
              {(c) => <Input {...c} value={attribute} onChange={(e) => setAttribute(e.target.value)} invalid={Boolean(errors.attribute)} />}
            </FormField>
            <FormField id="derive-aggregation" label="Aggregation">
              {(c) => (
                <Select {...c} value={aggregation} onChange={(e) => setAggregation(e.target.value as Aggregation)}>
                  {AGGREGATIONS.map((a) => (
                    <option key={a} value={a}>
                      {AGGREGATION_LABELS[a]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {aggregation === "fixed" ? (
              <FormField id="derive-fixed" label="Fixed value" error={errors.fixed}>
                {(c) => <Input {...c} value={fixed} onChange={(e) => setFixed(e.target.value)} invalid={Boolean(errors.fixed)} />}
              </FormField>
            ) : null}
          </div>
        </Card>
        {aggregation !== "fixed" ? (
          <RowCard title="Sender batches" addLabel="Add sender" onAdd={senders.add}>
            {senders.rows.map((s, i) => (
              <fieldset key={i} className={styles.lineRow}>
                <legend className={styles.groupsLegend}>{`Sender ${i + 1}`}</legend>
                <FormField id={`sender-${i}-batch`} label="Batch" error={errors[`batch-${i}`]}>
                  {(c) => <Input {...c} value={s.batchId} onChange={(e) => senders.update(i, { batchId: e.target.value })} invalid={Boolean(errors[`batch-${i}`])} />}
                </FormField>
                <FormField id={`sender-${i}-value`} label="Value" hint={kind === "date" ? "yyyy-mm-dd; empty if not recorded." : "Empty if not recorded."} error={errors[`value-${i}`]}>
                  {(c) => <Input {...c} value={s.value} onChange={(e) => senders.update(i, { value: e.target.value })} invalid={Boolean(errors[`value-${i}`])} />}
                </FormField>
                <FormField id={`sender-${i}-qty`} label="Consumed quantity" error={errors[`qty-${i}`]}>
                  {(c) => <Input {...c} inputMode="decimal" value={s.qty} onChange={(e) => senders.update(i, { qty: e.target.value })} invalid={Boolean(errors[`qty-${i}`])} />}
                </FormField>
                <RemoveRow label={`Remove sender ${i + 1}`} show={senders.rows.length > 1} onRemove={() => senders.remove(i)} />
              </fieldset>
            ))}
          </RowCard>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={preview.busy}>
            Preview derivation
          </Button>
        </div>
      </form>
      <Refusal title="Derivation refused" failure={preview.failure} />
      {result ? (
        <>
          <StatGroup label="Derived value">
            <StatTile label={result.attribute} value={showValue(result.value)} />
            <StatTile label="Missing senders" value={result.missingSenders.length} tone={result.missingSenders.length > 0 ? "warning" : undefined} />
          </StatGroup>
          {result.missingSenders.length > 0 ? (
            <Alert tone="warning" title={`Sender value missing (${result.warningCode ?? "PMF-0007"})`}>
              {`No ${result.attribute} recorded on ${result.missingSenders.join(", ")}. Record it and derive again.`}
            </Alert>
          ) : null}
        </>
      ) : null}
    </div>
  );
}

interface MergeRow {
  id: string;
  qty: string;
  unitCost: string;
  disposition: Disposition;
  expiry: string;
  potency: string;
  grade: string;
}

const blankMergeRow = (): MergeRow => ({ id: "", qty: "", unitCost: "", disposition: "available", expiry: "", potency: "", grade: "" });

/** FR-24.16: the target batch a merge would produce. */
export function MergePage({ merge = defaultMerge }: { merge?: (req: MergeRequest) => Promise<MergeResult> }) {
  // AC-24.12: 100 kg expiring 2027-03-01 and 50 kg expiring 2027-02-01.
  const [itemId, setItemId] = useState("ITEM-1");
  const [warehouseId, setWarehouseId] = useState("WH-1");
  const [gradeOverride, setGradeOverride] = useState(false);
  const batches = useRows<MergeRow>(
    [
      { ...blankMergeRow(), id: "B-201", qty: "100", unitCost: "2", expiry: "2027-03-01" },
      { ...blankMergeRow(), id: "B-202", qty: "50", unitCost: "2", expiry: "2027-02-01" },
    ],
    blankMergeRow,
  );
  const [errors, setErrors] = useState<Record<string, string>>({});
  const preview = usePreview(merge);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (batches.rows.length < 2) found.batches = "A merge needs at least two batches.";
    batches.rows.forEach((b, i) => {
      if (!b.id.trim()) found[`id-${i}`] = "Enter the batch.";
      if (!QUANTITY.test(b.qty.trim())) found[`qty-${i}`] = "Enter a quantity with at most 6 decimals.";
      if (b.unitCost.trim() !== "" && !QUANTITY.test(b.unitCost.trim())) found[`cost-${i}`] = "Enter a cost with at most 6 decimals.";
      if (b.expiry !== "" && !DATE.test(b.expiry)) found[`expiry-${i}`] = "Enter a date.";
      if (b.potency.trim() !== "" && !NUMBER.test(b.potency.trim())) found[`potency-${i}`] = "Enter a number.";
    });
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void preview.run({
      batches: batches.rows.map((b) => {
        const values: Record<string, AttributeValue> = {};
        if (b.expiry) values.expiry = { date: b.expiry };
        if (b.potency.trim()) values.potency = { num: b.potency.trim() };
        if (b.grade.trim()) values.grade = { text: b.grade.trim() };
        return { id: b.id.trim(), itemId: itemId.trim(), warehouseId: warehouseId.trim(), disposition: b.disposition, qty: b.qty.trim(), unitCost: b.unitCost.trim() || "0", values };
      }),
      mergeOverride: gradeOverride ? ["grade"] : [],
    });
  }

  const result = preview.result;
  const attributeRows = result ? Object.entries(result.values).map(([name, value]) => ({ name, value: showValue(value) })) : [];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Batch merge"
        description="Preview the target of merging batches of one item and warehouse: numbers become the quantity-weighted average, dates the earliest, and text must agree unless it may differ."
      />
      <form className={styles.stack} onSubmit={submit} noValidate>
        <Card title="Item and warehouse">
          <div className={styles.form}>
            <FormField id="merge-item" label="Item">
              {(c) => <Input {...c} value={itemId} onChange={(e) => setItemId(e.target.value)} />}
            </FormField>
            <FormField id="merge-warehouse" label="Warehouse">
              {(c) => <Input {...c} value={warehouseId} onChange={(e) => setWarehouseId(e.target.value)} />}
            </FormField>
            <div className={styles.lineChecks}>
              <span className={styles.check}>
                <Input type="checkbox" id="merge-grade-override" checked={gradeOverride} onChange={(e) => setGradeOverride(e.target.checked)} />
                <label htmlFor="merge-grade-override">Grade may differ (merge override)</label>
              </span>
            </div>
          </div>
        </Card>
        <RowCard title="Source batches" addLabel="Add batch" onAdd={batches.add}>
          {batches.rows.map((b, i) => (
            <fieldset key={i} className={styles.lineRow}>
              <legend className={styles.groupsLegend}>{`Batch ${i + 1}`}</legend>
              <FormField id={`merge-${i}-id`} label="Batch" error={errors[`id-${i}`]}>
                {(c) => <Input {...c} value={b.id} onChange={(e) => batches.update(i, { id: e.target.value })} invalid={Boolean(errors[`id-${i}`])} />}
              </FormField>
              <FormField id={`merge-${i}-qty`} label="Quantity" error={errors[`qty-${i}`]}>
                {(c) => <Input {...c} inputMode="decimal" value={b.qty} onChange={(e) => batches.update(i, { qty: e.target.value })} invalid={Boolean(errors[`qty-${i}`])} />}
              </FormField>
              <FormField id={`merge-${i}-cost`} label="Cost per unit" error={errors[`cost-${i}`]}>
                {(c) => <Input {...c} inputMode="decimal" value={b.unitCost} onChange={(e) => batches.update(i, { unitCost: e.target.value })} invalid={Boolean(errors[`cost-${i}`])} />}
              </FormField>
              <FormField id={`merge-${i}-disposition`} label="Disposition">
                {(c) => (
                  <Select {...c} value={b.disposition} onChange={(e) => batches.update(i, { disposition: e.target.value as Disposition })}>
                    {DISPOSITIONS.map((d) => (
                      <option key={d} value={d}>
                        {d}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id={`merge-${i}-expiry`} label="Expiry" error={errors[`expiry-${i}`]}>
                {(c) => <Input {...c} type="date" value={b.expiry} onChange={(e) => batches.update(i, { expiry: e.target.value })} invalid={Boolean(errors[`expiry-${i}`])} />}
              </FormField>
              <FormField id={`merge-${i}-potency`} label="Potency %" error={errors[`potency-${i}`]}>
                {(c) => <Input {...c} inputMode="decimal" value={b.potency} onChange={(e) => batches.update(i, { potency: e.target.value })} invalid={Boolean(errors[`potency-${i}`])} />}
              </FormField>
              <FormField id={`merge-${i}-grade`} label="Grade">
                {(c) => <Input {...c} value={b.grade} onChange={(e) => batches.update(i, { grade: e.target.value })} />}
              </FormField>
              <RemoveRow label={`Remove batch ${i + 1}`} show={batches.rows.length > 2} onRemove={() => batches.remove(i)} />
            </fieldset>
          ))}
          {errors.batches ? (
            <p role="alert" className={styles.fieldError}>
              {errors.batches}
            </p>
          ) : null}
        </RowCard>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={preview.busy}>
            Preview merge
          </Button>
        </div>
      </form>
      <Refusal title="Merge refused" failure={preview.failure} />
      {result ? (
        <Card title="Target batch">
          <div className={styles.stack}>
            <StatGroup label="Target batch">
              <StatTile label="Quantity" value={result.qty} />
              <StatTile label="Cost per unit" value={result.unitCost} />
              <StatTile label="Disposition" value={result.disposition} />
            </StatGroup>
            <DataTable
              label="Target attributes"
              columns={[
                { key: "name", header: "Attribute" },
                { key: "value", header: "Value", align: "end" },
              ]}
              rows={attributeRows}
              getRowKey={(r) => r.name}
              empty={{ title: "No attributes", description: "None of the sources carries an attribute value." }}
            />
            <p className={styles.note} role="status">{`Genealogy parents: ${result.parents.join(", ")}`}</p>
          </div>
        </Card>
      ) : null}
    </div>
  );
}

interface RequirementRow {
  level: RequirementLevel;
  sellableDays: string;
  min: string;
  max: string;
  sameBatch: boolean;
}

interface CandidateRow {
  id: string;
  qty: string;
  disposition: Disposition;
  expiry: string;
  value: string;
}

const LEVEL_LABELS: Record<RequirementLevel, string> = { 3: "Customer", 2: "Customer group", 1: "Item default" };

/** FR-24.19, BR-24.13: which batches meet a customer's requirements for a line. */
export function RequirementCheckPage({ check = defaultCheckRequirements }: { check?: (req: CheckRequest) => Promise<CheckResult> }) {
  // AC-24.14: sellable days 60; batches expiring 45 and 90 days after shipment.
  const [shipDate, setShipDate] = useState("2027-01-01");
  const [qty, setQty] = useState("80");
  const [attribute, setAttribute] = useState("potency");
  const reqs = useRows<RequirementRow>([{ level: 3, sellableDays: "60", min: "", max: "", sameBatch: false }], () => ({
    level: 1,
    sellableDays: "",
    min: "",
    max: "",
    sameBatch: false,
  }));
  const batches = useRows<CandidateRow>(
    [
      { id: "B-301", qty: "100", disposition: "available", expiry: "2027-02-15", value: "" },
      { id: "B-302", qty: "100", disposition: "available", expiry: "2027-04-01", value: "" },
    ],
    () => ({ id: "", qty: "", disposition: "available", expiry: "", value: "" }),
  );
  const [errors, setErrors] = useState<Record<string, string>>({});
  const preview = usePreview(check);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!DATE.test(shipDate)) found.shipDate = "Enter the ship date.";
    if (!QUANTITY.test(qty.trim())) found.qty = "Enter a quantity with at most 6 decimals.";
    reqs.rows.forEach((r, i) => {
      if (r.sellableDays !== "" && !DAYS.test(r.sellableDays)) found[`days-${i}`] = "Enter whole days (0 to 3650).";
      if (r.min !== "" && !NUMBER.test(r.min)) found[`min-${i}`] = "Enter a number.";
      if (r.max !== "" && !NUMBER.test(r.max)) found[`max-${i}`] = "Enter a number.";
    });
    batches.rows.forEach((b, i) => {
      if (!b.id.trim()) found[`id-${i}`] = "Enter the batch.";
      if (!QUANTITY.test(b.qty.trim())) found[`bqty-${i}`] = "Enter a quantity.";
      if (b.value.trim() !== "" && !NUMBER.test(b.value.trim())) found[`value-${i}`] = "Enter a number.";
    });
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    const name = attribute.trim();
    void preview.run({
      shipDate,
      qty: qty.trim(),
      requirements: reqs.rows.map((r) => ({
        level: r.level,
        ...(r.sellableDays !== "" ? { sellableDays: Number(r.sellableDays) } : {}),
        ...(name && (r.min || r.max) ? { attribute: name, ...(r.min ? { min: r.min } : {}), ...(r.max ? { max: r.max } : {}) } : {}),
        ...(r.sameBatch ? { sameBatch: true } : {}),
      })),
      batches: batches.rows.map((b) => {
        const values: Record<string, AttributeValue> = {};
        if (b.expiry) values.expiry = { date: b.expiry };
        if (name && b.value.trim()) values[name] = { num: b.value.trim() };
        return { id: b.id.trim(), qty: b.qty.trim(), disposition: b.disposition, values };
      }),
    });
  }

  const result = preview.result;
  const qualifyingColumns: DataTableColumn<CheckResult["qualifying"][number]>[] = [
    { key: "batchId", header: "Batch", mono: true },
    { key: "expiry", header: "Expiry", render: (r) => r.expiry ?? dash },
    { key: "qty", header: "Quantity", align: "end" },
  ];
  const excludedColumns: DataTableColumn<CheckResult["excluded"][number]>[] = [
    { key: "batchId", header: "Batch", mono: true },
    { key: "reason", header: "Reason" },
    { key: "warningCode", header: "Code", mono: true },
  ];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Customer batch requirements"
        description="Check which batches a customer line may use: remaining shelf life at shipment, attribute ranges and same-batch. A customer row beats a customer-group row, which beats the item default."
      />
      <form className={styles.stack} onSubmit={submit} noValidate>
        <Card title="Line">
          <div className={styles.form}>
            <FormField id="req-ship" label="Ship date" error={errors.shipDate}>
              {(c) => <Input {...c} type="date" value={shipDate} onChange={(e) => setShipDate(e.target.value)} invalid={Boolean(errors.shipDate)} />}
            </FormField>
            <FormField id="req-qty" label="Line quantity" error={errors.qty}>
              {(c) => <Input {...c} inputMode="decimal" value={qty} onChange={(e) => setQty(e.target.value)} invalid={Boolean(errors.qty)} />}
            </FormField>
            <FormField id="req-attribute" label="Attribute" hint="The attribute the ranges and batch values below refer to.">
              {(c) => <Input {...c} value={attribute} onChange={(e) => setAttribute(e.target.value)} />}
            </FormField>
          </div>
        </Card>
        <RowCard title="Requirements" addLabel="Add requirement" onAdd={reqs.add}>
          {reqs.rows.map((r, i) => (
            <fieldset key={i} className={styles.lineRow}>
              <legend className={styles.groupsLegend}>{`Requirement ${i + 1}`}</legend>
              <FormField id={`req-${i}-level`} label="Applies to">
                {(c) => (
                  <Select {...c} value={String(r.level)} onChange={(e) => reqs.update(i, { level: Number(e.target.value) as RequirementLevel })}>
                    {REQUIREMENT_LEVELS.map((l) => (
                      <option key={l} value={String(l)}>
                        {LEVEL_LABELS[l]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id={`req-${i}-days`} label="Sellable days" error={errors[`days-${i}`]}>
                {(c) => <Input {...c} inputMode="numeric" value={r.sellableDays} onChange={(e) => reqs.update(i, { sellableDays: e.target.value })} invalid={Boolean(errors[`days-${i}`])} />}
              </FormField>
              <FormField id={`req-${i}-min`} label="Minimum" error={errors[`min-${i}`]}>
                {(c) => <Input {...c} inputMode="decimal" value={r.min} onChange={(e) => reqs.update(i, { min: e.target.value })} invalid={Boolean(errors[`min-${i}`])} />}
              </FormField>
              <FormField id={`req-${i}-max`} label="Maximum" error={errors[`max-${i}`]}>
                {(c) => <Input {...c} inputMode="decimal" value={r.max} onChange={(e) => reqs.update(i, { max: e.target.value })} invalid={Boolean(errors[`max-${i}`])} />}
              </FormField>
              <div className={styles.lineChecks}>
                <span className={styles.check}>
                  <Input type="checkbox" id={`req-${i}-same`} checked={r.sameBatch} onChange={(e) => reqs.update(i, { sameBatch: e.target.checked })} />
                  <label htmlFor={`req-${i}-same`}>Same batch</label>
                </span>
                {reqs.rows.length > 1 ? (
                  <Button size="sm" variant="ghost" onClick={() => reqs.remove(i)}>
                    {`Remove requirement ${i + 1}`}
                  </Button>
                ) : null}
              </div>
            </fieldset>
          ))}
        </RowCard>
        <RowCard title="Candidate batches" addLabel="Add batch" onAdd={batches.add}>
          {batches.rows.map((b, i) => (
            <fieldset key={i} className={styles.lineRow}>
              <legend className={styles.groupsLegend}>{`Batch ${i + 1}`}</legend>
              <FormField id={`cand-${i}-id`} label="Batch" error={errors[`id-${i}`]}>
                {(c) => <Input {...c} value={b.id} onChange={(e) => batches.update(i, { id: e.target.value })} invalid={Boolean(errors[`id-${i}`])} />}
              </FormField>
              <FormField id={`cand-${i}-qty`} label="Quantity" error={errors[`bqty-${i}`]}>
                {(c) => <Input {...c} inputMode="decimal" value={b.qty} onChange={(e) => batches.update(i, { qty: e.target.value })} invalid={Boolean(errors[`bqty-${i}`])} />}
              </FormField>
              <FormField id={`cand-${i}-disposition`} label="Disposition">
                {(c) => (
                  <Select {...c} value={b.disposition} onChange={(e) => batches.update(i, { disposition: e.target.value as Disposition })}>
                    {DISPOSITIONS.map((d) => (
                      <option key={d} value={d}>
                        {d}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id={`cand-${i}-expiry`} label="Expiry">
                {(c) => <Input {...c} type="date" value={b.expiry} onChange={(e) => batches.update(i, { expiry: e.target.value })} />}
              </FormField>
              <FormField id={`cand-${i}-value`} label="Attribute value" error={errors[`value-${i}`]}>
                {(c) => <Input {...c} inputMode="decimal" value={b.value} onChange={(e) => batches.update(i, { value: e.target.value })} invalid={Boolean(errors[`value-${i}`])} />}
              </FormField>
              <RemoveRow label={`Remove batch ${i + 1}`} show={batches.rows.length > 1} onRemove={() => batches.remove(i)} />
            </fieldset>
          ))}
        </RowCard>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={preview.busy}>
            Check batches
          </Button>
        </div>
      </form>
      <Refusal title="No batch can supply the line" failure={preview.failure} />
      {result ? (
        <>
          <StatGroup label="Requirement check">
            <StatTile label="Sellable days" value={result.requirement.sellableDays} />
            <StatTile label="Qualifying quantity" value={result.qtyAvailable} />
            <StatTile label="Same batch" value={result.requirement.sameBatch ? (result.sameBatchId ?? dash) : "Not required"} />
          </StatGroup>
          <Card title="Qualifying batches">
            <DataTable
              label="Qualifying batches"
              columns={qualifyingColumns}
              rows={result.qualifying}
              getRowKey={(r) => r.batchId}
              empty={{ title: "No batch qualifies", description: "Every candidate misses the customer's requirements." }}
            />
          </Card>
          {result.excluded.length > 0 ? (
            <Card title="Excluded batches">
              <DataTable label="Excluded batches" columns={excludedColumns} rows={result.excluded} getRowKey={(r) => r.batchId} />
            </Card>
          ) : null}
        </>
      ) : null}
    </div>
  );
}

interface PackedRow {
  orderId: string;
  itemId: string;
  units: string;
  due: string;
}

/** FR-24.17, BR-24.12: bulk batch orders for packed orders, pegged by due date. */
export function ConsolidationPage({ consolidate = defaultConsolidate }: { consolidate?: (req: ConsolidateRequest) => Promise<ConsolidateResult> }) {
  // AC-24.13: 400 and 600 bottles at 0.5 kg, formula multiple 100 kg.
  const [relation, setRelation] = useState({ packQtyPerUnit: "0.5", multiple: "100", max: "" });
  const orders = useRows<PackedRow>(
    [
      { orderId: "PK-1", itemId: "BOTTLE-500G", units: "400", due: "2027-01-05" },
      { orderId: "PK-2", itemId: "BOTTLE-500G", units: "600", due: "2027-01-06" },
    ],
    () => ({ orderId: "", itemId: "", units: "", due: "" }),
  );
  const [errors, setErrors] = useState<Record<string, string>>({});
  const preview = usePreview(consolidate);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!QUANTITY.test(relation.packQtyPerUnit.trim())) found.pack = "Enter a quantity with at most 6 decimals.";
    if (!QUANTITY.test(relation.multiple.trim())) found.multiple = "Enter a quantity with at most 6 decimals.";
    if (relation.max.trim() !== "" && !QUANTITY.test(relation.max.trim())) found.max = "Enter a quantity, or leave empty for no maximum.";
    orders.rows.forEach((o, i) => {
      if (!o.orderId.trim()) found[`order-${i}`] = "Enter the packing order.";
      if (!QUANTITY.test(o.units.trim())) found[`units-${i}`] = "Enter the packed units.";
      if (!DATE.test(o.due)) found[`due-${i}`] = "Enter the due date.";
    });
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void preview.run({
      relation: { packQtyPerUnit: relation.packQtyPerUnit.trim(), multiple: relation.multiple.trim(), max: relation.max.trim() || "0" },
      packedOrders: orders.rows.map((o) => ({ orderId: o.orderId.trim(), itemId: o.itemId.trim(), units: o.units.trim(), due: o.due })),
    });
  }

  const result = preview.result;
  const pegRows = result ? result.bulkOrders.flatMap((b, i) => b.pegs.map((p) => ({ ...p, bulk: `Bulk ${i + 1} (${b.qty})` }))) : [];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Bulk consolidation"
        description="Size the bulk batch orders for packed orders: the bulk need rounds up to the formula multiple and splits at the formula maximum; the earliest due packing orders are pegged first."
      />
      <form className={styles.stack} onSubmit={submit} noValidate>
        <Card title="Bulk and pack relation">
          <div className={styles.form}>
            <FormField id="cons-pack" label="Bulk per packed unit" error={errors.pack}>
              {(c) => <Input {...c} inputMode="decimal" value={relation.packQtyPerUnit} onChange={(e) => setRelation({ ...relation, packQtyPerUnit: e.target.value })} invalid={Boolean(errors.pack)} />}
            </FormField>
            <FormField id="cons-multiple" label="Formula multiple" error={errors.multiple}>
              {(c) => <Input {...c} inputMode="decimal" value={relation.multiple} onChange={(e) => setRelation({ ...relation, multiple: e.target.value })} invalid={Boolean(errors.multiple)} />}
            </FormField>
            <FormField id="cons-max" label="Formula maximum" optional hint="Empty for no maximum." error={errors.max}>
              {(c) => <Input {...c} inputMode="decimal" value={relation.max} onChange={(e) => setRelation({ ...relation, max: e.target.value })} invalid={Boolean(errors.max)} />}
            </FormField>
          </div>
        </Card>
        <RowCard title="Packed orders" addLabel="Add packed order" onAdd={orders.add}>
          {orders.rows.map((o, i) => (
            <fieldset key={i} className={styles.lineRow}>
              <legend className={styles.groupsLegend}>{`Packed order ${i + 1}`}</legend>
              <FormField id={`pk-${i}-order`} label="Packing order" error={errors[`order-${i}`]}>
                {(c) => <Input {...c} value={o.orderId} onChange={(e) => orders.update(i, { orderId: e.target.value })} invalid={Boolean(errors[`order-${i}`])} />}
              </FormField>
              <FormField id={`pk-${i}-item`} label="Packed item">
                {(c) => <Input {...c} value={o.itemId} onChange={(e) => orders.update(i, { itemId: e.target.value })} />}
              </FormField>
              <FormField id={`pk-${i}-units`} label="Units" error={errors[`units-${i}`]}>
                {(c) => <Input {...c} inputMode="decimal" value={o.units} onChange={(e) => orders.update(i, { units: e.target.value })} invalid={Boolean(errors[`units-${i}`])} />}
              </FormField>
              <FormField id={`pk-${i}-due`} label="Due" error={errors[`due-${i}`]}>
                {(c) => <Input {...c} type="date" value={o.due} onChange={(e) => orders.update(i, { due: e.target.value })} invalid={Boolean(errors[`due-${i}`])} />}
              </FormField>
              <RemoveRow label={`Remove packed order ${i + 1}`} show={orders.rows.length > 1} onRemove={() => orders.remove(i)} />
            </fieldset>
          ))}
        </RowCard>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={preview.busy}>
            Preview consolidation
          </Button>
        </div>
      </form>
      <Refusal title="Consolidation refused" failure={preview.failure} />
      {result ? (
        <Card title="Bulk batch orders">
          <div className={styles.stack}>
            <StatGroup label="Consolidation">
              <StatTile label="Bulk orders" value={result.bulkOrders.length} />
              <StatTile label="Bulk sizes" value={result.bulkOrders.map((b) => b.qty).join(" + ")} />
            </StatGroup>
            <DataTable
              label="Pegged packing orders"
              columns={[
                { key: "bulk", header: "Bulk order" },
                { key: "orderId", header: "Packing order", mono: true },
                { key: "itemId", header: "Packed item" },
                { key: "bulkQty", header: "Bulk quantity", align: "end" },
              ]}
              rows={pegRows}
              getRowKey={(r, i) => `${r.bulk}-${r.orderId}-${i}`}
            />
          </div>
        </Card>
      ) : null}
    </div>
  );
}

interface ExceptionRow {
  id: string;
  disposition: ExceptionDisposition;
  signatureId: string;
}

const EXCEPTION_LABELS: Record<ExceptionDisposition, string> = {
  "": "Not dispositioned",
  accepted: "Accepted",
  "deviation-raised": "Deviation raised",
  rejected: "Rejected",
};

/** BR-24.15: QA approval of a batch record needs every exception dispositioned and signed. */
export function ApprovalCheckPage({ check = defaultCheckApproval }: { check?: (req: ApproveCheckRequest) => Promise<ApproveCheckResult> }) {
  // AC-24.16: one expired-batch override that is not yet dispositioned.
  const exceptions = useRows<ExceptionRow>([{ id: "EXC-expired-batch", disposition: "", signatureId: "" }], () => ({ id: "", disposition: "", signatureId: "" }));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const preview = usePreview(check);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    exceptions.rows.forEach((e, i) => {
      if (!e.id.trim()) found[`id-${i}`] = "Enter the exception.";
    });
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void preview.run({ exceptions: exceptions.rows.map((e) => ({ id: e.id.trim(), disposition: e.disposition, signatureId: e.signatureId.trim() })) });
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Batch record approval check"
        description="Review by exception: QA can approve a batch record only when every exception has a disposition and a signature."
      />
      <form className={styles.stack} onSubmit={submit} noValidate>
        <RowCard title="Exceptions" addLabel="Add exception" onAdd={exceptions.add}>
          {exceptions.rows.map((e, i) => (
            <fieldset key={i} className={styles.lineRow}>
              <legend className={styles.groupsLegend}>{`Exception ${i + 1}`}</legend>
              <FormField id={`exc-${i}-id`} label="Exception" error={errors[`id-${i}`]}>
                {(c) => <Input {...c} value={e.id} onChange={(ev) => exceptions.update(i, { id: ev.target.value })} invalid={Boolean(errors[`id-${i}`])} />}
              </FormField>
              <FormField id={`exc-${i}-disposition`} label="Disposition">
                {(c) => (
                  <Select {...c} value={e.disposition} onChange={(ev) => exceptions.update(i, { disposition: ev.target.value as ExceptionDisposition })}>
                    {EXCEPTION_DISPOSITIONS.map((d) => (
                      <option key={d} value={d}>
                        {EXCEPTION_LABELS[d]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id={`exc-${i}-signature`} label="Signature">
                {(c) => <Input {...c} value={e.signatureId} onChange={(ev) => exceptions.update(i, { signatureId: ev.target.value })} />}
              </FormField>
              <RemoveRow label={`Remove exception ${i + 1}`} show={exceptions.rows.length > 1} onRemove={() => exceptions.remove(i)} />
            </fieldset>
          ))}
        </RowCard>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={preview.busy}>
            Check approval
          </Button>
        </div>
      </form>
      <Refusal title="Approval refused" failure={preview.failure} />
      {preview.result ? (
        <Alert tone="success" title="The batch record can be approved" live="status">
          {`All ${preview.result.exceptions} exceptions are dispositioned and signed.`}
        </Alert>
      ) : null}
    </div>
  );
}
