import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, StatGroup, StatTile, Textarea, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  matchSelfBilling,
  type Problem,
  type SelfBillLine,
  type SelfBillLineResult,
  type SelfBillOutcome,
  type SelfBillResult,
  type SelfBillShipment,
} from "./api/client";
import { dash } from "./format";
import { IssueList, ProblemAlert } from "./parts";
import styles from "./pages.module.css";

const OUTCOMES: Record<SelfBillOutcome, { label: string; tone: BadgeTone }> = {
  MATCHED: { label: "Matched", tone: "success" },
  DIFFERENCE: { label: "Difference", tone: "warning" },
  UNMATCHED: { label: "Unmatched", tone: "danger" },
};

const FIELD_LABELS: Record<string, string> = { quantity: "Quantity", unitPrice: "Unit price", tax: "VAT" };

const columns: DataTableColumn<SelfBillLineResult>[] = [
  { key: "line", header: "Customer line", mono: true },
  {
    key: "outcome",
    header: "Outcome",
    render: (row) => {
      const o = OUTCOMES[row.outcome] ?? { label: row.outcome, tone: "neutral" as const };
      return <Badge tone={o.tone}>{o.label}</Badge>;
    },
  },
  { key: "shipmentId", header: "Shipment", mono: true, render: (row) => row.shipmentId ?? dash },
  {
    key: "differences",
    header: "Differences (theirs vs ours)",
    render: (row) =>
      row.differences && row.differences.length > 0 ? row.differences.map((d) => `${FIELD_LABELS[d.field] ?? d.field} ${d.theirs} vs ${d.ours}`).join("; ") : dash,
  },
];

const shipment = (n: number, shippedOn: string): SelfBillShipment => ({
  id: `SHP-${n}`,
  deliveryNote: `DN-${n}`,
  poLine: "1",
  item: "ITEM-A",
  quantity: "2",
  unitPrice: "10.00",
  taxRatePct: "21",
  shippedOn,
});

const EXAMPLE = JSON.stringify(
  {
    shipments: [shipment(1, "2026-09-20"), shipment(2, "2026-09-20"), shipment(3, "2026-09-01")],
    lines: [
      { line: "1", deliveryNote: "DN-1", poLine: "1", item: "ITEM-A", quantity: "2", unitPrice: "10.00", tax: "4.20" },
      { line: "2", deliveryNote: "DN-2", poLine: "1", item: "ITEM-A", quantity: "2", unitPrice: "10.05", tax: "4.22" },
    ] satisfies SelfBillLine[],
  },
  null,
  2,
);

interface Values {
  document: string;
  asOf: string;
  waitDays: string;
  quantityTolerance: string;
  priceTolerance: string;
}

type Errors = Partial<Record<keyof Values, string>>;

/** Client checks before the request: the JSON holds shipments and lines arrays, and the parameters are in range. */
export function validateSelfBilling(values: Values): { errors: Errors; data?: { shipments: SelfBillShipment[]; lines: SelfBillLine[] } } {
  const errors: Errors = {};
  let data: { shipments: SelfBillShipment[]; lines: SelfBillLine[] } | undefined;
  try {
    const parsed = JSON.parse(values.document) as { shipments?: unknown; lines?: unknown } | null;
    if (!parsed || !Array.isArray(parsed.shipments) || !Array.isArray(parsed.lines)) {
      errors.document = "The JSON must be an object with shipments and lines arrays.";
    } else {
      data = { shipments: parsed.shipments as SelfBillShipment[], lines: parsed.lines as SelfBillLine[] };
    }
  } catch (error) {
    errors.document = `The input is not valid JSON: ${error instanceof Error ? error.message : "parse error"}.`;
  }
  if (values.asOf !== "" && !/^\d{4}-\d{2}-\d{2}$/.test(values.asOf)) errors.asOf = "Enter a date as YYYY-MM-DD.";
  const wait = Number(values.waitDays);
  if (!Number.isInteger(wait) || wait < 1 || wait > 120) errors.waitDays = "Enter whole days from 1 to 120.";
  for (const key of ["quantityTolerance", "priceTolerance"] as const) {
    const v = Number(values[key]);
    if (values[key].trim() === "" || !(v >= 0)) errors[key] = "Enter zero or a positive number.";
  }
  return Object.keys(errors).length > 0 ? { errors } : { errors, data };
}

/**
 * Self-billing match (FR-35.16, BR-35.15): match a customer's self-billed lines
 * to the shipments on (delivery note, PO line, item), with the customer's
 * tolerances, and list shipments the customer has not billed after the wait
 * period. Nothing is posted and no dispute is opened.
 */
export function SelfBillingPage() {
  const [values, setValues] = useState<Values>({ document: EXAMPLE, asOf: "2026-10-27", waitDays: "30", quantityTolerance: "0", priceTolerance: "0.01" });
  const [errors, setErrors] = useState<Errors>({});
  const [result, setResult] = useState<SelfBillResult | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const update = (patch: Partial<Values>) => setValues((prev) => ({ ...prev, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, data } = validateSelfBilling(values);
    setErrors(found);
    setFailure(null);
    if (!data) return;
    setBusy(true);
    try {
      setResult(
        await matchSelfBilling({
          ...data,
          asOf: values.asOf || undefined,
          waitDays: Number(values.waitDays),
          tolerance: { quantity: values.quantityTolerance.trim(), price: values.priceTolerance.trim() },
        }),
      );
    } catch (error) {
      setResult(null);
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const count = (o: SelfBillOutcome) => result?.lines.filter((l) => l.outcome === o).length ?? 0;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Self-billing match"
        description="Match a customer's self-billed invoice lines to your shipments by delivery note, PO line and item. Differences beyond the customer's tolerances would go to the dispute worklist. Nothing is posted."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="sb-as-of" label="Match as of" hint="YYYY-MM-DD; unbilled shipments are counted up to this day." error={errors.asOf}>
            {(control) => <Input {...control} type="date" value={values.asOf} onChange={(event) => update({ asOf: event.target.value })} invalid={Boolean(errors.asOf)} />}
          </FormField>
          <FormField id="sb-wait" label="Wait days" hint="Days after shipping before an unbilled shipment is raised (1 to 120)." error={errors.waitDays}>
            {(control) => <Input {...control} inputMode="numeric" value={values.waitDays} onChange={(event) => update({ waitDays: event.target.value })} invalid={Boolean(errors.waitDays)} />}
          </FormField>
          <FormField id="sb-qty-tol" label="Quantity tolerance" error={errors.quantityTolerance}>
            {(control) => (
              <Input {...control} inputMode="decimal" value={values.quantityTolerance} onChange={(event) => update({ quantityTolerance: event.target.value })} invalid={Boolean(errors.quantityTolerance)} />
            )}
          </FormField>
          <FormField id="sb-price-tol" label="Price tolerance per unit" error={errors.priceTolerance}>
            {(control) => (
              <Input {...control} inputMode="decimal" value={values.priceTolerance} onChange={(event) => update({ priceTolerance: event.target.value })} invalid={Boolean(errors.priceTolerance)} />
            )}
          </FormField>
          <div className={styles.wide}>
            <FormField
              id="sb-document"
              label="Shipments and self-billed lines (JSON)"
              hint="Prefilled with an example. Quantities, prices, VAT rates and VAT amounts are decimal strings; shippedOn is YYYY-MM-DD."
              error={errors.document}
            >
              {(control) => <Textarea {...control} mono rows={16} value={values.document} onChange={(event) => update({ document: event.target.value })} invalid={Boolean(errors.document)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run match
            </Button>
            <Button type="button" variant="ghost" onClick={() => update({ document: EXAMPLE })}>
              Reset to example
            </Button>
          </div>
        </form>
      </Card>
      <ProblemAlert problem={failure} title="The document could not be matched" />
      {result ? (
        <Card title="Result" headingLevel={2}>
          <div className={styles.stack}>
            <StatGroup label="Match summary">
              <StatTile label="Matched" value={count("MATCHED")} />
              <StatTile label="Differences" value={count("DIFFERENCE")} tone="warning" />
              <StatTile label="Unmatched" value={count("UNMATCHED")} tone="danger" />
              <StatTile label="Unbilled shipments" value={result.unbilled.length} tone="warning" hint={`after ${values.waitDays} days`} />
            </StatGroup>
            <DataTable
              label="Self-billed lines"
              columns={columns}
              rows={result.lines}
              getRowKey={(row, i) => `${row.line}-${i}`}
              empty={{ title: "No self-billed lines", description: "The document has no lines; only unbilled shipments are checked." }}
            />
            {result.unbilled.length > 0 ? (
              <Alert tone="warning" title="Not self-billed yet" live="off">
                <ul className={styles.plainList} aria-label="Unbilled shipments">
                  {result.unbilled.map((id) => (
                    <li key={id} className={styles.mono}>
                      {id}
                    </li>
                  ))}
                </ul>
              </Alert>
            ) : null}
            <IssueList issues={result.issues} label="Match findings" />
          </div>
        </Card>
      ) : null}
    </div>
  );
}
