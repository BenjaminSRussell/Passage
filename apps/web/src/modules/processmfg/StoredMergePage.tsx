import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import {
  describeProblem,
  listBatches as defaultListBatches,
  mergeStoredBatches as defaultMerge,
  newKey,
  type Batch,
  type GenealogyLink,
  type Problem,
  type StoredMergeRequest,
  type StoredMergeResult,
} from "./api/client";
import { DispositionBadge } from "./BatchRecordPages";
import { dash, QUANTITY, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** Duty that merges batches; enforced by the service (FR-24.16). */
export const MERGE_DUTY = "production.batch.merge";

interface Props {
  merge?: (req: StoredMergeRequest, key: string) => Promise<StoredMergeResult>;
  list?: (filter: { itemId?: string }, signal?: AbortSignal) => Promise<Batch[]>;
}

interface Line {
  qty: string;
  unitCost: string;
}

const COST = /^\d{1,12}(\.\d{1,6})?$/;

const parentColumns: DataTableColumn<GenealogyLink>[] = [
  { key: "batchNumber", header: "Parent batch", mono: true, render: (l) => <Link to={`/processmfg/batches/${l.batchId}`}>{l.batchNumber}</Link> },
  { key: "qty", header: "Quantity", align: "end" },
  { key: "unitCost", header: "Cost per unit", align: "end" },
];

/**
 * FR-24.16, AC-24.12: merges stored batches of one item into a new batch. The
 * disposition and attribute values of every source come from its quality
 * record; the quantity and cost per unit are typed, because the register keeps
 * neither. The target gets the quantity-weighted attributes, the earliest
 * expiry and its genealogy.
 */
export function StoredMergePage({ merge = (req, key) => defaultMerge(req, key), list = defaultListBatches }: Props) {
  const allowed = holdsDuty(usePrincipalDuties(), MERGE_DUTY);
  const [itemId, setItemId] = useState("");
  const [target, setTarget] = useState("");
  const [warehouse, setWarehouse] = useState("");
  const [cwUnit, setCwUnit] = useState("");
  const [override, setOverride] = useState("");
  const [picked, setPicked] = useState<Record<string, Line>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<StoredMergeResult | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  const item = itemId.trim();
  const itemValid = UUID.test(item);
  const batches = useLoad<Batch[]>((signal) => (itemValid ? list({ itemId: item }, signal) : Promise.resolve([])), `stored-merge|${itemValid ? item : ""}`);

  function touch() {
    key.current = newKey();
  }
  function setLine(id: string, patch: Partial<Line> | null) {
    touch();
    setPicked((prev) => {
      const next = { ...prev };
      if (patch === null) delete next[id];
      else next[id] = { ...(prev[id] ?? { qty: "", unitCost: "" }), ...patch };
      return next;
    });
  }

  if (!allowed) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Merge stored batches" />
        <Card title="Merge batches">
          <p className={styles.note}>Merging batches needs the batch merge duty. Ask your administrator if you should have it.</p>
        </Card>
      </div>
    );
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!itemValid) found.itemId = "Enter the item id as a UUID.";
    const number = target.trim();
    if (number === "" || number.length > 40) found.target = "Enter a batch number of 1 to 40 characters.";
    const wh = warehouse.trim();
    if (wh === "" || wh.length > 80) found.warehouse = "Enter the warehouse of the batches.";
    const chosen = (batches.data ?? []).filter((b) => picked[b.id]);
    if (itemValid && chosen.length < 2) found.sources = "Select at least two batches to merge.";
    for (const b of chosen) {
      const line = picked[b.id];
      if (!QUANTITY.test(line.qty.trim()) || Number(line.qty) === 0) found[`qty-${b.id}`] = "Enter a quantity above zero.";
      if (!COST.test(line.unitCost.trim())) found[`cost-${b.id}`] = "Enter the cost per unit.";
    }
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const names = override.split(",").map((n) => n.trim()).filter(Boolean);
    setBusy(true);
    try {
      const out = await merge(
        {
          targetBatchNumber: number,
          sources: chosen.map((b) => ({
            batchId: b.id,
            version: b.version,
            warehouseId: wh,
            ...(cwUnit.trim() ? { cwUnit: cwUnit.trim() } : {}),
            qty: picked[b.id].qty.trim(),
            unitCost: picked[b.id].unitCost.trim(),
          })),
          ...(names.length > 0 ? { mergeOverride: names } : {}),
        },
        key.current,
      );
      key.current = newKey();
      setResult(out);
      setPicked({});
      setTarget("");
      batches.reload();
    } catch (e) {
      const problem = describeProblem(e);
      setFailure(problem);
      setResult(null);
      // A refused command can be corrected and retried under a new key; an answer lost in transit is retried under the same one.
      if (problem.code !== "COM-0500") touch();
    } finally {
      setBusy(false);
    }
  }

  const columns: DataTableColumn<Batch>[] = [
    {
      key: "id",
      header: "Merge",
      render: (b) => (
        <input
          type="checkbox"
          aria-label={`Merge batch ${b.batchNumber}`}
          checked={Boolean(picked[b.id])}
          onChange={(e) => setLine(b.id, e.target.checked ? {} : null)}
        />
      ),
    },
    { key: "batchNumber", header: "Batch", mono: true },
    { key: "disposition", header: "Disposition", render: (b) => <DispositionBadge disposition={b.disposition} /> },
    { key: "expiryDate", header: "Expiry", render: (b) => b.expiryDate ?? dash },
    {
      key: "version",
      header: "Quantity",
      render: (b) =>
        picked[b.id] ? (
          <Input
            aria-label={`Quantity of batch ${b.batchNumber}`}
            inputMode="decimal"
            value={picked[b.id].qty}
            onChange={(e) => setLine(b.id, { qty: e.target.value })}
            invalid={Boolean(errors[`qty-${b.id}`])}
          />
        ) : (
          dash
        ),
    },
    {
      key: "createdBy",
      header: "Cost per unit",
      render: (b) =>
        picked[b.id] ? (
          <Input
            aria-label={`Cost per unit of batch ${b.batchNumber}`}
            inputMode="decimal"
            value={picked[b.id].unitCost}
            onChange={(e) => setLine(b.id, { unitCost: e.target.value })}
            invalid={Boolean(errors[`cost-${b.id}`])}
          />
        ) : (
          dash
        ),
    },
  ];
  const lineErrors = Object.entries(errors).filter(([k]) => k.startsWith("qty-") || k.startsWith("cost-"));

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Merge stored batches"
        description="Merge batches of one item into a new batch. Numeric attributes become the quantity-weighted average, expiry the earliest date, and the new batch lists the sources as its parents. Disposition and attributes are read from each batch's quality record; the list reloads after a merge because each source moves to a new version."
      />
      <form className={styles.stack} onSubmit={submit} noValidate>
        <Card title="Merge">
          <div className={styles.form}>
            <FormField id="sm-item" label="Item" hint="The batches of this item are listed below." error={errors.itemId}>
              {(c) => <EntityLookup entity="product" {...c} value={itemId} onChange={(e) => { setItemId(e.target.value); touch(); }} invalid={Boolean(errors.itemId)} />}
            </FormField>
            <FormField id="sm-target" label="New batch number" error={errors.target}>
              {(c) => <Input {...c} value={target} onChange={(e) => { setTarget(e.target.value); touch(); }} invalid={Boolean(errors.target)} />}
            </FormField>
            <FormField id="sm-warehouse" label="Warehouse" hint="All merged batches must be in the same warehouse." error={errors.warehouse}>
              {(c) => <Input {...c} value={warehouse} onChange={(e) => { setWarehouse(e.target.value); touch(); }} invalid={Boolean(errors.warehouse)} />}
            </FormField>
            <FormField id="sm-cw" label="Catch-weight unit" hint="Leave empty for items without catch weight.">
              {(c) => <Input {...c} value={cwUnit} onChange={(e) => { setCwUnit(e.target.value); touch(); }} />}
            </FormField>
            <FormField id="sm-override" label="Text attributes allowed to differ" hint="Names separated by commas; the largest batch's text wins.">
              {(c) => <Input {...c} value={override} onChange={(e) => { setOverride(e.target.value); touch(); }} />}
            </FormField>
          </div>
        </Card>
        {itemValid ? (
          <Card title="Batches of the item">
            {batches.error ? (
              <Alert tone="danger" title="Could not load batches" live="alert" action={<Button size="sm" onClick={batches.reload}>Try again</Button>}>
                <ProblemMessage problem={batches.error} />
              </Alert>
            ) : (
              <DataTable
                label="Batches of the item"
                columns={columns}
                rows={batches.data ?? []}
                getRowKey={(b) => b.id}
                loading={batches.loading}
                empty={{ title: "No batches registered for this item", description: "Register the batches and record their attributes first." }}
              />
            )}
            {errors.sources ? <Alert tone="danger" title="Nothing to merge">{errors.sources}</Alert> : null}
            {lineErrors.length > 0 ? <Alert tone="danger" title="Check the selected batches">{lineErrors.map(([, m]) => m).filter((m, i, all) => all.indexOf(m) === i).join(" ")}</Alert> : null}
          </Card>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Merge batches
          </Button>
        </div>
      </form>
      {failure ? (
        <Alert
          tone="danger"
          title="Merge refused"
          live="alert"
          action={failure.code === "PMF-0104" ? <Button size="sm" onClick={batches.reload}>Reload batches</Button> : undefined}
        >
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {result ? (
        <>
          <Alert tone="success" title={`Created batch ${result.batch.batchNumber}`} live="status">
            <Link to={`/processmfg/batches/${result.batch.id}`}>Open the new batch</Link>
          </Alert>
          <StatGroup label="Merged batch">
            <StatTile label="Quantity" value={result.qty} />
            <StatTile label="Cost per unit" value={result.unitCost} />
            <StatTile label="Disposition" value={result.batch.disposition} />
            <StatTile label="Expiry" value={result.batch.expiryDate ?? dash} />
          </StatGroup>
          <Card title="Parents">
            <DataTable label="Parent batches" columns={parentColumns} rows={result.parents} getRowKey={(l) => l.batchId} />
          </Card>
        </>
      ) : null}
    </div>
  );
}
