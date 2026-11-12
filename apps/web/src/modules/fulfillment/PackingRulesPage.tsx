import { useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, ListPage, loadedRows, Select, type DataTableColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { describeProblem, listContainerTypes, listPackingRules, savePackingRule, type ContainerType, type PackingRule, type Problem } from "./api/client";
import { compareDecimal, DECIMAL, UUID } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { EntityName } from "../../shell/lookup/EntityName";
import { PlainName } from "../sales/OrderNumber";

const columns: DataTableColumn<PackingRule>[] = [
  { key: "productId", header: "Product", render: (row) => <PlainName short entity="product" id={row.productId} /> },
  { key: "customerId", header: "Customer", render: (row) => (row.customerId ? <EntityName entity="customer" id={row.customerId} /> : "All customers") },
  { key: "containerType", header: "Container type", mono: true },
  { key: "qtyPerContainer", header: "Per container", align: "end" },
  { key: "maxWeightKg", header: "Max kg", align: "end" },
];

interface Values {
  productId: string;
  customerId: string;
  containerType: string;
  qtyPerContainer: string;
  maxWeightKg: string;
}

const blank: Values = { productId: "", customerId: "", containerType: "", qtyPerContainer: "", maxWeightKg: "" };

const ruleFilters: ListFilterDef[] = [{ kind: "entity", key: "productId", label: "Product", entity: "product" }];

/** Packing rules (FR-28.15): product, optionally for one customer, to a container type and its limits. */
export function PackingRulesPage() {
  // Bumped after a save, so the list reads the saved rule.
  const [reloads, setReloads] = useState(0);
  const typeState = useLoad<ContainerType[]>((signal) => listContainerTypes(signal), "container-types");
  const useRuleRows = (query: ListQuery): ListData<PackingRule> => {
    const filter = query.filters.productId ?? "";
    const state = useLoad<PackingRule[]>((signal) => listPackingRules({ productId: filter }, signal), `${filter}-${reloads}`);
    return loadedRows(state);
  };
  const [values, setValues] = useState<Values>(blank);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();
  const types = typeState.data ?? [];

  function set(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.renew();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!UUID.test(values.productId.trim())) found.productId = "Enter the product id (a UUID).";
    if (values.customerId.trim() !== "" && !UUID.test(values.customerId.trim())) found.customerId = "Enter the customer id (a UUID), or leave it empty for every customer.";
    if (values.containerType === "") found.containerType = "Choose the container type.";
    if (!DECIMAL.test(values.qtyPerContainer.trim()) || compareDecimal(values.qtyPerContainer.trim(), "0") === 0) found.qtyPerContainer = "Enter how many units fit in one container, more than 0.";
    if (!DECIMAL.test(values.maxWeightKg.trim()) || compareDecimal(values.maxWeightKg.trim(), "0") === 0) found.maxWeightKg = "Enter the max gross weight in kg, more than 0.";
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const saved = await savePackingRule(
        {
          productId: values.productId.trim(),
          customerId: values.customerId.trim() || null,
          containerType: values.containerType,
          qtyPerContainer: values.qtyPerContainer.trim(),
          maxWeightKg: values.maxWeightKg.trim(),
        },
        key.current(),
      );
      key.renew();
      setValues(blank);
      setNotice(`Saved: ${saved.qtyPerContainer} per ${saved.containerType}, at most ${saved.maxWeightKg} kg.`);
      setReloads((n) => n + 1);
    } catch (error) {
      setFailure(describeProblem(error));
      key.renew();
    } finally {
      setBusy(false);
    }
  }

  return (
    <ListPage
      id="fulfillment-packing-rules"
      title="Packing rules"
      description="Auto-pack uses the customer's rule for a product when there is one, otherwise the product's general rule. Lines without a rule stay unpacked. Open a rule to change it below."
      actions={
        <Link className={buttonClassName("secondary")} to="/fulfillment/container-types">
          Container types
        </Link>
      }
      noun="packing rules"
      singular="packing rule"
      columns={columns}
      filters={ruleFilters}
      useRows={useRuleRows}
      getRowKey={(row) => row.id}
      onRowOpen={(row) => {
        setValues({ productId: row.productId, customerId: row.customerId ?? "", containerType: row.containerType, qtyPerContainer: row.qtyPerContainer, maxWeightKg: row.maxWeightKg });
        key.renew();
      }}
      empty={{ title: "No packing rules yet", description: "Add a rule per product so auto-pack can fill containers." }}
      footer={
      <Card title="Add or change a packing rule">
        <form className={styles.form} onSubmit={submit} noValidate>
          <p className={styles.note}>One rule per product and customer: saving the same product and customer replaces the rule. Open a row above to edit it.</p>
          {typeState.data && types.length === 0 ? (
            <div className={styles.formActions}>
              <Alert tone="info" title="Add a container type first">
                A rule names a container type. <Link to="/fulfillment/container-types">Add container types</Link>.
              </Alert>
            </div>
          ) : null}
          <FormField id="rule-product" label="Product" error={errors.productId}>
            {(control) => <EntityLookup entity="product" {...control} value={values.productId} invalid={Boolean(errors.productId)} onChange={(event) => set({ productId: event.target.value })} />}
          </FormField>
          <FormField id="rule-customer" label="Customer" hint="Empty: the rule applies to every customer." error={errors.customerId} optional>
            {(control) => <EntityLookup entity="customer" {...control} value={values.customerId} invalid={Boolean(errors.customerId)} onChange={(event) => set({ customerId: event.target.value })} />}
          </FormField>
          <FormField id="rule-type" label="Container type" error={errors.containerType}>
            {(control) => (
              <Select {...control} value={values.containerType} onChange={(event) => set({ containerType: event.target.value })}>
                <option value="">Choose a container type</option>
                {types.map((t) => (
                  <option key={t.code} value={t.code}>
                    {t.code} ({t.tareKg} kg tare)
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="rule-qty" label="Quantity per container" error={errors.qtyPerContainer}>
            {(control) => <Input {...control} inputMode="decimal" value={values.qtyPerContainer} invalid={Boolean(errors.qtyPerContainer)} onChange={(event) => set({ qtyPerContainer: event.target.value })} />}
          </FormField>
          <FormField id="rule-max" label="Max gross weight (kg)" error={errors.maxWeightKg}>
            {(control) => <Input {...control} inputMode="decimal" value={values.maxWeightKg} invalid={Boolean(errors.maxWeightKg)} onChange={(event) => set({ maxWeightKg: event.target.value })} />}
          </FormField>
          <ProblemAlert title="Could not save the packing rule" problem={failure} />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save packing rule
            </Button>
            {notice ? (
              <p role="status" className={styles.note}>
                {notice}
              </p>
            ) : null}
          </div>
        </form>
      </Card>
      }
    />
  );
}
