import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { describeProblem, resolvePrice, type Problem, type ResolvedLine, type ResolveRequest, type ResolveResponse, type TraceAccess, type TraceStep } from "./api/client";
import { DAY, DECIMAL, today, TYPE_LABELS, UUID } from "./format";
import styles from "./pages.module.css";

interface LineDraft {
  productId: string;
  productGroupId: string;
  unit: string;
  quantity: string;
}

interface HeaderDraft {
  legalEntityId: string;
  customerId: string;
  customerGroupId: string;
  currency: string;
  pricingDate: string;
}

type Errors = Record<string, string>;

const blankLine: LineDraft = { productId: "", productGroupId: "", unit: "EA", quantity: "1" };

/** Client checks that mirror the resolve validation (PRD §5.26.5). */
export function validateSimulation(h: HeaderDraft, lines: LineDraft[]): Errors {
  const errors: Errors = {};
  if (!UUID.test(h.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (h.customerId.trim() === "") errors.customerId = "Enter the customer.";
  if (!/^[A-Z]{3}$/.test(h.currency)) errors.currency = "Use a three-letter ISO 4217 code, such as USD.";
  if (!DAY.test(h.pricingDate)) errors.pricingDate = "Enter the pricing date.";
  lines.forEach((l, i) => {
    if (l.productId.trim() === "") errors[`lines[${i}].productId`] = "Enter the product.";
    if (l.unit.trim() === "") errors[`lines[${i}].unit`] = "Enter the unit.";
    if (!DECIMAL.test(l.quantity) || Number(l.quantity) <= 0) errors[`lines[${i}].quantity`] = "Enter a quantity above 0.";
  });
  return errors;
}

function toRequest(h: HeaderDraft, lines: LineDraft[]): ResolveRequest {
  return {
    legalEntityId: h.legalEntityId.trim(),
    customerId: h.customerId.trim(),
    customerGroupId: h.customerGroupId.trim() || undefined,
    currency: h.currency,
    pricingDate: h.pricingDate,
    lines: lines.map((l) => ({ productId: l.productId.trim(), productGroupId: l.productGroupId.trim() || undefined, unit: l.unit.trim(), quantity: l.quantity })),
  };
}

const lineColumns: DataTableColumn<ResolvedLine>[] = [
  { key: "lineNo", header: "Line", align: "end" },
  { key: "quantity", header: "Quantity", mono: true, align: "end", render: (l) => `${l.quantity} ${l.unit}` },
  { key: "unitPrice", header: "Price", mono: true, align: "end", render: (l) => (l.errorCode ? "—" : l.unitPrice) },
  { key: "netUnitPrice", header: "Net unit", mono: true, align: "end", render: (l) => (l.errorCode ? "—" : l.netUnitPrice) },
  { key: "lineNet", header: "Line net", mono: true, align: "end", render: (l) => (l.errorCode ? "—" : l.lineNet) },
  {
    key: "applied",
    header: "Conditions applied",
    render: (l) =>
      l.errorCode
        ? `${l.errorCode}: ${l.errorDetail ?? "no price"}`
        : l.applied.map((a) => `${TYPE_LABELS[a.type] ?? a.type} (access ${a.access})`).join(", ") + (l.exclusive ? " · exclusive" : ""),
  },
];

interface TraceRow extends TraceAccess {
  condition: string;
}

const traceColumns: DataTableColumn<TraceRow>[] = [
  { key: "condition", header: "Condition" },
  { key: "seq", header: "Access", align: "end" },
  { key: "access", header: "Key" },
  { key: "evaluated", header: "Evaluated", render: (r) => (r.evaluated ? "Yes" : "No") },
  { key: "candidates", header: "Candidates", mono: true, render: (r) => (r.candidates.length > 0 ? r.candidates.join(", ") : "—") },
  { key: "winner", header: "Winner", mono: true, render: (r) => r.winner ?? "—" },
  { key: "reason", header: "Reason" },
];

function traceRows(steps: TraceStep[] = []): TraceRow[] {
  return steps.flatMap((s) => s.accesses.map((a) => ({ ...a, condition: TYPE_LABELS[s.condition as keyof typeof TYPE_LABELS] ?? s.condition })));
}

/** Simulation / explain (FR-26.4, FR-26.13): price a tuple against the active agreements and show why. */
export function SimulatePage() {
  const [header, setHeader] = useState<HeaderDraft>({ legalEntityId: "", customerId: "", customerGroupId: "", currency: "USD", pricingDate: today() });
  const [lines, setLines] = useState<LineDraft[]>([blankLine]);
  const [errors, setErrors] = useState<Errors>({});
  const [result, setResult] = useState<ResolveResponse | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const setH = (patch: Partial<HeaderDraft>) => setHeader((prev) => ({ ...prev, ...patch }));
  const setLine = (i: number, patch: Partial<LineDraft>) => setLines((prev) => prev.map((l, j) => (j === i ? { ...l, ...patch } : l)));

  async function run(event: FormEvent) {
    event.preventDefault();
    const found = validateSimulation(header, lines);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setResult(await resolvePrice(toRequest(header, lines), true));
    } catch (error) {
      const problem = describeProblem(error);
      setResult(null);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const headerField = (id: string, name: keyof HeaderDraft, label: string, extra: { optional?: boolean; type?: string; hint?: string } = {}) => (
    <FormField id={id} label={label} optional={extra.optional} hint={extra.hint} error={errors[name]}>
      {(c) => <Input {...c} type={extra.type ?? "text"} value={header[name]} onChange={(e) => setH({ [name]: e.target.value })} invalid={Boolean(errors[name])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="Price simulation" description="Enter a customer, date and lines, then read which agreements produced each net price." />
      <Card>
        <form className={styles.form} onSubmit={run} noValidate>
          {headerField("sim-legal-entity", "legalEntityId", "Legal entity id")}
          {headerField("sim-customer", "customerId", "Customer")}
          {headerField("sim-customer-group", "customerGroupId", "Customer group", { optional: true })}
          {headerField("sim-currency", "currency", "Currency")}
          {headerField("sim-date", "pricingDate", "Pricing date", { type: "date", hint: "Usually the requested ship date." })}
          {lines.map((l, i) => (
            <fieldset key={i} className={styles.lineSet}>
              <legend className={styles.groupsLegend}>{`Line ${i + 1}`}</legend>
              <FormField id={`sim-product-${i}`} label="Product" error={errors[`lines[${i}].productId`]}>
                {(c) => <Input {...c} value={l.productId} onChange={(e) => setLine(i, { productId: e.target.value })} invalid={Boolean(errors[`lines[${i}].productId`])} />}
              </FormField>
              <FormField id={`sim-group-${i}`} label="Product group" optional>
                {(c) => <Input {...c} value={l.productGroupId} onChange={(e) => setLine(i, { productGroupId: e.target.value })} />}
              </FormField>
              <FormField id={`sim-unit-${i}`} label="Unit" error={errors[`lines[${i}].unit`]}>
                {(c) => <Input {...c} value={l.unit} onChange={(e) => setLine(i, { unit: e.target.value })} invalid={Boolean(errors[`lines[${i}].unit`])} />}
              </FormField>
              <FormField id={`sim-qty-${i}`} label="Quantity" error={errors[`lines[${i}].quantity`]}>
                {(c) => <Input {...c} inputMode="decimal" value={l.quantity} onChange={(e) => setLine(i, { quantity: e.target.value })} invalid={Boolean(errors[`lines[${i}].quantity`])} />}
              </FormField>
              {lines.length > 1 ? (
                <Button variant="ghost" size="sm" onClick={() => setLines((prev) => prev.filter((_, j) => j !== i))}>
                  {`Remove line ${i + 1}`}
                </Button>
              ) : null}
            </fieldset>
          ))}
          <div className={styles.formActions}>
            <Button variant="secondary" onClick={() => setLines((prev) => [...prev, blankLine])}>
              Add line
            </Button>
            <Button type="submit" variant="primary" loading={busy}>
              Run simulation
            </Button>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The simulation failed" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
        </form>
      </Card>
      {result ? (
        <>
          <StatGroup label="Simulation totals">
            <StatTile label="Subtotal" value={`${result.subtotal} ${result.currency}`} />
            <StatTile label="Total discount" value={`${result.totalDiscount} ${result.currency}`} />
            {Number(result.surcharge) !== 0 ? <StatTile label="Small-order surcharge" value={`${result.surcharge} ${result.currency}`} /> : null}
            <StatTile label="Order net" value={`${result.orderNet} ${result.currency}`} />
          </StatGroup>
          <Card title="Priced lines">
            <div className={styles.stack}>
              <DataTable label="Priced lines" columns={lineColumns} rows={result.lines} getRowKey={(l) => String(l.lineNo)} />
              <p className={`${styles.note} ${styles.mono}`}>{`Explain reference ${result.explainRef}`}</p>
              {result.explainTruncated ? <p className={styles.note}>The explanation is larger than the configured limit and was left out.</p> : null}
            </div>
          </Card>
          {result.lines.map((l) =>
            l.trace && l.trace.length > 0 ? (
              <Card key={l.lineNo} title={`Why line ${l.lineNo} has this price`} headingLevel={3}>
                <DataTable label={`Explain line ${l.lineNo}`} columns={traceColumns} rows={traceRows(l.trace)} getRowKey={(r) => `${r.condition}-${r.seq}`} />
              </Card>
            ) : null,
          )}
          {result.trace && result.trace.length > 0 ? (
            <Card title="How the order-level conditions were chosen" headingLevel={3}>
              <DataTable label="Explain order" columns={traceColumns} rows={traceRows(result.trace)} getRowKey={(r) => `${r.condition}-${r.seq}`} />
            </Card>
          ) : null}
        </>
      ) : (
        <Card>
          <EmptyState icon="percent" title="No simulation run yet" description="Run a simulation to see each line's net price and the agreements behind it." headingLevel={3} />
        </Card>
      )}
    </div>
  );
}
