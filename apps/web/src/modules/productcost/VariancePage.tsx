import { useState, type FormEvent } from "react";
import {
  Alert,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  StatGroup,
  StatTile,
  type DataTableColumn,
} from "../../shell/ui";
import {
  computeVariance,
  describeProblem,
  type Decomposition,
  type Order,
  type OrderLine,
  type Problem,
  type VarianceTypes,
} from "./api/client";
import { amount, isDecimal, isNegative, isPositive, isSignedDecimal } from "./format";
import { LineGrid, type LineField } from "./LineGrid";
import styles from "./pages.module.css";

type LineKey = "component" | "kind" | "plannedQty" | "stdUnitCost" | "estPerLot" | "actualQty" | "actualUnitCost";
type LineValues = Record<LineKey, string>;

interface Values {
  currency: string;
  plannedQty: string;
  rafQty: string;
  lines: LineValues[];
}

const lineFields: LineField<LineKey>[] = [
  { name: "component", label: "Cost component", mono: true },
  {
    name: "kind",
    label: "Line type",
    options: [
      { value: "planned", label: "Planned (variable)" },
      { value: "fixed", label: "Planned, fixed per lot (setup)" },
      { value: "unplanned", label: "Not in the estimate (substitute)" },
    ],
  },
  { name: "plannedQty", label: "Planned quantity for the order", decimal: true },
  { name: "stdUnitCost", label: "Standard unit cost", decimal: true },
  { name: "estPerLot", label: "Estimate per lot", decimal: true },
  { name: "actualQty", label: "Actual quantity", decimal: true },
  { name: "actualUnitCost", label: "Actual unit cost", decimal: true },
];

const emptyLine = (): LineValues => ({
  component: "DM",
  kind: "planned",
  plannedQty: "",
  stdUnitCost: "",
  estPerLot: "",
  actualQty: "",
  actualUnitCost: "",
});

/** AC-39.5: 100 planned, 90 finished, 290 consumed of a 3-per-unit component, a substitute part and one setup. */
export const ac395: Values = {
  currency: "USD",
  plannedQty: "100",
  rafQty: "90",
  lines: [
    { component: "DM", kind: "planned", plannedQty: "300", stdUnitCost: "2.00", estPerLot: "", actualQty: "290", actualUnitCost: "2.00" },
    { component: "DM", kind: "unplanned", plannedQty: "", stdUnitCost: "", estPerLot: "", actualQty: "5", actualUnitCost: "1.50" },
    { component: "DL", kind: "fixed", plannedQty: "", stdUnitCost: "0.40", estPerLot: "40.00", actualQty: "1", actualUnitCost: "40.00" },
  ],
};

function applies(line: LineValues, field: LineKey): boolean {
  switch (field) {
    case "plannedQty":
      return line.kind === "planned";
    case "stdUnitCost":
      return line.kind !== "unplanned";
    case "estPerLot":
      return line.kind === "fixed";
    default:
      return true;
  }
}

/** Checks that mirror the service, and the order the form sends. */
export function validateOrder(values: Values): { errors: Record<string, string>; order?: Order } {
  const errors: Record<string, string> = {};
  const currency = values.currency.trim().toUpperCase();
  if (currency !== "" && !/^[A-Z]{3}$/.test(currency))
    errors.currency = "Enter a three-letter currency code such as USD, or leave it empty.";
  if (!isPositive(values.plannedQty)) errors.plannedQty = "Enter the order quantity, above 0.";
  if (!isDecimal(values.rafQty)) errors.rafQty = "Enter the quantity reported as finished, 0 or more.";
  if (values.lines.length === 0) errors.lines = "Add at least one line.";
  const lines: OrderLine[] = values.lines.map((ln, i) => {
    const at = (k: LineKey) => `lines[${i}].${k}`;
    if (ln.component.trim() === "") errors[at("component")] = "Enter the cost component, such as DM.";
    for (const k of ["plannedQty", "stdUnitCost", "estPerLot", "actualUnitCost"] as const) {
      if (applies(ln, k) && !isDecimal(ln[k])) errors[at(k)] = "Enter a number, 0 or more.";
    }
    if (!isSignedDecimal(ln.actualQty)) errors[at("actualQty")] = "Enter a number; 0 when nothing was consumed.";
    const line: OrderLine = {
      component: ln.component.trim(),
      planned: ln.kind !== "unplanned",
      actualQty: ln.actualQty.trim(),
      actualUnitCost: ln.actualUnitCost.trim(),
    };
    if (ln.kind === "fixed") {
      line.fixed = true;
      line.estPerLot = ln.estPerLot.trim();
    }
    if (ln.kind === "planned") line.plannedQty = ln.plannedQty.trim();
    if (ln.kind !== "unplanned") line.stdUnitCost = ln.stdUnitCost.trim();
    return line;
  });
  if (Object.keys(errors).length > 0) return { errors };
  const order: Order = { plannedQty: values.plannedQty.trim(), rafQty: values.rafQty.trim(), lines };
  if (currency !== "") order.currency = currency;
  return { errors, order };
}

interface Row {
  component: string;
  total: string;
  types: VarianceTypes;
}

function signed(value: string) {
  return (
    <span className={isNegative(value) ? styles.favourable : isPositive(value) ? styles.unfavourable : undefined}>{amount(value)}</span>
  );
}

const columns: DataTableColumn<Row>[] = [
  { key: "component", header: "Component", mono: true },
  { key: "lotSize", header: "Lot size", align: "end", render: (r) => signed(r.types.lotSize) },
  { key: "quantity", header: "Quantity", align: "end", render: (r) => signed(r.types.quantity) },
  { key: "price", header: "Price", align: "end", render: (r) => signed(r.types.price) },
  { key: "substitution", header: "Substitution", align: "end", render: (r) => signed(r.types.substitution) },
  { key: "total", header: "Total", align: "end", render: (r) => signed(r.total) },
];

/** Decompose an ended production order's variance by type and cost component (FR-39.8). */
export function VariancePage() {
  const [values, setValues] = useState<Values>(ac395);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<Decomposition | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, order } = validateOrder(values);
    setErrors(found);
    setFailure(null);
    if (!order) return;
    setBusy(true);
    try {
      setResult(await computeVariance(order));
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  const field = (id: "currency" | "plannedQty" | "rafQty", label: string, hint: string, optional?: boolean) => (
    <FormField id={`variance-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input
          {...control}
          inputMode={id === "currency" ? undefined : "decimal"}
          value={values[id]}
          onChange={(e) => setValues({ ...values, [id]: e.target.value })}
          invalid={Boolean(errors[id])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Production variances"
        description="Enter an ended order's planned and actual consumption. Unfavourable variances are positive. The lot size, quantity, price and substitution variances add up to the total to the cent; any rounding residual goes to the quantity variance."
      />
      <Card title="Ended order" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
          {field("plannedQty", "Order quantity", "Q order, the quantity the order was estimated for.")}
          {field("rafQty", "Reported as finished", "Q RAF, the good output; standard cost is earned on this quantity.")}
          {field("currency", "Currency", "Sets the posting precision: two decimals by default, none for JPY.", true)}
          <LineGrid
            idPrefix="variance-line"
            noun="Line"
            fields={lineFields}
            lines={values.lines}
            errors={errors}
            newLine={emptyLine}
            applies={applies}
            onChange={(lines) => setValues({ ...values, lines })}
          />
          {failure ? (
            <div className={styles.formActions}>
              <Alert
                tone="danger"
                title={failure.code === "CST-0720" ? "The variance does not close" : "The variance was not computed"}
                live="alert"
              >
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Compute variance
            </Button>
            <Button variant="ghost" onClick={() => setValues(ac395)}>
              Load AC-39.5 example
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Variance by component" columns={columns} rows={[]} getRowKey={(r) => r.component} loading />
      ) : result ? (
        <>
          <StatGroup label="Order variance">
            <StatTile label="Total variance" value={amount(result.total)} />
            <StatTile label="Lot size" value={amount(result.types.lotSize)} />
            <StatTile label="Quantity" value={amount(result.types.quantity)} />
            <StatTile label="Price" value={amount(result.types.price)} />
            <StatTile label="Substitution" value={amount(result.types.substitution)} />
          </StatGroup>
          <DataTable
            label="Variance by component"
            columns={columns}
            rows={result.byComponent}
            getRowKey={(r) => r.component}
            empty={{ title: "No variance", description: "The order consumed exactly its standard." }}
          />
          <p className={styles.note}>Rounding residual assigned to quantity variance: {amount(result.residual)}</p>
        </>
      ) : (
        <Card>
          <EmptyState
            icon="percent"
            headingLevel={3}
            title="No variance computed yet"
            description="Compute the variance to see it split by type and cost component."
          />
        </Card>
      )}
    </div>
  );
}
