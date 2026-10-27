import { useState, type FormEvent } from "react";
import { Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { describeProblem, salesOrderEstimate, type CostItem, type ExplosionMode, type Problem, type SalesEstimate } from "./api/client";
import { amount, dash, isPositive } from "./format";
import { parseItems } from "./RollPage";
import { DiagnosticsTable, ExplosionModeField, ItemsField, ProblemAlert, RollStatus, SplitTable, itemsText } from "./shared";
import styles from "./pages.module.css";

interface Values {
  currency: string;
  items: string;
  itemId: string;
  quantity: string;
  netAmount: string;
  mode: ExplosionMode;
  fallbackToActive: boolean;
}

/** AC-39.13: a configured line priced 2,400.00 whose configuration rolls to 1,860.00. */
export const configuredLine: CostItem[] = [
  { id: "RM-FRAME", kind: "Purchased", price: "1500.00" },
  {
    id: "CFG-LINE",
    kind: "Manufactured",
    costingLot: "1",
    bom: [{ childId: "RM-FRAME", qty: "1" }],
    operations: [{ runHours: "6", rate: "60" }],
  },
];

const initial: Values = {
  currency: "USD",
  items: itemsText(configuredLine),
  itemId: "CFG-LINE",
  quantity: "1",
  netAmount: "2400.00",
  mode: "Multilevel",
  fallbackToActive: false,
};

type Errors = Record<string, string>;

interface EstimateRequest {
  currency?: string;
  items: CostItem[];
  itemId: string;
  quantity: string;
  netAmount: string;
  options: { mode: ExplosionMode; fallbackToActive: boolean };
}

/** Checks that mirror the service, and the request the form sends. */
export function validateEstimate(values: Values): { errors: Errors; request?: EstimateRequest } {
  const errors: Errors = {};
  const currency = values.currency.trim().toUpperCase();
  if (currency !== "" && !/^[A-Z]{3}$/.test(currency))
    errors.currency = "Enter a three-letter currency code such as USD, or leave it empty.";
  const { items, error } = parseItems(values.items);
  if (error) errors.items = error;
  const itemId = values.itemId.trim();
  if (itemId === "") errors.itemId = "Enter the id of the line's item.";
  else if (items && !items.some((it) => it.id === itemId)) errors.itemId = "The item must be one of the items above.";
  if (!isPositive(values.quantity)) errors.quantity = "Enter the order quantity, above 0.";
  if (!isPositive(values.netAmount)) errors.netAmount = "Enter the net line amount, above 0.";
  if (Object.keys(errors).length > 0 || !items) return { errors };
  return {
    errors,
    request: {
      currency: currency || undefined,
      items,
      itemId,
      quantity: values.quantity.trim(),
      netAmount: values.netAmount.trim(),
      options: { mode: values.mode, fallbackToActive: values.fallbackToActive },
    },
  };
}

const placeholderColumns: DataTableColumn<{ id: string }>[] = [{ key: "id", header: "Estimate" }];

/** Cost a configured or make-to-order sales order line at its order quantity and show the margin (FR-39.16). */
export function SalesEstimatePage() {
  const [values, setValues] = useState<Values>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [estimate, setEstimate] = useState<SalesEstimate | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, request } = validateEstimate(values);
    setErrors(found);
    setFailure(null);
    if (!request) return;
    setBusy(true);
    try {
      setEstimate(await salesOrderEstimate(request));
    } catch (err) {
      const problem = describeProblem(err);
      setErrors(problem.fields);
      setFailure(problem);
      setEstimate(null);
    } finally {
      setBusy(false);
    }
  }

  const diagnostics = estimate?.diagnostics ?? [];
  const failed = estimate?.status === "Error";

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Sales order estimate"
        description="The line's order-specific structure is rolled at the order quantity, so setup is spread over the order. Margin is the net line amount less the estimated cost. Storing the estimate against the line and freezing it at confirmation arrive with stored calculations."
      />
      <Card title="Sales order line" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
          <ItemsField
            id="estimate-items"
            label="Order structure"
            value={values.items}
            error={errors.items}
            onChange={(items) => setValues({ ...values, items })}
          />
          <FormField id="estimate-item" label="Line item" hint="The configured or make-to-order item on the line." error={errors.itemId}>
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
          <FormField id="estimate-quantity" label="Order quantity" error={errors.quantity}>
            {(control) => (
              <Input
                {...control}
                inputMode="decimal"
                value={values.quantity}
                onChange={(e) => setValues({ ...values, quantity: e.target.value })}
                invalid={Boolean(errors.quantity)}
              />
            )}
          </FormField>
          <FormField id="estimate-net" label="Net line amount" error={errors.netAmount}>
            {(control) => (
              <Input
                {...control}
                inputMode="decimal"
                value={values.netAmount}
                onChange={(e) => setValues({ ...values, netAmount: e.target.value })}
                invalid={Boolean(errors.netAmount)}
              />
            )}
          </FormField>
          <FormField
            id="estimate-currency"
            label="Currency"
            hint="Sets the rounding of the cost: two decimals by default, none for JPY."
            optional
            error={errors.currency}
          >
            {(control) => (
              <Input
                {...control}
                value={values.currency}
                onChange={(e) => setValues({ ...values, currency: e.target.value })}
                invalid={Boolean(errors.currency)}
              />
            )}
          </FormField>
          <ExplosionModeField id="estimate-mode" value={values.mode} onChange={(mode) => setValues({ ...values, mode })} />
          <div className={styles.check}>
            <Input
              id="estimate-fallback"
              type="checkbox"
              checked={values.fallbackToActive}
              onChange={(e) => setValues({ ...values, fallbackToActive: e.target.checked })}
            />
            <label htmlFor="estimate-fallback">Fall back to the active standard</label>
          </div>
          {failure ? <ProblemAlert title="The line was not costed" problem={failure} /> : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Cost the line
            </Button>
            <Button variant="ghost" onClick={() => setValues(initial)}>
              Load example
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Sales order estimate" columns={placeholderColumns} rows={[]} getRowKey={(r) => r.id} loading />
      ) : estimate ? (
        <Card title={`Estimate for ${estimate.itemId}`} headingLevel={3}>
          <div className={styles.stack}>
            <StatGroup label="Line margin">
              <StatTile label="Estimated cost" value={failed ? dash : amount(estimate.cost)} />
              <StatTile label="Net amount" value={amount(estimate.netAmount)} />
              <StatTile label="Margin" value={failed ? dash : amount(estimate.margin)} />
              <StatTile label="Margin %" value={failed ? dash : amount(estimate.marginPct)} />
            </StatGroup>
            <p className={styles.note}>
              Calculation type {estimate.calculationType}, quantity {estimate.quantity} at {failed ? dash : amount(estimate.unitCost)} per
              unit. Status: <RollStatus status={estimate.status} />
            </p>
            {failed ? null : <SplitTable label={`Cost by component of ${estimate.itemId}`} split={estimate.split} />}
            {diagnostics.length > 0 ? <DiagnosticsTable label={`Diagnostics of ${estimate.itemId}`} diagnostics={diagnostics} /> : null}
          </div>
        </Card>
      ) : (
        <Card>
          <EmptyState
            icon="receipt"
            headingLevel={3}
            title="No estimate yet"
            description="Enter the line's structure, quantity and net amount to see its estimated cost by component and its margin."
          />
        </Card>
      )}
    </div>
  );
}
