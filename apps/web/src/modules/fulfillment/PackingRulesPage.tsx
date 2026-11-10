import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listContainerTypes, listPackingRules, savePackingRule, type ContainerType, type PackingRule, type Problem } from "./api/client";
import { compareDecimal, DECIMAL, shortId, UUID } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<PackingRule>[] = [
  { key: "productId", header: "Product", mono: true, render: (row) => shortId(row.productId) },
  { key: "customerId", header: "Customer", mono: true, render: (row) => (row.customerId ? shortId(row.customerId) : "All customers") },
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

interface Data {
  rules: PackingRule[];
  types: ContainerType[];
}

/** Packing rules (FR-28.15): product, optionally for one customer, to a container type and its limits. */
export function PackingRulesPage() {
  const [product, setProduct] = useState("");
  const filter = UUID.test(product.trim()) ? product.trim() : "";
  const state = useLoad<Data>(async (signal) => {
    const [rules, types] = await Promise.all([listPackingRules({ productId: filter }, signal), listContainerTypes(signal)]);
    return { rules, types };
  }, filter);
  const [values, setValues] = useState<Values>(blank);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();
  const types = state.data?.types ?? [];

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
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
      key.renew();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Packing rules"
        description="Auto-pack uses the customer's rule for a product when there is one, otherwise the product's general rule. Lines without a rule stay unpacked."
        actions={
          <Link className={buttonClassName("secondary")} to="/fulfillment/container-types">
            Container types
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="rules-product" label="Product id" hint="Show the rules of one product." optional>
          {(control) => <Input {...control} value={product} onChange={(event) => setProduct(event.target.value)} />}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load packing rules" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Packing rules"
          columns={columns}
          rows={state.data?.rules ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          onRowOpen={(row) => {
            setValues({ productId: row.productId, customerId: row.customerId ?? "", containerType: row.containerType, qtyPerContainer: row.qtyPerContainer, maxWeightKg: row.maxWeightKg });
            key.renew();
          }}
          empty={
            filter
              ? { title: "No rules for this product", description: "Add one below; until then auto-pack leaves its lines unpacked (FUL-0012)." }
              : { title: "No packing rules yet", description: "Add a rule per product so auto-pack can fill containers." }
          }
        />
      )}
      <Card title="Add or change a packing rule">
        <form className={styles.form} onSubmit={submit} noValidate>
          <p className={styles.note}>One rule per product and customer: saving the same product and customer replaces the rule. Open a row above to edit it.</p>
          {state.data && types.length === 0 ? (
            <div className={styles.formActions}>
              <Alert tone="info" title="Add a container type first">
                A rule names a container type. <Link to="/fulfillment/container-types">Add container types</Link>.
              </Alert>
            </div>
          ) : null}
          <FormField id="rule-product" label="Product id" error={errors.productId}>
            {(control) => <Input {...control} value={values.productId} invalid={Boolean(errors.productId)} onChange={(event) => set({ productId: event.target.value })} />}
          </FormField>
          <FormField id="rule-customer" label="Customer id" hint="Empty: the rule applies to every customer." error={errors.customerId} optional>
            {(control) => <Input {...control} value={values.customerId} invalid={Boolean(errors.customerId)} onChange={(event) => set({ customerId: event.target.value })} />}
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
    </div>
  );
}
