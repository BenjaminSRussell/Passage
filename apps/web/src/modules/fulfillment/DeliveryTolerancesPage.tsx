import { useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Button, Card, FormField, Input, type DataTableColumn, ListPage, loadedRows } from "../../shell/ui";
import { describeProblem, listDeliveryTolerances, saveDeliveryTolerance, type DeliveryTolerance, type Problem } from "./api/client";
import { dash, formatDateTime, PERCENT, UUID } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { EntityName } from "../../shell/lookup/EntityName";
import { PlainName } from "../sales/OrderNumber";

/** Saving a tolerance needs this duty (the service enforces it); reading needs fulfillment.warehouse.view. */
const DUTY_SAVE = "fulfillment.packing-rule.maintain";

const columns: DataTableColumn<DeliveryTolerance>[] = [
  { key: "productId", header: "Product", render: (row) => (row.productId ? <PlainName short entity="product" id={row.productId} /> : "All products") },
  { key: "customerId", header: "Customer", render: (row) => (row.customerId ? <EntityName entity="customer" id={row.customerId} /> : "All customers") },
  { key: "overdeliveryPct", header: "Over-delivery %", align: "end" },
  { key: "updatedAt", header: "Saved", render: (row) => (row.updatedAt ? formatDateTime(row.updatedAt) : dash) },
];

interface Values {
  productId: string;
  customerId: string;
  overdeliveryPct: string;
}

const blank: Values = { productId: "", customerId: "", overdeliveryPct: "" };

/** Validates the form; the service enforces the same rules (BR-28.7). */
export function validateTolerance(values: Values): Partial<Record<keyof Values, string>> {
  const errors: Partial<Record<keyof Values, string>> = {};
  if (values.productId.trim() !== "" && !UUID.test(values.productId.trim())) errors.productId = "Enter the product id (a UUID), or leave it empty for every product.";
  if (values.customerId.trim() !== "" && !UUID.test(values.customerId.trim())) errors.customerId = "Enter the customer id (a UUID), or leave it empty for every customer.";
  const pct = values.overdeliveryPct.trim();
  if (!PERCENT.test(pct) || Number(pct) > 100) errors.overdeliveryPct = "Enter a percentage from 0 to 100 with at most 4 decimals.";
  return errors;
}

/**
 * Over-delivery tolerances (BR-28.7, FUL-0005): how far above the planned quantity a pick may go,
 * per product and customer. The most specific scope applies; with none a pick above plan is refused.
 */
export function DeliveryTolerancesPage() {
  const state = useLoad((signal) => listDeliveryTolerances(signal), "delivery-tolerances");
  const [values, setValues] = useState<Values>(blank);
  const [version, setVersion] = useState(0);
  const [errors, setErrors] = useState<Partial<Record<keyof Values, string>>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();
  const canSave = holdsDuty(usePrincipalDuties(), DUTY_SAVE);

  /** The saved version of the scope the form names, 0 when the scope has none. */
  function versionOf(next: Values): number {
    const product = next.productId.trim().toLowerCase() || null;
    const customer = next.customerId.trim().toLowerCase() || null;
    return state.data?.find((t) => t.productId === product && t.customerId === customer)?.version ?? 0;
  }

  function update(change: Partial<Values>) {
    const next = { ...values, ...change };
    setValues(next);
    setVersion(versionOf(next));
    key.renew();
  }

  function open(row: DeliveryTolerance) {
    setValues({ productId: row.productId ?? "", customerId: row.customerId ?? "", overdeliveryPct: row.overdeliveryPct });
    setVersion(row.version);
    setErrors({});
    setNotice(null);
    setFailure(null);
    key.renew();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setNotice(null);
    const found = validateTolerance(values);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const saved = await saveDeliveryTolerance(
        { productId: values.productId.trim() || null, customerId: values.customerId.trim() || null, overdeliveryPct: values.overdeliveryPct.trim() },
        version,
        key.current(),
      );
      key.renew();
      setVersion(saved.version);
      setNotice(`Saved: a pick may exceed the plan by up to ${saved.overdeliveryPct}%.`);
      state.reload();
    } catch (failed) {
      const problem = describeProblem(failed);
      setFailure(problem.code === "conflict_version" ? { ...problem, message: "Someone else changed this tolerance. Reload the list and open the row again." } : problem);
      key.renew();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <ListPage
        id="fulfillment-delivery-tolerances"
        title="Over-delivery tolerances"
        description="How far above the planned quantity a pick may go. The most specific row applies: product and customer, then product, then customer, then the general row. Without a row, a pick above plan is refused (FUL-0004); above the tolerance it is refused (FUL-0005)."
        noun="over-delivery tolerances"
        columns={columns}
        useRows={() => loadedRows(state)}
        getRowKey={(row) => row.id}
        onRowOpen={canSave ? open : undefined}
        empty={{ title: "No tolerances yet", description: canSave ? "Every pick above plan is refused until you add one below." : "Every pick above plan is refused until one is added." }}
      />
      {canSave ? (
        <Card title="Add or change a tolerance">
          <form className={styles.form} onSubmit={submit} noValidate>
            <p className={styles.note}>One row per product and customer: saving the same product and customer replaces the row. Open a row above to edit it.</p>
            <FormField id="tol-product" label="Product" hint="Empty: every product." error={errors.productId} optional>
              {(control) => <EntityLookup entity="product" {...control} value={values.productId} invalid={Boolean(errors.productId)} onChange={(event) => update({ productId: event.target.value })} />}
            </FormField>
            <FormField id="tol-customer" label="Customer" hint="Empty: every customer." error={errors.customerId} optional>
              {(control) => <EntityLookup entity="customer" {...control} value={values.customerId} invalid={Boolean(errors.customerId)} onChange={(event) => update({ customerId: event.target.value })} />}
            </FormField>
            <FormField id="tol-pct" label="Over-delivery (%)" hint="Percent of the planned quantity, 0 to 100. 0 allows no excess." error={errors.overdeliveryPct}>
              {(control) => <Input {...control} inputMode="decimal" value={values.overdeliveryPct} invalid={Boolean(errors.overdeliveryPct)} onChange={(event) => update({ overdeliveryPct: event.target.value })} />}
            </FormField>
            <ProblemAlert title="Could not save the tolerance" problem={failure} />
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Save tolerance
              </Button>
              {notice ? (
                <p role="status" className={styles.note}>
                  {notice}
                </p>
              ) : null}
            </div>
          </form>
        </Card>
      ) : (
        <p className={styles.note}>Changing a tolerance needs the fulfillment.packing-rule.maintain duty.</p>
      )}
    </div>
  );
}
