import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { CONSIGNMENT_LINE_DIMENSIONS, createConsignment, describeProblem, type ConsignmentLineDimension, type Problem } from "./api/client";
import { DECIMAL, sentence, today, UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export interface ConsignmentLineValues {
  productId: string;
  qty: string;
  unit: string;
  consignmentPrice: string;
  dimensions: Record<ConsignmentLineDimension, string>;
}

export interface ConsignmentValues {
  legalEntityId: string;
  vendorId: string;
  siteId: string;
  warehouseId: string;
  requestedDate: string;
  currency: string;
  lines: ConsignmentLineValues[];
}

type HeaderField = "legalEntityId" | "vendorId" | "siteId" | "warehouseId" | "requestedDate" | "currency" | "lines";
type LineField = "productId" | "qty" | "unit" | "consignmentPrice";

export interface ConsignmentErrors {
  header: Partial<Record<HeaderField, string>>;
  lines: Partial<Record<LineField, string>>[];
}

const emptyLine = (): ConsignmentLineValues => ({ productId: "", qty: "", unit: "EA", consignmentPrice: "", dimensions: { location: "", batch: "", serial: "" } });

const code = /^\S{1,60}$/;

/** Client checks that mirror the service: header codes, positive quantities, serial quantity 1, non-negative price. */
export function validateConsignment(values: ConsignmentValues): ConsignmentErrors {
  const header: ConsignmentErrors["header"] = {};
  if (!UUID.test(values.legalEntityId.trim())) header.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!code.test(values.vendorId.trim())) header.vendorId = "Enter the vendor who owns the stock.";
  if (!code.test(values.siteId.trim())) header.siteId = "Enter the receiving site.";
  if (!code.test(values.warehouseId.trim())) header.warehouseId = "Enter the receiving warehouse.";
  if (!/^\d{4}-\d{2}-\d{2}$/.test(values.requestedDate)) header.requestedDate = "Enter the requested delivery date.";
  if (!/^[A-Z]{3}$/.test(values.currency.trim())) header.currency = "Enter a three-letter currency code, such as USD.";
  if (values.lines.length === 0) header.lines = "Add at least one line.";
  const lines = values.lines.map((line) => {
    const errors: Partial<Record<LineField, string>> = {};
    if (!UUID.test(line.productId.trim())) errors.productId = "Enter the product id (a UUID).";
    const qty = line.qty.trim();
    if (!DECIMAL.test(qty) || Number(qty) <= 0) errors.qty = "Enter a quantity above zero.";
    else if (line.dimensions.serial.trim() !== "" && Number(qty) !== 1) errors.qty = "A serial number arrives one unit at a time.";
    if (!/^[A-Z0-9_-]{1,12}$/.test(line.unit.trim())) errors.unit = "Enter a unit code such as EA.";
    const price = line.consignmentPrice.trim();
    if (price !== "" && (!DECIMAL.test(price) || Number(price) < 0)) errors.consignmentPrice = "Enter a price of zero or more.";
    return errors;
  });
  return { header, lines };
}

function hasErrors(errors: ConsignmentErrors): boolean {
  return Object.keys(errors.header).length > 0 || errors.lines.some((l) => Object.keys(l).length > 0);
}

/** Maps service field errors (vendorId, lines[0].qty, ...) onto the form. */
function mapProblem(problem: Problem, lineCount: number): ConsignmentErrors | null {
  const out: ConsignmentErrors = { header: {}, lines: Array.from({ length: lineCount }, () => ({})) };
  let found = false;
  for (const [name, message] of Object.entries(problem.fields)) {
    const line = /^lines\[(\d+)\]\.(\w+)/.exec(name);
    if (line) {
      const i = Number(line[1]);
      if (out.lines[i] && ["productId", "qty", "unit", "consignmentPrice"].includes(line[2] ?? "")) {
        out.lines[i][line[2] as LineField] = message;
        found = true;
      }
    } else if (["legalEntityId", "vendorId", "siteId", "warehouseId", "requestedDate", "currency", "lines"].includes(name)) {
      out.header[name as HeaderField] = message;
      found = true;
    }
  }
  return found ? out : null;
}

/** New consignment replenishment order (FR-16.17). Creating it receives nothing yet. */
export function NewConsignmentPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<ConsignmentValues>({
    legalEntityId: "",
    vendorId: "",
    siteId: "",
    warehouseId: "",
    requestedDate: today(),
    currency: "",
    lines: [emptyLine()],
  });
  const [errors, setErrors] = useState<ConsignmentErrors>({ header: {}, lines: [] });
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per filled-in form: a double submit replays instead of creating twice.
  const key = useRef(crypto.randomUUID());

  const change = (next: ConsignmentValues) => {
    setValues(next);
    key.current = crypto.randomUUID();
  };
  const set = (name: Exclude<keyof ConsignmentValues, "lines">) => (event: { target: { value: string } }) =>
    change({ ...values, [name]: name === "currency" ? event.target.value.toUpperCase() : event.target.value });
  const setLine = (index: number, patch: Partial<ConsignmentLineValues>) =>
    change({ ...values, lines: values.lines.map((line, i) => (i === index ? { ...line, ...patch } : line)) });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateConsignment(values);
    setErrors(found);
    setFailure(null);
    if (hasErrors(found)) return;
    setBusy(true);
    try {
      const created = await createConsignment(
        {
          legalEntityId: values.legalEntityId.trim(),
          vendorId: values.vendorId.trim(),
          siteId: values.siteId.trim(),
          warehouseId: values.warehouseId.trim(),
          requestedDate: values.requestedDate,
          currency: values.currency.trim(),
          lines: values.lines.map((line) => ({
            productId: line.productId.trim(),
            qty: line.qty.trim(),
            unit: line.unit.trim(),
            consignmentPrice: line.consignmentPrice,
            dimensions: line.dimensions,
          })),
        },
        key.current,
      );
      navigate(`/inventory/consignment-orders/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const mapped = mapProblem(problem, values.lines.length);
      if (mapped) setErrors(mapped);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = crypto.randomUUID();
    } finally {
      setBusy(false);
    }
  }

  const he = errors.header;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="New consignment order"
        description="Order stock the vendor keeps owning. It is received as quantity only and valued when you take it into company ownership."
      />
      <form onSubmit={submit} noValidate className={styles.stack}>
        <Card title="Vendor and destination">
          <div className={styles.form}>
            <FormField id="cro-legal-entity" label="Legal entity" error={he.legalEntityId}>
              {(control) => <Input {...control} value={values.legalEntityId} onChange={set("legalEntityId")} invalid={Boolean(he.legalEntityId)} />}
            </FormField>
            <FormField id="cro-vendor" label="Vendor" hint="Becomes the owner of the received stock." error={he.vendorId}>
              {(control) => <Input {...control} value={values.vendorId} maxLength={60} onChange={set("vendorId")} invalid={Boolean(he.vendorId)} />}
            </FormField>
            <FormField id="cro-site" label="Site" error={he.siteId}>
              {(control) => <Input {...control} value={values.siteId} maxLength={60} onChange={set("siteId")} invalid={Boolean(he.siteId)} />}
            </FormField>
            <FormField id="cro-warehouse" label="Warehouse" error={he.warehouseId}>
              {(control) => <Input {...control} value={values.warehouseId} maxLength={60} onChange={set("warehouseId")} invalid={Boolean(he.warehouseId)} />}
            </FormField>
            <FormField id="cro-requested" label="Requested date" error={he.requestedDate}>
              {(control) => <Input {...control} type="date" value={values.requestedDate} onChange={set("requestedDate")} invalid={Boolean(he.requestedDate)} />}
            </FormField>
            <FormField id="cro-currency" label="Currency" hint="Currency of the consignment prices." error={he.currency}>
              {(control) => <Input {...control} value={values.currency} maxLength={3} onChange={set("currency")} invalid={Boolean(he.currency)} />}
            </FormField>
          </div>
        </Card>
        {values.lines.map((line, index) => {
          const le = errors.lines[index] ?? {};
          const n = index + 1;
          return (
            <Card key={index} title={`Line ${n}`}>
              <div className={styles.form}>
                <FormField id={`cro-line-${n}-product`} label="Product" error={le.productId}>
                  {(control) => <Input {...control} value={line.productId} onChange={(e) => setLine(index, { productId: e.target.value })} invalid={Boolean(le.productId)} />}
                </FormField>
                <FormField id={`cro-line-${n}-qty`} label="Quantity" error={le.qty}>
                  {(control) => <Input {...control} inputMode="decimal" value={line.qty} onChange={(e) => setLine(index, { qty: e.target.value })} invalid={Boolean(le.qty)} />}
                </FormField>
                <FormField id={`cro-line-${n}-unit`} label="Unit" error={le.unit}>
                  {(control) => (
                    <Input {...control} value={line.unit} maxLength={12} onChange={(e) => setLine(index, { unit: e.target.value.toUpperCase() })} invalid={Boolean(le.unit)} />
                  )}
                </FormField>
                <FormField id={`cro-line-${n}-price`} label="Consignment price" optional hint="Per unit; used when the stock changes ownership." error={le.consignmentPrice}>
                  {(control) => (
                    <Input
                      {...control}
                      inputMode="decimal"
                      value={line.consignmentPrice}
                      onChange={(e) => setLine(index, { consignmentPrice: e.target.value })}
                      invalid={Boolean(le.consignmentPrice)}
                    />
                  )}
                </FormField>
                {CONSIGNMENT_LINE_DIMENSIONS.map((name) => (
                  <FormField key={name} id={`cro-line-${n}-${name}`} label={sentence(name)} optional>
                    {(control) => (
                      <Input
                        {...control}
                        value={line.dimensions[name]}
                        maxLength={60}
                        onChange={(e) => setLine(index, { dimensions: { ...line.dimensions, [name]: e.target.value } })}
                      />
                    )}
                  </FormField>
                ))}
                {values.lines.length > 1 ? (
                  <div className={styles.formActions}>
                    <Button size="sm" onClick={() => change({ ...values, lines: values.lines.filter((_, i) => i !== index) })}>
                      Remove line {n}
                    </Button>
                  </div>
                ) : null}
              </div>
            </Card>
          );
        })}
        {he.lines ? (
          <Alert tone="danger" title="No lines" live="alert">
            {he.lines}
          </Alert>
        ) : null}
        {failure ? (
          <Alert tone="danger" title="Could not create the consignment order" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
        <div className={styles.formActions}>
          <Button onClick={() => change({ ...values, lines: [...values.lines, emptyLine()] })}>Add line</Button>
          <Button type="submit" variant="primary" loading={busy}>
            Create consignment order
          </Button>
          <Link className={buttonClassName("ghost")} to="/inventory/consignment-orders">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}
