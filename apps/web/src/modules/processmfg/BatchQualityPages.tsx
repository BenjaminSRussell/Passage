import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  ATTRIBUTE_KINDS,
  checkAttributes as defaultCheckAttributes,
  DISPOSITIONS,
  previewRework as defaultPreviewRework,
  proposePicking as defaultProposePicking,
  REWORK_OUTPUT_DISPOSITIONS,
  type AttributeKind,
  type AttributesRequest,
  type AttributesResult,
  type AttributeValue,
  type Disposition,
  type PickingRequest,
  type PickingResult,
  type ReworkOutputDisposition,
  type ReworkRequest,
  type ReworkResult,
} from "./api/client";
import { DATE, NUMBER, Refusal, RemoveRow, RowCard, usePreview, useRows } from "./BatchRulePages";
import { dash, QUANTITY } from "./format";
import styles from "./pages.module.css";

/** Shortest audit reason for consuming an expired batch (BR-24.10), in characters. */
export const MIN_OVERRIDE_REASON = 10;

const KIND_LABELS: Record<AttributeKind, string> = { numeric: "Numeric", text: "Text", enum: "Enum", date: "Date" };

interface AttributeRow {
  name: string;
  kind: AttributeKind;
  min: string;
  max: string;
  options: string;
  value: string;
}

const blankAttribute = (): AttributeRow => ({ name: "", kind: "numeric", min: "", max: "", options: "", value: "" });

/** The recorded value as the service reads it for the attribute's kind; "" is not recorded. */
function recorded(row: AttributeRow): AttributeValue | undefined {
  const v = row.value.trim();
  if (v === "") return undefined;
  if (row.kind === "numeric") return { num: v };
  if (row.kind === "date") return { date: v };
  return { text: v };
}

/** FR-24.5: check recorded batch attribute values against the item's definitions. */
export function BatchAttributesPage({ check = defaultCheckAttributes }: { check?: (req: AttributesRequest) => Promise<AttributesResult> }) {
  const attributes = useRows<AttributeRow>(
    [
      { name: "potency", kind: "numeric", min: "40", max: "60", options: "", value: "38.5" },
      { name: "grade", kind: "enum", min: "", max: "", options: "A, B", value: "A" },
    ],
    blankAttribute,
  );
  const [errors, setErrors] = useState<Record<string, string>>({});
  const preview = usePreview(check);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    const names = new Set<string>();
    attributes.rows.forEach((a, i) => {
      const name = a.name.trim();
      if (!name) found[`name-${i}`] = "Name the attribute.";
      else if (names.has(name)) found[`name-${i}`] = "This attribute is already listed.";
      names.add(name);
      if (a.kind === "numeric") {
        if (a.min.trim() !== "" && !NUMBER.test(a.min.trim())) found[`min-${i}`] = "Enter a number.";
        if (a.max.trim() !== "" && !NUMBER.test(a.max.trim())) found[`max-${i}`] = "Enter a number.";
        if (a.value.trim() !== "" && !NUMBER.test(a.value.trim())) found[`value-${i}`] = "Enter a number with at most 6 decimals.";
      }
      if (a.kind === "date" && a.value.trim() !== "" && !DATE.test(a.value.trim())) found[`value-${i}`] = "Enter a date as yyyy-mm-dd.";
      if (a.kind === "enum" && a.options.trim() === "") found[`options-${i}`] = "List the allowed values, separated by commas.";
    });
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    const values: Record<string, AttributeValue> = {};
    for (const a of attributes.rows) {
      const v = recorded(a);
      if (v) values[a.name.trim()] = v;
    }
    void preview.run({
      definitions: attributes.rows.map((a) => ({
        name: a.name.trim(),
        kind: a.kind,
        ...(a.kind === "numeric" && a.min.trim() ? { min: a.min.trim() } : {}),
        ...(a.kind === "numeric" && a.max.trim() ? { max: a.max.trim() } : {}),
        ...(a.kind === "enum"
          ? {
              options: a.options
                .split(",")
                .map((o) => o.trim())
                .filter(Boolean),
            }
          : {}),
      })),
      values,
    });
  }

  const result = preview.result;
  const columns: DataTableColumn<AttributesResult["results"][number]>[] = [
    { key: "attribute", header: "Attribute", mono: true },
    { key: "pass", header: "Result", render: (r) => (r.pass ? <Badge tone="success">Within limits</Badge> : <Badge tone="warning">Outside limits</Badge>) },
    { key: "detail", header: "Detail", render: (r) => r.detail ?? dash },
  ];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Batch attributes"
        description="Check the values recorded on a batch against the item's attribute definitions. A number outside its limits is still recorded, and becomes an exception the batch record review must disposition."
      />
      <form className={styles.stack} onSubmit={submit} noValidate>
        <RowCard title="Attributes" addLabel="Add attribute" onAdd={attributes.add}>
          {attributes.rows.map((a, i) => (
            <fieldset key={i} className={styles.lineRow}>
              <legend className={styles.groupsLegend}>{`Attribute ${i + 1}`}</legend>
              <FormField id={`attr-${i}-name`} label="Name" error={errors[`name-${i}`]}>
                {(c) => <Input {...c} value={a.name} onChange={(e) => attributes.update(i, { name: e.target.value })} invalid={Boolean(errors[`name-${i}`])} />}
              </FormField>
              <FormField id={`attr-${i}-kind`} label="Kind">
                {(c) => (
                  <Select {...c} value={a.kind} onChange={(e) => attributes.update(i, { kind: e.target.value as AttributeKind })}>
                    {ATTRIBUTE_KINDS.map((k) => (
                      <option key={k} value={k}>
                        {KIND_LABELS[k]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              {a.kind === "numeric" ? (
                <>
                  <FormField id={`attr-${i}-min`} label="Minimum" error={errors[`min-${i}`]}>
                    {(c) => <Input {...c} inputMode="decimal" value={a.min} onChange={(e) => attributes.update(i, { min: e.target.value })} invalid={Boolean(errors[`min-${i}`])} />}
                  </FormField>
                  <FormField id={`attr-${i}-max`} label="Maximum" error={errors[`max-${i}`]}>
                    {(c) => <Input {...c} inputMode="decimal" value={a.max} onChange={(e) => attributes.update(i, { max: e.target.value })} invalid={Boolean(errors[`max-${i}`])} />}
                  </FormField>
                </>
              ) : null}
              {a.kind === "enum" ? (
                <FormField id={`attr-${i}-options`} label="Options" hint="Separated by commas." error={errors[`options-${i}`]}>
                  {(c) => <Input {...c} value={a.options} onChange={(e) => attributes.update(i, { options: e.target.value })} invalid={Boolean(errors[`options-${i}`])} />}
                </FormField>
              ) : null}
              <FormField id={`attr-${i}-value`} label="Recorded value" hint="Empty if not recorded." error={errors[`value-${i}`]}>
                {(c) => (
                  <Input
                    {...c}
                    type={a.kind === "date" ? "date" : "text"}
                    inputMode={a.kind === "numeric" ? "decimal" : undefined}
                    value={a.value}
                    onChange={(e) => attributes.update(i, { value: e.target.value })}
                    invalid={Boolean(errors[`value-${i}`])}
                  />
                )}
              </FormField>
              <RemoveRow label={`Remove attribute ${i + 1}`} show={attributes.rows.length > 1} onRemove={() => attributes.remove(i)} />
            </fieldset>
          ))}
        </RowCard>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={preview.busy}>
            Check values
          </Button>
        </div>
      </form>
      <Refusal title="Values refused" failure={preview.failure} />
      {result ? (
        <>
          <StatGroup label="Attribute check">
            <StatTile label="Values checked" value={result.results.length} />
            <StatTile label="Exceptions" value={result.exceptions} tone="warning" />
          </StatGroup>
          <Card title="Results">
            <DataTable
              label="Attribute results"
              columns={columns}
              rows={result.results}
              getRowKey={(r) => r.attribute}
              empty={{ title: "No values recorded", description: "Enter a recorded value for at least one attribute." }}
            />
          </Card>
        </>
      ) : null}
    </div>
  );
}

interface PickRow {
  id: string;
  qty: string;
  disposition: Disposition;
  expiry: string;
  potency: string;
}

const blankPick = (): PickRow => ({ id: "", qty: "", disposition: "available", expiry: "", potency: "" });

const EXCLUSION_LABELS: Record<string, string> = {
  "PMF-0003": "Expired",
  "PMF-0005": "Potency outside the band",
  "PMF-0016": "Blocked by disposition",
};

/** FR-24.3, FR-24.5, FR-24.6, BR-24.10: the batches a line consumes. */
export function PickingPage({ propose = defaultProposePicking }: { propose?: (req: PickingRequest) => Promise<PickingResult> }) {
  // AC-24.4 and AC-24.10: an expired batch and an on-hold batch next to usable ones.
  const [postingDate, setPostingDate] = useState("2027-01-15");
  const [qty, setQty] = useState("80");
  const [bandMin, setBandMin] = useState("40");
  const [bandMax, setBandMax] = useState("60");
  const [reason, setReason] = useState("");
  const batches = useRows<PickRow>(
    [
      { id: "B-401", qty: "50", disposition: "available", expiry: "2027-01-10", potency: "45" },
      { id: "B-402", qty: "100", disposition: "on-hold", expiry: "2027-02-01", potency: "45" },
      { id: "B-403", qty: "100", disposition: "available", expiry: "2027-06-01", potency: "50" },
    ],
    blankPick,
  );
  const [errors, setErrors] = useState<Record<string, string>>({});
  const preview = usePreview(propose);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!DATE.test(postingDate)) found.postingDate = "Enter the posting date.";
    if (!QUANTITY.test(qty.trim())) found.qty = "Enter a quantity with at most 6 decimals.";
    const banded = bandMin.trim() !== "" || bandMax.trim() !== "";
    if (banded) {
      if (!NUMBER.test(bandMin.trim())) found.bandMin = "Enter the band minimum.";
      if (!NUMBER.test(bandMax.trim())) found.bandMax = "Enter the band maximum.";
    }
    const why = reason.trim();
    if (why !== "" && [...why].length < MIN_OVERRIDE_REASON) found.reason = `Give at least ${MIN_OVERRIDE_REASON} characters, or leave it empty.`;
    batches.rows.forEach((b, i) => {
      if (!b.id.trim()) found[`id-${i}`] = "Enter the batch.";
      if (!QUANTITY.test(b.qty.trim())) found[`qty-${i}`] = "Enter a quantity.";
      if (b.expiry !== "" && !DATE.test(b.expiry)) found[`expiry-${i}`] = "Enter a date.";
      if (b.potency.trim() !== "" && !NUMBER.test(b.potency.trim())) found[`potency-${i}`] = "Enter a number.";
    });
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void preview.run({
      postingDate,
      qty: qty.trim(),
      ...(banded ? { potency: { attribute: "potency", min: bandMin.trim(), max: bandMax.trim() } } : {}),
      ...(why ? { overrideReason: why } : {}),
      batches: batches.rows.map((b) => {
        const values: Record<string, AttributeValue> = {};
        if (b.expiry) values.expiry = { date: b.expiry };
        if (b.potency.trim()) values.potency = { num: b.potency.trim() };
        return { id: b.id.trim(), qty: b.qty.trim(), disposition: b.disposition, values };
      }),
    });
  }

  const result = preview.result;
  const overridden = result?.picked.filter((p) => p.expiredOverride).length ?? 0;
  const pickedColumns: DataTableColumn<PickingResult["picked"][number]>[] = [
    { key: "batchId", header: "Batch", mono: true },
    { key: "expiry", header: "Expiry", render: (r) => r.expiry ?? dash },
    { key: "qty", header: "Quantity taken", align: "end" },
    { key: "expiredOverride", header: "Override", render: (r) => (r.expiredOverride ? <Badge tone="warning">Expired, overridden</Badge> : dash) },
  ];
  const excludedColumns: DataTableColumn<PickingResult["excluded"][number]>[] = [
    { key: "batchId", header: "Batch", mono: true },
    { key: "code", header: "Rule", render: (r) => EXCLUSION_LABELS[r.code] ?? r.code },
    { key: "detail", header: "Reason" },
    { key: "codeId", header: "Code", mono: true, render: (r) => r.code },
  ];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Batch picking"
        description="Propose the batches a line consumes: only available batches, the one expiring first first, and potency inside the line's band. Expired batches are used only with the Consume expired batch duty and a stated reason."
      />
      <form className={styles.stack} onSubmit={submit} noValidate>
        <Card title="Line">
          <div className={styles.form}>
            <FormField id="pick-posting" label="Posting date" error={errors.postingDate}>
              {(c) => <Input {...c} type="date" value={postingDate} onChange={(e) => setPostingDate(e.target.value)} invalid={Boolean(errors.postingDate)} />}
            </FormField>
            <FormField id="pick-qty" label="Line quantity" error={errors.qty}>
              {(c) => <Input {...c} inputMode="decimal" value={qty} onChange={(e) => setQty(e.target.value)} invalid={Boolean(errors.qty)} />}
            </FormField>
            <FormField id="pick-min" label="Potency minimum %" hint="Leave both empty for no potency band." error={errors.bandMin}>
              {(c) => <Input {...c} inputMode="decimal" value={bandMin} onChange={(e) => setBandMin(e.target.value)} invalid={Boolean(errors.bandMin)} />}
            </FormField>
            <FormField id="pick-max" label="Potency maximum %" error={errors.bandMax}>
              {(c) => <Input {...c} inputMode="decimal" value={bandMax} onChange={(e) => setBandMax(e.target.value)} invalid={Boolean(errors.bandMax)} />}
            </FormField>
            <FormField id="pick-reason" label="Reason to consume expired batches" hint="Needs the Consume expired batch duty; at least 10 characters." error={errors.reason}>
              {(c) => <Input {...c} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(errors.reason)} />}
            </FormField>
          </div>
        </Card>
        <RowCard title="Candidate batches" addLabel="Add batch" onAdd={batches.add}>
          {batches.rows.map((b, i) => (
            <fieldset key={i} className={styles.lineRow}>
              <legend className={styles.groupsLegend}>{`Batch ${i + 1}`}</legend>
              <FormField id={`pick-${i}-id`} label="Batch" error={errors[`id-${i}`]}>
                {(c) => <Input {...c} value={b.id} onChange={(e) => batches.update(i, { id: e.target.value })} invalid={Boolean(errors[`id-${i}`])} />}
              </FormField>
              <FormField id={`pick-${i}-qty`} label="Quantity" error={errors[`qty-${i}`]}>
                {(c) => <Input {...c} inputMode="decimal" value={b.qty} onChange={(e) => batches.update(i, { qty: e.target.value })} invalid={Boolean(errors[`qty-${i}`])} />}
              </FormField>
              <FormField id={`pick-${i}-disposition`} label="Disposition">
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
              <FormField id={`pick-${i}-expiry`} label="Expiry" error={errors[`expiry-${i}`]}>
                {(c) => <Input {...c} type="date" value={b.expiry} onChange={(e) => batches.update(i, { expiry: e.target.value })} invalid={Boolean(errors[`expiry-${i}`])} />}
              </FormField>
              <FormField id={`pick-${i}-potency`} label="Potency %" error={errors[`potency-${i}`]}>
                {(c) => <Input {...c} inputMode="decimal" value={b.potency} onChange={(e) => batches.update(i, { potency: e.target.value })} invalid={Boolean(errors[`potency-${i}`])} />}
              </FormField>
              <RemoveRow label={`Remove batch ${i + 1}`} show={batches.rows.length > 1} onRemove={() => batches.remove(i)} />
            </fieldset>
          ))}
        </RowCard>
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
              empty={{ title: "No batch can be picked", description: "Every candidate is blocked, expired or outside the potency band." }}
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

const OUTPUT_LABELS: Record<ReworkOutputDisposition, string> = { quarantine: "Quarantine", "on-hold": "On hold", available: "Available" };

/** FR-24.18, AC-24.18: plan a rework batch order for a batch. */
export function ReworkPage({ preview: call = defaultPreviewRework }: { preview?: (req: ReworkRequest) => Promise<ReworkResult> }) {
  // AC-24.18: rework of batch B-77 with the defaults.
  const [batchId, setBatchId] = useState("B-77");
  const [batchQty, setBatchQty] = useState("500");
  const [disposition, setDisposition] = useState<Disposition>("on-hold");
  const [reworkQty, setReworkQty] = useState("500");
  const [outputQty, setOutputQty] = useState("480");
  const [keep, setKeep] = useState(true);
  const [quality, setQuality] = useState(true);
  const [output, setOutput] = useState<ReworkOutputDisposition>("quarantine");
  const [capitalize, setCapitalize] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const preview = usePreview(call);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!batchId.trim()) found.batchId = "Enter the batch.";
    if (!QUANTITY.test(batchQty.trim())) found.batchQty = "Enter the batch quantity.";
    if (!QUANTITY.test(reworkQty.trim())) found.reworkQty = "Enter the quantity to rework.";
    if (!QUANTITY.test(outputQty.trim())) found.outputQty = "Enter the expected output.";
    if (quality && output === "available") found.output = "With a quality order the output waits for quality release; choose quarantine or on hold.";
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void preview.run({
      batch: { id: batchId.trim(), qty: batchQty.trim(), disposition },
      reworkQty: reworkQty.trim(),
      outputQty: outputQty.trim(),
      params: { keepBatchNumber: keep, requireQualityOrder: quality, outputDisposition: output, capitalize },
    });
  }

  const result = preview.result;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Rework batch order"
        description="Plan the rework of a batch: the batch, or part of it, is the first ingredient; the output goes back to the same batch number by default and waits in quarantine for quality release."
      />
      <form className={styles.stack} onSubmit={submit} noValidate>
        <Card title="Batch to rework">
          <div className={styles.form}>
            <FormField id="rework-batch" label="Batch" error={errors.batchId}>
              {(c) => <Input {...c} value={batchId} onChange={(e) => setBatchId(e.target.value)} invalid={Boolean(errors.batchId)} />}
            </FormField>
            <FormField id="rework-batch-qty" label="Batch quantity" error={errors.batchQty}>
              {(c) => <Input {...c} inputMode="decimal" value={batchQty} onChange={(e) => setBatchQty(e.target.value)} invalid={Boolean(errors.batchQty)} />}
            </FormField>
            <FormField id="rework-disposition" label="Current disposition">
              {(c) => (
                <Select {...c} value={disposition} onChange={(e) => setDisposition(e.target.value as Disposition)}>
                  {DISPOSITIONS.map((d) => (
                    <option key={d} value={d}>
                      {d}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="rework-qty" label="Quantity to rework" error={errors.reworkQty}>
              {(c) => <Input {...c} inputMode="decimal" value={reworkQty} onChange={(e) => setReworkQty(e.target.value)} invalid={Boolean(errors.reworkQty)} />}
            </FormField>
            <FormField id="rework-output-qty" label="Expected output" error={errors.outputQty}>
              {(c) => <Input {...c} inputMode="decimal" value={outputQty} onChange={(e) => setOutputQty(e.target.value)} invalid={Boolean(errors.outputQty)} />}
            </FormField>
          </div>
        </Card>
        <Card title="Rework parameters">
          <div className={styles.form}>
            <FormField id="rework-output" label="Output disposition" error={errors.output}>
              {(c) => (
                <Select {...c} value={output} onChange={(e) => setOutput(e.target.value as ReworkOutputDisposition)}>
                  {REWORK_OUTPUT_DISPOSITIONS.map((d) => (
                    <option key={d} value={d}>
                      {OUTPUT_LABELS[d]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <div className={styles.lineChecks}>
              <span className={styles.check}>
                <Input type="checkbox" id="rework-keep" checked={keep} onChange={(e) => setKeep(e.target.checked)} />
                <label htmlFor="rework-keep">Keep the batch number</label>
              </span>
              <span className={styles.check}>
                <Input type="checkbox" id="rework-quality" checked={quality} onChange={(e) => setQuality(e.target.checked)} />
                <label htmlFor="rework-quality">Require a quality order</label>
              </span>
              <span className={styles.check}>
                <Input type="checkbox" id="rework-capitalize" checked={capitalize} onChange={(e) => setCapitalize(e.target.checked)} />
                <label htmlFor="rework-capitalize">Capitalize the rework cost</label>
              </span>
            </div>
          </div>
        </Card>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={preview.busy}>
            Plan rework
          </Button>
        </div>
      </form>
      <Refusal title="Rework refused" failure={preview.failure} />
      {result ? (
        <Card title="Rework plan">
          <div className={styles.stack}>
            <StatGroup label="Rework plan">
              <StatTile label="Output batch" value={result.outputBatchId ?? "New number at report as finished"} />
              <StatTile label="Output" value={result.outputQty} />
              <StatTile label="Output disposition" value={result.outputDisposition} />
              <StatTile label="Quality order" value={result.qualityOrderRequired ? "Required" : "Not required"} />
              <StatTile label="Cost posts as" value={result.varianceType === "rework" ? "Rework variance" : "Capitalized"} />
            </StatGroup>
            <p className={styles.note} role="status">{`First ingredient: ${result.reworkQty} of ${result.sourceBatchId}. Genealogy parents: ${result.parents.join(", ")}`}</p>
          </div>
        </Card>
      ) : null}
    </div>
  );
}
