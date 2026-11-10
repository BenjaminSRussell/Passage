import { useRef, useState, type FormEvent } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { lookupProblem } from "../../shell/errors";
import { holdsDuty } from "../../shell/dutyGate";
import { FeatureGate } from "../../shell/tenant/TenantSettings";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, FormField, FormSection, Input, PageHeader, Select, Skeleton, Textarea } from "../../shell/ui";
import { createProduct, ProductApiError, updateProduct, type Lifecycle, type Product, type ProductType, type TrackingFlags } from "./api/client";
import { DUTY_WRITE, lifecycleLabel, productTypeLabel, trackingFeature, trackingFlags, trackingLabel } from "./labels";
import styles from "./pages.module.css";
import { ProductNotFound } from "./pages";
import { ProductProblem } from "./ProductProblem";
import { useItemGroups, useProduct } from "./useProduct";

type Errors = Record<string, string>;

const NUMBER_PATTERN = /^[A-Z0-9][A-Z0-9._-]{0,39}$/;
const UNIT_PATTERN = /^[A-Z0-9._-]{1,10}$/;
const lifecycleOrder: Lifecycle[] = ["draft", "active", "phase_out", "obsolete"];

interface Values extends TrackingFlags {
  itemGroupId: string;
  productNumber: string;
  name: string;
  description: string;
  baseUom: string;
  subtype: "product" | "master";
  productType: ProductType;
  lifecycleState: Lifecycle;
}

const blank: Values = {
  productNumber: "",
  name: "",
  description: "",
  baseUom: "",
  subtype: "product",
  productType: "item",
  lifecycleState: "draft",
  itemGroupId: "",
  batchTracked: false,
  serialTracked: false,
  catchWeight: false,
};

export function validate(values: Values, creating: boolean): Errors {
  const errors: Errors = {};
  const number = values.productNumber.trim().toUpperCase();
  if (creating) {
    if (number === "") errors.productNumber = "Enter a product number.";
    else if (!NUMBER_PATTERN.test(number)) {
      errors.productNumber = "Use capital letters, digits, dots, dashes or underscores, starting with a letter or digit, up to 40 characters.";
    }
    const unit = values.baseUom.trim().toUpperCase();
    if (unit === "") errors.baseUom = "Enter the unit this product is counted in, for example EA.";
    else if (!UNIT_PATTERN.test(unit)) errors.baseUom = "Use up to 10 letters or digits for the unit, for example EA, KG or M.";
  }
  if (values.name.trim() === "") errors.name = "Enter a name for the product.";
  return errors;
}

function serverErrors(error: unknown): Errors {
  if (!(error instanceof ProductApiError)) return {};
  if (error.code === "PRD-NUM-FORMAT") {
    const entry = lookupProblem(error.code);
    return { productNumber: entry ? `${entry.message} ${entry.action}` : error.message };
  }
  return error.fields;
}

interface FormProps {
  initial: Values;
  creating: boolean;
  id?: string;
  version?: number;
  submitLabel: string;
  onReload?: () => void;
}

function ProductForm({ initial, creating, id, version, submitLabel, onReload }: FormProps) {
  const navigate = useNavigate();
  const [values, setValues] = useState<Values>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const formRef = useRef<HTMLFormElement>(null);
  const itemGroups = useItemGroups();
  const stocked = values.productType !== "service";
  const attempt = useRef<{ body: string; key: string } | null>(null);

  const set = <K extends keyof Values>(field: K, value: Values[K]) => setValues((prev) => ({ ...prev, [field]: value }));
  const field = (name: keyof Values) => errors[name];
  const tracking = (): TrackingFlags => ({
    batchTracked: stocked && values.batchTracked,
    serialTracked: stocked && values.serialTracked,
    catchWeight: stocked && values.catchWeight,
  });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validate(values, creating);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) {
      queueMicrotask(() => formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus());
      return;
    }
    setBusy(true);
    try {
      let saved: Product;
      if (creating) {
        const body = {
          productNumber: values.productNumber.trim().toUpperCase(),
          name: values.name.trim(),
          description: values.description.trim(),
          baseUom: values.baseUom.trim().toUpperCase(),
          subtype: values.subtype,
          productType: values.productType,
          ...(values.itemGroupId !== "" ? { itemGroupId: values.itemGroupId } : {}),
          ...Object.fromEntries(Object.entries(tracking()).filter(([, on]) => on)),
        };
        const serialized = JSON.stringify(body);
        if (attempt.current?.body !== serialized) attempt.current = { body: serialized, key: crypto.randomUUID() };
        saved = await createProduct(body, attempt.current.key);
      } else {
        saved = await updateProduct(id ?? "", version ?? 0, {
          name: values.name.trim(),
          description: values.description.trim(),
          lifecycleState: values.lifecycleState,
          itemGroupId: values.itemGroupId,
          ...tracking(),
        });
      }
      navigate(`/product/${encodeURIComponent(saved.id)}`);
    } catch (error) {
      setFailure(error);
      setErrors(serverErrors(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <form ref={formRef} className={styles.form} onSubmit={(event) => void submit(event)} noValidate aria-label={creating ? "Create a product" : "Edit the product"}>
        <FormField id="product-number" label="Product number" error={field("productNumber")} hint={creating ? "Letters, digits, dots, dashes and underscores. It can't change once the product is released." : undefined}>
          {(control) => (
            <Input
              {...control}
              value={values.productNumber}
              readOnly={!creating}
              onChange={(event) => set("productNumber", event.target.value.toUpperCase())}
              autoComplete="off"
            />
          )}
        </FormField>
        <FormField id="product-name" label="Name" error={field("name")}>
          {(control) => <Input {...control} value={values.name} onChange={(event) => set("name", event.target.value)} autoComplete="off" />}
        </FormField>
        {creating ? (
          <>
            <FormField id="product-unit" label="Base unit" error={field("baseUom")} hint="The unit stock is counted in, for example EA, KG or M.">
              {(control) => (
                <Input {...control} value={values.baseUom} onChange={(event) => set("baseUom", event.target.value.toUpperCase())} autoComplete="off" />
              )}
            </FormField>
            <FormField id="product-kind" label="Kind">
              {(control) => (
                <Select {...control} value={values.subtype} onChange={(event) => set("subtype", event.target.value as Values["subtype"])}>
                  <option value="product">Product</option>
                  <option value="master">Master (has variants)</option>
                </Select>
              )}
            </FormField>
            <FormField id="product-type" label="Type">
              {(control) => (
                <Select {...control} value={values.productType} onChange={(event) => set("productType", event.target.value as ProductType)}>
                  {(["item", "service"] as const).map((type) => (
                    <option key={type} value={type}>
                      {productTypeLabel[type]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </>
        ) : (
          <FormField id="product-state" label="State" error={field("lifecycleState")}>
            {(control) => (
              <Select {...control} value={values.lifecycleState} onChange={(event) => set("lifecycleState", event.target.value as Lifecycle)}>
                {lifecycleOrder.map((state) => (
                  <option key={state} value={state}>
                    {lifecycleLabel[state]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        )}
        <div className={styles.wide}>
          <FormField id="product-description" label="Description" optional error={field("description")}>
            {(control) => <Textarea {...control} value={values.description} onChange={(event) => set("description", event.target.value)} />}
          </FormField>
        </div>
        <FormSection collapse="more" defaultOpen={initial.itemGroupId !== "" || trackingFlags.some((flag) => initial[flag])}>
          <FormField
            id="product-item-group"
            label="Item group"
            optional
            error={field("itemGroupId")}
            hint={itemGroups.failed ? "Item groups could not be loaded. Try again later." : "Groups products for posting. Ask an administrator to add groups."}
          >
            {(control) => (
              <Select {...control} value={values.itemGroupId} onChange={(event) => set("itemGroupId", event.target.value)}>
                <option value="">None</option>
                {values.itemGroupId !== "" && !itemGroups.groups.some((group) => group.id === values.itemGroupId) ? (
                  <option value={values.itemGroupId}>Current item group</option>
                ) : null}
                {itemGroups.groups.map((group) => (
                  <option key={group.id} value={group.id}>
                    {group.name} ({group.code})
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {trackingFlags.map((flag) => (
            <FeatureGate key={flag} feature={trackingFeature[flag]}>
              <FormField id={`product-${flag}`} label={trackingLabel[flag]} hint={stocked ? undefined : "A service can't be tracked."}>
                {(control) => (
                  <Input {...control} type="checkbox" checked={stocked && values[flag]} disabled={!stocked} onChange={(event) => set(flag, event.target.checked)} />
                )}
              </FormField>
            </FeatureGate>
          ))}
        </FormSection>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            {submitLabel}
          </Button>
          <Button type="button" variant="ghost" onClick={() => navigate(creating ? "/product/items" : `/product/${encodeURIComponent(id ?? "")}`)}>
            Cancel
          </Button>
        </div>
      </form>
      {failure ? (
        <ProductProblem
          error={failure}
          title={creating ? "Could not create the product" : "Could not save the product"}
          retry={failure instanceof ProductApiError && failure.status === 412 ? onReload : undefined}
          retryLabel="Reload product (your edits will be cleared)"
        />
      ) : null}
    </>
  );
}

function NeedsWriteDuty() {
  return (
    <Alert tone="info" title="You can't change products">
      Ask an administrator for the product.write duty to create or edit products.
    </Alert>
  );
}

export function NewProductPage() {
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_WRITE);
  return (
    <div className={styles.stack}>
      <PageHeader title="New product" description="Add a product or master so it can be used on purchase and sales order lines." />
      {canWrite ? <ProductForm initial={blank} creating submitLabel="Create product" /> : <NeedsWriteDuty />}
    </div>
  );
}

export function EditProductPage() {
  const { id } = useParams();
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_WRITE);
  const { data, error, loading, reload } = useProduct(id);

  if (!canWrite) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Edit product" />
        <NeedsWriteDuty />
      </div>
    );
  }
  if (loading) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Edit product" />
        <Skeleton width="3/4" />
        <span className="visually-hidden" role="status">
          Loading the product…
        </span>
      </div>
    );
  }
  if (error instanceof ProductApiError && error.status === 404) {
    return <ProductNotFound onBack={() => navigate("/product/items")} />;
  }
  if (error || !data) {
    return <ProductProblem error={error} title="Could not load the product" retry={reload} />;
  }
  return (
    <div className={styles.stack}>
      <PageHeader title={`Edit ${data.name}`} />
      <ProductForm
        key={`${data.id}:${data.version}`}
        initial={{
          ...blank,
          productNumber: data.productNumber,
          name: data.name,
          description: data.description,
          lifecycleState: data.lifecycleState,
          productType: data.productType,
          itemGroupId: data.itemGroupId,
          batchTracked: data.batchTracked,
          serialTracked: data.serialTracked,
          catchWeight: data.catchWeight,
        }}
        creating={false}
        id={data.id}
        version={data.version}
        submitLabel="Save changes"
        onReload={reload}
      />
    </div>
  );
}
