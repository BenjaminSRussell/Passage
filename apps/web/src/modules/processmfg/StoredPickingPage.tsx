import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  listBatches as defaultListBatches,
  proposeStoredPicking as defaultPropose,
  type Batch,
  type StoredPickingRequest,
  type StoredPickingResult,
} from "./api/client";
import { DATE, NUMBER, Refusal, usePreview } from "./BatchRulePages";
import { MIN_OVERRIDE_REASON } from "./BatchQualityPages";
import { DispositionBadge } from "./BatchRecordPages";
import { dash, QUANTITY, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const EXCLUSION_LABELS: Record<string, string> = {
  "PMF-0003": "Expired",
  "PMF-0005": "Potency outside the band",
  "PMF-0016": "Blocked by disposition",
};

interface Props {
  propose?: (req: StoredPickingRequest) => Promise<StoredPickingResult>;
  list?: (filter: { itemId?: string }, signal?: AbortSignal) => Promise<Batch[]>;
}

/**
 * FR-24.3, FR-24.5, FR-24.6, BR-24.10: the batches a line consumes, taken from
 * the stored batch register. The disposition, expiry and potency of each batch
 * come from its quality record; only the on-hand quantity is typed, because
 * the register keeps none.
 */
export function StoredPickingPage({ propose = defaultPropose, list = defaultListBatches }: Props) {
  const [itemId, setItemId] = useState("");
  const [postingDate, setPostingDate] = useState(new Date().toISOString().slice(0, 10));
  const [qty, setQty] = useState("");
  const [bandMin, setBandMin] = useState("");
  const [bandMax, setBandMax] = useState("");
  const [reason, setReason] = useState("");
  const [onHand, setOnHand] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const preview = usePreview(propose);

  const item = itemId.trim();
  const itemValid = UUID.test(item);
  const batches = useLoad<Batch[]>((signal) => (itemValid ? list({ itemId: item }, signal) : Promise.resolve([])), `stored-pick|${itemValid ? item : ""}`);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!itemValid) found.itemId = "Enter the item id as a UUID.";
    if (!DATE.test(postingDate)) found.postingDate = "Enter the posting date.";
    if (!QUANTITY.test(qty.trim())) found.qty = "Enter a quantity with at most 6 decimals.";
    const banded = bandMin.trim() !== "" || bandMax.trim() !== "";
    if (banded) {
      if (!NUMBER.test(bandMin.trim())) found.bandMin = "Enter the band minimum.";
      if (!NUMBER.test(bandMax.trim())) found.bandMax = "Enter the band maximum.";
    }
    const why = reason.trim();
    if (why !== "" && [...why].length < MIN_OVERRIDE_REASON) found.reason = `Give at least ${MIN_OVERRIDE_REASON} characters, or leave it empty.`;
    const lines = (batches.data ?? []).filter((b) => (onHand[b.id] ?? "").trim() !== "");
    for (const b of lines) {
      if (!QUANTITY.test(onHand[b.id].trim()) && onHand[b.id].trim() !== "0") found[`qty-${b.id}`] = "Enter a quantity.";
    }
    if (itemValid && lines.length === 0) found.onHand = "Enter the on-hand quantity of at least one batch.";
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void preview.run({
      itemId: item,
      postingDate,
      qty: qty.trim(),
      ...(banded ? { potency: { min: bandMin.trim(), max: bandMax.trim() } } : {}),
      ...(why ? { overrideReason: why } : {}),
      onHand: lines.map((b) => ({ batchId: b.id, qty: onHand[b.id].trim() })),
    });
  }

  const result = preview.result;
  const numberOf = (id: string) => result?.batchNumbers[id] ?? id;
  const overridden = result?.picked.filter((p) => p.expiredOverride).length ?? 0;
  const pickedColumns: DataTableColumn<StoredPickingResult["picked"][number]>[] = [
    { key: "batchId", header: "Batch", mono: true, render: (r) => numberOf(r.batchId) },
    { key: "expiry", header: "Expiry", render: (r) => r.expiry ?? dash },
    { key: "qty", header: "Quantity taken", align: "end" },
    { key: "expiredOverride", header: "Override", render: (r) => (r.expiredOverride ? "Expired, overridden" : dash) },
  ];
  const excludedColumns: DataTableColumn<StoredPickingResult["excluded"][number]>[] = [
    { key: "batchId", header: "Batch", mono: true, render: (r) => numberOf(r.batchId) },
    { key: "code", header: "Rule", render: (r) => EXCLUSION_LABELS[r.code] ?? r.code },
    { key: "detail", header: "Reason" },
  ];
  const batchColumns: DataTableColumn<Batch>[] = [
    { key: "batchNumber", header: "Batch", mono: true },
    { key: "disposition", header: "Disposition", render: (b) => <DispositionBadge disposition={b.disposition} /> },
    { key: "expiryDate", header: "Expiry", render: (b) => b.expiryDate ?? dash },
    {
      key: "id",
      header: "On hand",
      render: (b) => (
        <Input
          id={`onhand-${b.id}`}
          aria-label={`On hand of batch ${b.batchNumber}`}
          inputMode="decimal"
          value={onHand[b.id] ?? ""}
          onChange={(e) => setOnHand({ ...onHand, [b.id]: e.target.value })}
          invalid={Boolean(errors[`qty-${b.id}`])}
        />
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Pick from stored batches"
        description="Propose the batches a line consumes from the batch register: only available batches, the one expiring first first, and potency inside the band. Disposition, expiry and potency are read from each batch's quality record."
      />
      <form className={styles.stack} onSubmit={submit} noValidate>
        <Card title="Line">
          <div className={styles.form}>
            <FormField id="sp-item" label="Item id" hint="The batches of this item are listed below." error={errors.itemId}>
              {(c) => <Input {...c} value={itemId} onChange={(e) => setItemId(e.target.value)} invalid={Boolean(errors.itemId)} />}
            </FormField>
            <FormField id="sp-posting" label="Posting date" error={errors.postingDate}>
              {(c) => <Input {...c} type="date" value={postingDate} onChange={(e) => setPostingDate(e.target.value)} invalid={Boolean(errors.postingDate)} />}
            </FormField>
            <FormField id="sp-qty" label="Line quantity" error={errors.qty}>
              {(c) => <Input {...c} inputMode="decimal" value={qty} onChange={(e) => setQty(e.target.value)} invalid={Boolean(errors.qty)} />}
            </FormField>
            <FormField id="sp-min" label="Potency minimum %" hint="Leave both empty for no band. Uses the item's potency attribute." error={errors.bandMin}>
              {(c) => <Input {...c} inputMode="decimal" value={bandMin} onChange={(e) => setBandMin(e.target.value)} invalid={Boolean(errors.bandMin)} />}
            </FormField>
            <FormField id="sp-max" label="Potency maximum %" error={errors.bandMax}>
              {(c) => <Input {...c} inputMode="decimal" value={bandMax} onChange={(e) => setBandMax(e.target.value)} invalid={Boolean(errors.bandMax)} />}
            </FormField>
            <FormField id="sp-reason" label="Reason to consume expired batches" hint="Needs the Consume expired batch duty; at least 10 characters." error={errors.reason}>
              {(c) => <Input {...c} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(errors.reason)} />}
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
                columns={batchColumns}
                rows={batches.data ?? []}
                getRowKey={(b) => b.id}
                loading={batches.loading}
                empty={{ title: "No batches registered for this item", description: "Register a batch and record its attributes first." }}
              />
            )}
            {errors.onHand ? <Alert tone="danger" title="Nothing to pick from">{errors.onHand}</Alert> : null}
          </Card>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={preview.busy}>
            Propose batches
          </Button>
        </div>
      </form>
      <Refusal title="Proposal refused" failure={preview.failure} />
      {result ? (
        <>
          <StatGroup label="Picking proposal">
            <StatTile label="Batches picked" value={result.picked.length} />
            <StatTile label="Excluded" value={result.excluded.length} tone="warning" />
            <StatTile label="Shortfall" value={result.shortfall} tone="danger" />
          </StatGroup>
          {reason.trim() && !result.overrideHeld ? (
            <Alert tone="warning" title="Override not applied">
              You do not hold the Consume expired batch duty, so expired batches stay excluded.
            </Alert>
          ) : null}
          {overridden > 0 ? (
            <Alert tone="warning" title="Expired batches overridden">
              {`${overridden} expired batch${overridden === 1 ? " is" : "es are"} used on your override; each becomes an exception in the batch record review.`}
            </Alert>
          ) : null}
          <Card title="Picked batches">
            <DataTable
              label="Picked batches"
              columns={pickedColumns}
              rows={result.picked}
              getRowKey={(r) => r.batchId}
              empty={{ title: "No batch can be picked", description: "Every batch is blocked, expired or outside the potency band." }}
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
