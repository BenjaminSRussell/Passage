import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createArrival, fetchPurchaseOrderLines, newKey, type ArrivalInput, type PurchaseOrderLine } from "./api/client";
import { DECIMAL, fieldErrors, UUID } from "../../shell/api/http";
import styles from "../../shell/pageLayout.module.css";
import { ProblemMessage } from "../../shell/errors";

const MAX_LINES = 500;
const LINE_NO = /^[1-9]\d*$/;

export interface ArrivalLineDraft {
  poLineNo: string;
  productId: string;
  overDeliveryPct: string;
  unit: string;
  batchTracked: boolean;
  serialTracked: boolean;
}

export interface ArrivalDraft {
  vendorId: string;
  purchaseOrderId: string;
  legalEntityId: string;
  packingSlip: string;
  carrier: string;
  palletCount: string;
  receiptDate: string;
  currency: string;
  lines: ArrivalLineDraft[];
}

type Errors = Record<string, string>;

export const emptyLine: ArrivalLineDraft = {
  poLineNo: "",
  productId: "",
  overDeliveryPct: "0",
  unit: "",
  batchTracked: false,
  serialTracked: false,
};

export const emptyArrival: ArrivalDraft = {
  vendorId: "",
  purchaseOrderId: "",
  legalEntityId: "",
  packingSlip: "",
  carrier: "",
  palletCount: "",
  receiptDate: "",
  currency: "USD",
  lines: [emptyLine],
};

/** Client checks that mirror POST /arrivals; the service has the last word. */
export function validateArrival(v: ArrivalDraft): Errors {
  const errors: Errors = {};
  for (const key of ["vendorId", "purchaseOrderId", "legalEntityId"] as const) {
    if (!UUID.test(v[key])) errors[key] = "Enter a UUID.";
  }
  if (v.packingSlip.trim() === "" || v.packingSlip.length > 64) errors.packingSlip = "Enter a packing slip of up to 64 characters.";
  if (v.carrier.length > 128) errors.carrier = "Enter a carrier of up to 128 characters.";
  if (v.palletCount !== "" && !/^\d+$/.test(v.palletCount)) errors.palletCount = "Enter a whole number.";
  if (!/^\d{4}-\d{2}-\d{2}$/.test(v.receiptDate)) errors.receiptDate = "Enter the receipt date.";
  if (!/^[A-Z]{3}$/.test(v.currency)) errors.currency = "Use a 3-letter upper-case currency code.";
  if (v.lines.length < 1 || v.lines.length > MAX_LINES) errors.lines = `Enter 1 to ${MAX_LINES} lines.`;
  v.lines.forEach((l, i) => {
    const at = (field: string) => `lines[${i}].${field}`;
    if (!LINE_NO.test(l.poLineNo)) errors[at("poLineNo")] = "Choose the purchase order line.";
    else if (v.lines.findIndex((o) => o.poLineNo === l.poLineNo) !== i) errors[at("poLineNo")] = "Use each purchase order line once.";
    if (!UUID.test(l.productId)) errors[at("productId")] = "Enter a UUID.";
    if (!DECIMAL.test(l.overDeliveryPct) || Number(l.overDeliveryPct) > 100) errors[at("overDeliveryPct")] = "Enter a percent from 0 to 100.";
    if (l.unit.trim() === "" || l.unit.length > 16) errors[at("unit")] = "Enter a unit of up to 16 characters.";
  });
  return errors;
}

/** Copies the product and unit of the picked purchase order line, which the service checks the arrival line against. */
export function pickPOLine(line: ArrivalLineDraft, poLineNo: string, poLines: PurchaseOrderLine[]): ArrivalLineDraft {
  const picked = poLines.find((l) => String(l.lineNo) === poLineNo);
  return picked ? { ...line, poLineNo, productId: picked.productId ?? line.productId, unit: picked.unit } : { ...line, poLineNo };
}

const poLineLabel = (l: PurchaseOrderLine) => `${l.lineNo}: ${l.description ?? l.productId ?? "line"} (${l.qty} ${l.unit})`;

export function toArrivalInput(v: ArrivalDraft): ArrivalInput {
  return {
    vendorId: v.vendorId,
    purchaseOrderId: v.purchaseOrderId,
    legalEntityId: v.legalEntityId,
    packingSlip: v.packingSlip.trim(),
    ...(v.carrier ? { carrier: v.carrier } : {}),
    ...(v.palletCount ? { palletCount: Number(v.palletCount) } : {}),
    receiptDate: v.receiptDate,
    currency: v.currency,
    lines: v.lines.map((l) => ({
      poLineNo: Number(l.poLineNo),
      productId: l.productId,
      overDeliveryPct: l.overDeliveryPct,
      unit: l.unit.trim(),
      batchTracked: l.batchTracked,
      serialTracked: l.serialTracked,
    })),
  };
}

/** Record a delivery at the dock (POST /arrivals); the receipt is registered afterwards. */
export function NewArrivalPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const key = useRef(newKey());
  const carried = useSearchParams()[0];
  const [values, setValues] = useState<ArrivalDraft>(() => ({ ...emptyArrival, purchaseOrderId: carried.get("purchaseOrderId") ?? "", vendorId: carried.get("vendorId") ?? "" }));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const purchaseOrder = useQuery({
    queryKey: ["receiving", "purchase-order-lines", values.purchaseOrderId],
    queryFn: () => fetchPurchaseOrderLines(values.purchaseOrderId),
    enabled: UUID.test(values.purchaseOrderId),
  });
  const poLines = purchaseOrder.data?.filter((l) => l.status !== "CANCELLED");

  const update = (patch: Partial<ArrivalDraft>) => setValues((prev) => ({ ...prev, ...patch }));
  const updateLine = (index: number, patch: Partial<ArrivalLineDraft>) =>
    setValues((prev) => ({ ...prev, lines: prev.lines.map((l, i) => (i === index ? { ...l, ...patch } : l)) }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateArrival(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const receipt = await createArrival(toArrivalInput(values), key.current);
      key.current = newKey();
      await queryClient.invalidateQueries({ queryKey: ["receiving", "receipts"] });
      navigate(`/receiving/receipts/${encodeURIComponent(receipt.id)}`);
    } catch (error) {
      setErrors(fieldErrors(error));
      setFailure(error);
    } finally {
      setBusy(false);
    }
  }

  const text = (field: keyof Omit<ArrivalDraft, "lines">, label: string, extra: { hint?: string; optional?: boolean; type?: string; mono?: boolean } = {}) => (
    <FormField id={`arrival-${field}`} label={label} hint={extra.hint} optional={extra.optional} error={errors[field]}>
      {(c) => <Input {...c} type={extra.type} className={extra.mono ? styles.mono : undefined} value={values[field]} onChange={(e) => update({ [field]: e.target.value })} invalid={Boolean(errors[field])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="Record arrival" description="Log a delivery as it reaches the dock. You register the quantities received on the receipt afterwards." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New arrival">
          {text("vendorId", "Vendor ID", { mono: true })}
          {text("purchaseOrderId", "Purchase order ID", { mono: true })}
          {text("legalEntityId", "Legal entity ID", { mono: true })}
          {text("packingSlip", "Packing slip", { hint: "Unique per vendor." })}
          {text("carrier", "Carrier", { optional: true })}
          {text("palletCount", "Pallet count", { optional: true })}
          {text("receiptDate", "Receipt date", { type: "date" })}
          {text("currency", "Currency", { hint: "ISO 4217, such as USD." })}
          {errors.lines ? (
            <div className={styles.formActions}>
              <Alert tone="danger" live="alert">
                {errors.lines}
              </Alert>
            </div>
          ) : null}
          {values.lines.map((line, i) => {
            const n = i + 1;
            const at = (field: string) => `lines[${i}].${field}`;
            const field = (name: keyof ArrivalLineDraft, label: string, extra: { mono?: boolean } = {}) => (
              <FormField id={`line-${i}-${name}`} label={`Line ${n} ${label}`} error={errors[at(name)]}>
                {(c) => (
                  <Input {...c} className={extra.mono ? styles.mono : undefined} value={String(line[name])} onChange={(e) => updateLine(i, { [name]: e.target.value })} invalid={Boolean(errors[at(name)])} />
                )}
              </FormField>
            );
            return (
              <fieldset key={i} className={styles.group}>
                <legend className={styles.legend}>Line {n}</legend>
                <FormField
                  id={`line-${i}-poLineNo`}
                  label={`Line ${n} PO line`}
                  hint={poLines ? "Fills in the product and unit; the service takes the open quantity and price from it." : purchaseOrder.isError ? "Could not read the purchase order's lines; enter the line number." : "Enter the purchase order ID to choose from its lines."}
                  error={errors[at("poLineNo")]}
                >
                  {(c) =>
                    poLines ? (
                      <Select {...c} value={line.poLineNo} onChange={(e) => updateLine(i, pickPOLine(line, e.target.value, poLines))} invalid={Boolean(errors[at("poLineNo")])}>
                        <option value="">Choose a line</option>
                        {poLines.map((l) => (
                          <option key={l.lineNo} value={String(l.lineNo)}>
                            {poLineLabel(l)}
                          </option>
                        ))}
                      </Select>
                    ) : (
                      <Input {...c} inputMode="numeric" value={line.poLineNo} onChange={(e) => updateLine(i, { poLineNo: e.target.value })} invalid={Boolean(errors[at("poLineNo")])} />
                    )
                  }
                </FormField>
                {field("productId", "product ID", { mono: true })}
                {field("overDeliveryPct", "over-delivery percent")}
                {field("unit", "unit")}
                <span className={styles.check}>
                  <Input id={`line-${i}-batchTracked`} type="checkbox" checked={line.batchTracked} onChange={(e) => updateLine(i, { batchTracked: e.target.checked })} />
                  <label htmlFor={`line-${i}-batchTracked`}>{`Line ${n} batch tracked`}</label>
                </span>
                <span className={styles.check}>
                  <Input id={`line-${i}-serialTracked`} type="checkbox" checked={line.serialTracked} onChange={(e) => updateLine(i, { serialTracked: e.target.checked })} />
                  <label htmlFor={`line-${i}-serialTracked`}>{`Line ${n} serial tracked`}</label>
                </span>
                {values.lines.length > 1 ? (
                  <div>
                    <Button onClick={() => update({ lines: values.lines.filter((_, j) => j !== i) })}>{`Remove line ${n}`}</Button>
                  </div>
                ) : null}
              </fieldset>
            );
          })}
          <div className={styles.formActions}>
            <Button onClick={() => update({ lines: [...values.lines, emptyLine] })}>Add line</Button>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not record the arrival" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Record arrival
            </Button>
            <Link className={buttonClassName("ghost")} to="/receiving/receipts">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
