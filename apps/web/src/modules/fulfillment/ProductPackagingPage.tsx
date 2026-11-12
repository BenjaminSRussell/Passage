import { useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Button, Card, FormField, Input, ListPage, loadedRows, type DataTableColumn, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { describeProblem, listProductPackaging, saveProductPackaging, type ProductPackaging, type Problem } from "./api/client";
import { DECIMAL, MATERIAL_CODE, shortId, UUID } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { PlainName } from "../sales/OrderNumber";

const columns: DataTableColumn<ProductPackaging>[] = [
  { key: "productId", header: "Product", render: (row) => <PlainName short entity="product" id={row.productId} /> },
  { key: "materialCode", header: "Material", mono: true },
  { key: "weightPerUnitKg", header: "kg per unit", align: "end" },
];

interface Values {
  productId: string;
  materialCode: string;
  weightPerUnitKg: string;
}

const blank: Values = { productId: "", materialCode: "", weightPerUnitKg: "" };

/** Packaging materials per product unit (FR-28.14): the weights the packaging fee is computed from. */
const packagingFilters: ListFilterDef[] = [{ kind: "entity", key: "productId", label: "Product", entity: "product" }];

export function ProductPackagingPage() {
  // Bumped after a save, so the list reads the saved packaging.
  const [reloads, setReloads] = useState(0);
  const usePackagingRows = (query: ListQuery) => {
    const filter = query.filters.productId ?? "";
    return loadedRows(useLoad((signal) => listProductPackaging({ productId: filter }, signal), `${filter}-${reloads}`));
  };
  const [values, setValues] = useState<Values>(blank);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  function set(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.renew();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!UUID.test(values.productId.trim())) found.productId = "Enter the product id (a UUID).";
    if (!MATERIAL_CODE.test(values.materialCode.trim())) found.materialCode = "Enter a material code of up to 32 letters or digits, such as CARDBOARD.";
    if (!DECIMAL.test(values.weightPerUnitKg.trim())) found.weightPerUnitKg = "Enter the kg of this material per unit, 0 or more, with at most 6 decimals.";
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const saved = await saveProductPackaging(
        { productId: values.productId.trim(), materialCode: values.materialCode.trim(), weightPerUnitKg: values.weightPerUnitKg.trim() },
        key.current(),
      );
      key.renew();
      setValues(blank);
      setNotice(`Saved ${saved.weightPerUnitKg} kg ${saved.materialCode} per unit of product ${shortId(saved.productId)}.`);
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
      id="fulfillment-product-packaging"
      title="Packaging materials"
      description="The packaging each sold unit carries, per material. A packing slip posted with a ship-to country rates these weights as the statistical packaging fee."
      noun="packaging rows"
      singular="packaging row"
      columns={columns}
      filters={packagingFilters}
      useRows={usePackagingRows}
      getRowKey={(row) => `${row.productId}-${row.materialCode}`}
      onRowOpen={(row) => {
        setValues({ productId: row.productId, materialCode: row.materialCode, weightPerUnitKg: row.weightPerUnitKg });
        key.renew();
      }}
      empty={{ title: "No packaging recorded", description: "Add the packaging material weight per unit of each product you ship." }}
      footer={
        <>
      <Card title="Set the packaging of a product">
        <form className={styles.form} onSubmit={submit} noValidate>
          <p className={styles.note}>Saving the same product and material replaces the weight. A weight of 0 stops the material counting.</p>
          <FormField id="pp-product" label="Product" error={errors.productId}>
            {(control) => <EntityLookup entity="product" {...control} value={values.productId} invalid={Boolean(errors.productId)} onChange={(event) => set({ productId: event.target.value })} />}
          </FormField>
          <FormField id="pp-material" label="Material code" error={errors.materialCode}>
            {(control) => <Input {...control} maxLength={32} value={values.materialCode} invalid={Boolean(errors.materialCode)} onChange={(event) => set({ materialCode: event.target.value.toUpperCase() })} />}
          </FormField>
          <FormField id="pp-weight" label="Weight per unit (kg)" error={errors.weightPerUnitKg}>
            {(control) => <Input {...control} inputMode="decimal" value={values.weightPerUnitKg} invalid={Boolean(errors.weightPerUnitKg)} onChange={(event) => set({ weightPerUnitKg: event.target.value })} />}
          </FormField>
          <ProblemAlert title="Could not save the packaging" problem={failure} />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save packaging
            </Button>
            {notice ? (
              <p role="status" className={styles.note}>
                {notice}
              </p>
            ) : null}
          </div>
        </form>
      </Card>
        </>
      }
    />
  );
}
