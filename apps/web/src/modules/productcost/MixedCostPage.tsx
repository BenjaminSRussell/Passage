import { useState, type FormEvent } from "react";
import { Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { describeProblem, mixedCost, type Alternative, type AlternativeRoll, type CostItem, type MixedRoll, type Problem } from "./api/client";
import { amount, dash, isDecimal } from "./format";
import { LineGrid, type LineField } from "./LineGrid";
import { parseItems } from "./RollPage";
import { DiagnosticsTable, ItemsField, ProblemAlert, RollStatus, SplitTable, itemsText, sumDecimals } from "./shared";
import styles from "./pages.module.css";

type AltKey = keyof Alternative;
type AltValues = Record<AltKey, string>;

interface Values {
  items: string;
  itemId: string;
  alternatives: AltValues[];
  fallbackToActive: boolean;
}

/** AC-39.11: vendor A at 10.00 for 60% and in-house production at 12.50 for 40%. */
export const mixedExample: CostItem[] = [
  { id: "BUY-A", kind: "Purchased", price: "10.00" },
  { id: "RM-500", kind: "Purchased", price: "12.50" },
  { id: "MAKE", kind: "Manufactured", costingLot: "1", bom: [{ childId: "RM-500", qty: "1" }] },
];

const initial: Values = {
  items: itemsText(mixedExample),
  itemId: "FG-MIX",
  alternatives: [
    { source: "VENDOR-A", percent: "60", itemId: "BUY-A" },
    { source: "IN-HOUSE", percent: "40", itemId: "MAKE" },
  ],
  fallbackToActive: false,
};

const altFields: LineField<AltKey>[] = [
  { name: "source", label: "Source", mono: true },
  { name: "percent", label: "Ratio (%)", decimal: true },
  { name: "itemId", label: "Item rolled", mono: true },
];

const emptyAlt = (): AltValues => ({ source: "", percent: "", itemId: "" });

type Errors = Record<string, string>;

/** Checks that mirror the service (BR-39.12) and the request the form sends. */
export function validateMixed(values: Values): { errors: Errors; items?: CostItem[]; alternatives?: Alternative[] } {
  const errors: Errors = {};
  const { items, error } = parseItems(values.items);
  if (error) errors.items = error;
  const itemId = values.itemId.trim();
  if (itemId === "") errors.itemId = "Enter the id of the mixed item.";
  if (values.alternatives.length === 0) errors.alternatives = "Add at least one alternative.";
  const seen = new Set<string>();
  const alternatives = values.alternatives.map((a, i) => {
    const at = (k: AltKey) => `alternatives[${i}].${k}`;
    const source = a.source.trim();
    if (source === "") errors[at("source")] = "Enter the source.";
    else if (seen.has(source)) errors[at("source")] = "Each source appears once.";
    seen.add(source);
    if (!isDecimal(a.percent)) errors[at("percent")] = "Enter a ratio from 0 to 100.";
    const alt = a.itemId.trim();
    if (alt === "") errors[at("itemId")] = "Enter the item rolled for this source.";
    else if (alt === itemId) errors[at("itemId")] = "An alternative cannot be the mixed item itself.";
    else if (items && !items.some((it) => it.id === alt)) errors[at("itemId")] = "The item must be one of the items above.";
    return { source, percent: a.percent.trim(), itemId: alt };
  });
  const total = sumDecimals(alternatives.map((a) => a.percent));
  if (total !== undefined && alternatives.length > 0 && total !== "100") {
    errors.alternatives = `The ratios total ${total}%; they must total 100% (CST-0741).`;
  }
  if (Object.keys(errors).length > 0 || !items) return { errors };
  return { errors, items, alternatives };
}

const altColumns: DataTableColumn<AlternativeRoll>[] = [
  { key: "source", header: "Source", mono: true },
  { key: "percent", header: "Ratio (%)", align: "end", render: (a) => amount(a.percent) },
  { key: "itemId", header: "Item rolled", mono: true },
  { key: "status", header: "Status", render: (a) => <RollStatus status={a.status} /> },
  { key: "unitCost", header: "Unit cost", align: "end", render: (a) => (a.status === "Pending" ? amount(a.unitCost) : dash) },
];

/** Cost an item supplied from several procurement alternatives by mixing ratio (FR-39.14). */
export function MixedCostPage() {
  const [values, setValues] = useState<Values>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<MixedRoll | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, items, alternatives } = validateMixed(values);
    setErrors(found);
    setFailure(null);
    if (!items || !alternatives) return;
    setBusy(true);
    try {
      setResult(
        await mixedCost({ items, itemId: values.itemId.trim(), alternatives, options: { fallbackToActive: values.fallbackToActive } }),
      );
    } catch (err) {
      const problem = describeProblem(err);
      setErrors(problem.fields);
      setFailure(problem);
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  const diagnostics = result?.diagnostics ?? [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Mixed cost"
        description="Each procurement alternative is rolled with its own structure and weighted by its mixing ratio; the cost component split is weighted the same way. Ratios must total 100%; a 0% alternative is shown but left out. Stored alternatives per item, site and version arrive with costing versions."
      />
      <Card title="Item and alternatives" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
          <ItemsField
            id="mixed-items"
            label="Items"
            value={values.items}
            error={errors.items}
            onChange={(items) => setValues({ ...values, items })}
          />
          <FormField id="mixed-item" label="Mixed item" hint="The item whose cost is mixed." error={errors.itemId}>
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={values.itemId}
                onChange={(e) => setValues({ ...values, itemId: e.target.value })}
                invalid={Boolean(errors.itemId)}
              />
            )}
          </FormField>
          <div className={styles.check}>
            <Input
              id="mixed-fallback"
              type="checkbox"
              checked={values.fallbackToActive}
              onChange={(e) => setValues({ ...values, fallbackToActive: e.target.checked })}
            />
            <label htmlFor="mixed-fallback">Fall back to the active standard</label>
          </div>
          <LineGrid
            idPrefix="mixed-alt"
            noun="Alternative"
            path="alternatives"
            fields={altFields}
            lines={values.alternatives}
            errors={errors}
            newLine={emptyAlt}
            onChange={(alternatives) => setValues({ ...values, alternatives })}
          />
          {failure ? <ProblemAlert title="The mixed cost was not computed" problem={failure} /> : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Compute mixed cost
            </Button>
            <Button variant="ghost" onClick={() => setValues(initial)}>
              Load example
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Alternatives" columns={altColumns} rows={[]} getRowKey={(a) => a.source} loading />
      ) : result ? (
        <Card title={`Mixed cost of ${result.itemId}`} headingLevel={3}>
          <div className={styles.stack}>
            <StatGroup label="Mixed cost">
              <StatTile label="Unit cost" value={result.status === "Error" ? dash : amount(result.unitCost)} />
              <StatTile label="Alternatives" value={result.alternatives.length} />
              <StatTile label="Diagnostics" value={diagnostics.length} tone={result.status === "Error" ? "danger" : "neutral"} />
            </StatGroup>
            <p className={styles.note}>
              Status: <RollStatus status={result.status} />
            </p>
            <DataTable
              label="Alternatives"
              columns={altColumns}
              rows={result.alternatives}
              getRowKey={(a) => a.source}
              empty={{ title: "No alternatives", description: "The item has no procurement alternatives." }}
            />
            {result.status === "Error" ? null : <SplitTable label={`Weighted split of ${result.itemId}`} split={result.split} />}
            {diagnostics.length > 0 ? <DiagnosticsTable label={`Diagnostics of ${result.itemId}`} diagnostics={diagnostics} /> : null}
          </div>
        </Card>
      ) : (
        <Card>
          <EmptyState
            icon="percent"
            headingLevel={3}
            title="No mixed cost yet"
            description="Enter the alternatives with their mixing ratios to see the weighted cost and split."
          />
        </Card>
      )}
    </div>
  );
}
