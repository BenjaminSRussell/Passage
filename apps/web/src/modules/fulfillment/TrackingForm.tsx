// Batch, serial and location capture for a picked line (BR-28.10, FR-28.2). The entries of one
// line are replaced in one command; a serial is one unit. The form keeps one Idempotency-Key
// until its values change and sends the shipment version as If-Match.
import { useState, type FormEvent } from "react";
import { Button, Card, FormField, Input, Select } from "../../shell/ui";
import { captureTracking, describeProblem, type Problem, type Shipment, type ShipmentLine, type TrackingEntry, type TrackingEntryInput } from "./api/client";
import { compareDecimal, DECIMAL, shortId, subtractDecimal, sumDecimals } from "./format";
import { explain, ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";

/** Exact quantity sum without trailing zeros: "6" rather than "6.000000". */
function sumQty(values: string[]): string {
  const total = sumDecimals(values, 6);
  return total.includes(".") ? total.replace(/\.?0+$/, "") : total;
}

interface Row {
  batchNo: string;
  serialNo: string;
  locationCode: string;
  qty: string;
}

const CODE = /^[A-Za-z0-9][A-Za-z0-9_./-]{0,63}$/;
const CAPTURE_STATUSES = ["PICKING", "PICKED", "PACKED", "PARTIALLY_SHIPPED"];

/** Lines that can take tracking now: something is picked and the shipment has not fully shipped. */
export function trackableLines(shipment: Shipment): ShipmentLine[] {
  if (!CAPTURE_STATUSES.includes(shipment.status)) return [];
  return shipment.lines.filter((l) => compareDecimal(l.qtyPicked, "0") > 0);
}

export function openEntries(line: ShipmentLine): TrackingEntry[] {
  return (line.tracking ?? []).filter((e) => e.packingSlipId === null);
}

/** What the line's tracking covers against what it still has to ship: "6 of 10". */
export function trackingCover(line: ShipmentLine): string {
  const tracked = sumQty((line.tracking ?? []).map((e) => e.qty));
  return `${tracked} of ${line.qtyPicked}`;
}

function toRows(line: ShipmentLine): Row[] {
  const rows = openEntries(line).map((e) => ({ batchNo: e.batchNo ?? "", serialNo: e.serialNo ?? "", locationCode: e.locationCode ?? "", qty: e.qty }));
  return rows.length > 0 ? rows : [{ batchNo: "", serialNo: "", locationCode: "", qty: "" }];
}

export function TrackingForm({ shipment, onDone }: { shipment: Shipment; onDone(next: Shipment): void }) {
  const lines = trackableLines(shipment);
  const [lineId, setLineId] = useState(lines[0]?.id ?? "");
  const line = lines.find((l) => l.id === lineId) ?? lines[0];
  const [rows, setRows] = useState<Row[]>(() => (line ? toRows(line) : []));
  const [errors, setErrors] = useState<Record<number, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  if (!line) return null;
  // Entries already on a packing slip stay; they count against the picked quantity.
  const shipped = sumQty((line.tracking ?? []).filter((e) => e.packingSlipId !== null).map((e) => e.qty));

  function choose(id: string) {
    const next = lines.find((l) => l.id === id);
    setLineId(id);
    setRows(next ? toRows(next) : []);
    setErrors({});
    setFailure(null);
    key.renew();
  }

  function edit(index: number, patch: Partial<Row>) {
    setRows(rows.map((r, i) => (i === index ? { ...r, ...patch } : r)));
    key.renew();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<number, string> = {};
    const entries: TrackingEntryInput[] = [];
    // Blank rows are ignored, so an all-blank form clears the line's entries. Errors are keyed
    // by the row's position on the form, so they land on the row that caused them.
    const serials = new Set<string>();
    let firstFilled = -1;
    rows.forEach((r, i) => {
      if (!(r.batchNo.trim() || r.serialNo.trim() || r.locationCode.trim() || r.qty.trim())) return;
      if (firstFilled < 0) firstFilled = i;
      const batchNo = r.batchNo.trim();
      const serialNo = r.serialNo.trim();
      const locationCode = r.locationCode.trim();
      const qty = serialNo && !r.qty.trim() ? "1" : r.qty.trim();
      if (!batchNo && !serialNo) found[i] = "Enter a batch or a serial number.";
      else if ((batchNo && !CODE.test(batchNo)) || (serialNo && !CODE.test(serialNo)) || (locationCode && !CODE.test(locationCode)))
        found[i] = "Codes use letters, digits and _ . / - only, at most 64 characters.";
      else if (!DECIMAL.test(qty) || compareDecimal(qty, "0") <= 0) found[i] = "Enter a quantity above 0 with at most 6 decimals.";
      else if (serialNo && qty !== "1") found[i] = "A serial number is one unit.";
      else if (serialNo && serials.has(serialNo)) found[i] = `Serial ${serialNo} is listed twice.`;
      else {
        if (serialNo) serials.add(serialNo);
        entries.push({ ...(batchNo ? { batchNo } : {}), ...(serialNo ? { serialNo } : {}), ...(locationCode ? { locationCode } : {}), qty });
      }
    });
    if (Object.keys(found).length === 0 && compareDecimal(sumQty([shipped, ...entries.map((e) => e.qty)]), line.qtyPicked) > 0) {
      found[Math.max(firstFilled, 0)] = `Tracking cannot exceed the picked quantity ${line.qtyPicked} (FUL-0035).`;
    }
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const next = await captureTracking(shipment.id, line.id, shipment.version, entries, key.current());
      key.renew();
      onDone(next);
    } catch (error) {
      const problem = explain(describeProblem(error));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  const covered = sumQty([shipped, ...rows.map((r) => (DECIMAL.test(r.qty.trim()) ? r.qty.trim() : r.serialNo.trim() && !r.qty.trim() ? "1" : "0"))]);
  return (
    <Card title="Batch and serial tracking">
      <form className={styles.form} onSubmit={submit} noValidate>
        <p className={styles.note}>
          Record the batch or serial numbers and the pick location of what was picked. A tracked line cannot post a packing slip until its entries cover the quantity the slip ships.
          Saving replaces the entries that have not shipped.
        </p>
        <FormField id="tracking-line" label="Shipment line">
          {(control) => (
            <Select {...control} value={line.id} onChange={(event) => choose(event.target.value)}>
              {lines.map((l) => (
                <option key={l.id} value={l.id}>
                  {`Line ${shortId(l.orderLineId)} (${l.unit}), picked ${l.qtyPicked}${l.trackingRequired ? ", tracking required" : ""}`}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        {rows.map((row, i) => (
          <fieldset key={i} className={styles.form}>
            <legend>{`Entry ${i + 1}`}</legend>
            <FormField id={`tracking-batch-${i}`} label={`Entry ${i + 1} batch`} optional error={errors[i]}>
              {(control) => <Input {...control} value={row.batchNo} invalid={Boolean(errors[i])} onChange={(event) => edit(i, { batchNo: event.target.value })} />}
            </FormField>
            <FormField id={`tracking-serial-${i}`} label={`Entry ${i + 1} serial`} optional>
              {(control) => <Input {...control} value={row.serialNo} onChange={(event) => edit(i, { serialNo: event.target.value })} />}
            </FormField>
            <FormField id={`tracking-location-${i}`} label={`Entry ${i + 1} location`} optional>
              {(control) => <Input {...control} value={row.locationCode} onChange={(event) => edit(i, { locationCode: event.target.value })} />}
            </FormField>
            <FormField id={`tracking-qty-${i}`} label={`Entry ${i + 1} quantity`} hint={row.serialNo.trim() ? "A serial is one unit." : undefined}>
              {(control) => <Input {...control} inputMode="decimal" value={row.qty} onChange={(event) => edit(i, { qty: event.target.value })} />}
            </FormField>
            {rows.length > 1 ? (
              <div className={styles.formActions}>
                <Button
                  type="button"
                  size="sm"
                  onClick={() => {
                    setRows(rows.filter((_, j) => j !== i));
                    key.renew();
                  }}
                >
                  {`Remove entry ${i + 1}`}
                </Button>
              </div>
            ) : null}
          </fieldset>
        ))}
        <p className={styles.note}>{`Covered ${covered} of ${line.qtyPicked} picked${compareDecimal(shipped, "0") > 0 ? ` (${shipped} already shipped)` : ""}${compareDecimal(line.qtyPicked, covered) > 0 ? `, ${subtractDecimal(line.qtyPicked, covered)} untracked` : ""}.`}</p>
        <ProblemAlert title="Could not save the tracking" problem={failure} />
        <div className={styles.formActions}>
          <Button
            type="button"
            onClick={() => {
              setRows([...rows, { batchNo: "", serialNo: "", locationCode: "", qty: "" }]);
              key.renew();
            }}
          >
            Add entry
          </Button>
          <Button type="submit" variant="primary" loading={busy}>
            Save tracking
          </Button>
        </div>
      </form>
    </Card>
  );
}
