import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  type DataTableColumn,
  FormErrorSummary,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type FormFieldTarget,
} from "../../shell/ui";
import {
  createProductListing,
  deleteProductListing,
  describeProblem,
  LISTING_SCOPES,
  LISTING_TYPES,
  listProductListings,
  type ListingFilter,
  type ListingScope,
  type ListingType,
  type ProductListing,
  type ProductListingInput,
  type Problem,
} from "./api/client";
import { formatDateTime, sentence, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";

const DATE = /^\d{4}-\d{2}-\d{2}$/;

export interface ListingValues {
  legalEntityId: string;
  scopeType: ListingScope;
  scopeKey: string;
  productId: string;
  listingType: ListingType;
  validFrom: string;
  validTo: string;
}

type Errors = Record<string, string>;

/** Checks that mirror the service (api/listings.go buildListing). */
export function validateListing(values: ListingValues): { errors: Errors; input?: ProductListingInput } {
  const errors: Errors = {};
  const key = values.scopeKey.trim();
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (values.scopeType === "CUSTOMER" && !UUID.test(key)) errors.scopeKey = "Enter the customer's UUID.";
  if (values.scopeType === "GROUP" && (key.length < 1 || key.length > 80)) errors.scopeKey = "Enter the customer group id (up to 80 characters).";
  if (!UUID.test(values.productId.trim())) errors.productId = "Enter the product's UUID.";
  if (values.validFrom && !DATE.test(values.validFrom)) errors.validFrom = "Choose a date.";
  if (values.validTo && !DATE.test(values.validTo)) errors.validTo = "Choose a date.";
  if (values.validFrom && values.validTo && values.validTo < values.validFrom) errors.validTo = "The end must not be before the start.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      legalEntityId: values.legalEntityId.trim(),
      scopeType: values.scopeType,
      scopeKey: key,
      productId: values.productId.trim(),
      listingType: values.listingType,
      ...(values.validFrom ? { validFrom: values.validFrom } : {}),
      ...(values.validTo ? { validTo: values.validTo } : {}),
    },
  };
}

function validity(row: ProductListing): string {
  if (!row.validFrom && !row.validTo) return "Always";
  return `${row.validFrom ?? "…"} to ${row.validTo ?? "…"}`;
}

/** Listing inquiry (PRD §5.27.14): which products each customer or group may or may not order (FR-27.15, BR-27.13). */
export function ProductListingsPage() {
  const [draft, setDraft] = useState<ListingFilter>({ legalEntityId: "", productId: "", scopeKey: "" });
  const [filter, setFilter] = useState<ListingFilter>({});
  const [confirming, setConfirming] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const invalid: Errors = {};
  if (draft.legalEntityId?.trim() && !UUID.test(draft.legalEntityId.trim())) invalid.legalEntityId = "Enter a legal entity UUID.";
  if (draft.productId?.trim() && !UUID.test(draft.productId.trim())) invalid.productId = "Enter a product UUID.";
  const state = useLoad((signal) => listProductListings(filter, signal), JSON.stringify(filter));

  async function remove(row: ProductListing) {
    setBusy(row.id);
    setFailure(null);
    setNotice("");
    try {
      await deleteProductListing(row.id);
      setNotice(`The ${sentence(row.listingType).toLowerCase()} entry for ${shortId(row.productId)} was removed.`);
      setConfirming(null);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const columns: DataTableColumn<ProductListing>[] = [
    { key: "listingType", header: "Type", render: (r) => <Badge tone={r.listingType === "DENY" ? "danger" : "success"}>{sentence(r.listingType)}</Badge> },
    { key: "productId", header: "Product", render: (row) => <EntityName entity="product" id={row.productId} /> },
    { key: "scopeType", header: "Applies to", render: (r) => sentence(r.scopeType) },
    { key: "scopeKey", header: "Customer or group", render: (r) => (r.scopeType === "CUSTOMER" ? <EntityName entity="customer" id={r.scopeKey} fallback={r.scopeKey} /> : <span className={styles.mono}>{r.scopeKey}</span>) },
    { key: "validity", header: "Valid", render: validity },
    { key: "legalEntityId", header: "Legal entity", render: (r) => <EntityName entity="legalEntity" id={r.legalEntityId} /> },
    { key: "createdAt", header: "Added", render: (r) => `${formatDateTime(r.createdAt)} by ${r.createdBy}` },
    {
      key: "actions",
      header: "Action",
      render: (r) =>
        confirming === r.id ? (
          <div className={styles.actions}>
            <Button size="sm" variant="danger" loading={busy === r.id} onClick={() => remove(r)} aria-label={`Confirm removing the entry for ${r.productId}`}>
              Confirm removal
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setConfirming(null)}>
              Keep
            </Button>
          </div>
        ) : (
          <Button size="sm" disabled={busy !== null} onClick={() => setConfirming(r.id)} aria-label={`Remove the entry for ${r.productId}`}>
            Remove
          </Button>
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Product listings"
        description="Allow and deny lists per customer or customer group. A customer deny beats any group allow; a product with no allow entry is open to everyone. Orders are checked on the order date."
        actions={
          <Link className={buttonClassName("primary")} to="/sales/product-listings/new">
            New listing entry
          </Link>
        }
      />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          if (Object.keys(invalid).length === 0) setFilter({ legalEntityId: draft.legalEntityId?.trim(), productId: draft.productId?.trim(), scopeKey: draft.scopeKey?.trim() });
        }}
      >
        <FormField id="listings-le" label="Legal entity" error={invalid.legalEntityId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })} invalid={Boolean(invalid.legalEntityId)} />}
        </FormField>
        <FormField id="listings-product" label="Product" error={invalid.productId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.productId} onChange={(e) => setDraft({ ...draft, productId: e.target.value })} invalid={Boolean(invalid.productId)} />}
        </FormField>
        <FormField id="listings-key" label="Customer or group" hint="A customer UUID or a group code." optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.scopeKey} onChange={(e) => setDraft({ ...draft, scopeKey: e.target.value })} />}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert tone="danger" title="Could not remove the entry" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load product listings" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Product listings"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(r) => r.id}
          loading={state.loading}
          empty={{ title: "No listing entries", description: "Without entries every product can be ordered by every customer." }}
        />
      )}
    </div>
  );
}

const NEW_PRODUCT_LISTING_FIELDS: Record<string, FormFieldTarget> = {
  legalEntityId: { id: "listing-legalEntityId", label: "Legal entity" },
  productId: { id: "listing-productId", label: "Product" },
  validFrom: { id: "listing-validFrom", label: "Valid from" },
  validTo: { id: "listing-validTo", label: "Valid to" },
};

/** Adds one allow or deny entry (FR-27.15, duty sales.listing.maintain). */
export function NewProductListingPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<ListingValues>({ legalEntityId: "", scopeType: "CUSTOMER", scopeKey: "", productId: "", listingType: "DENY", validFrom: "", validTo: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([values]));
  const update = (patch: Partial<ListingValues>) => setValues((v) => ({ ...v, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateListing(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createProductListing(input);
      release();
      navigate("/sales/product-listings");
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const text = (name: "legalEntityId" | "scopeKey" | "productId", label: string, hint?: string) => (
    <FormField id={`listing-${name}`} label={label} hint={hint} error={errors[name]}>
      {(control) => <Input {...control} className={styles.mono} value={values[name]} onChange={(e) => update({ [name]: e.target.value })} invalid={Boolean(errors[name])} />}
    </FormField>
  );
  const date = (name: "validFrom" | "validTo", label: string) => (
    <FormField id={`listing-${name}`} label={label} error={errors[name]} optional>
      {(control) => <Input {...control} type="date" value={values[name]} onChange={(e) => update({ [name]: e.target.value })} invalid={Boolean(errors[name])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New listing entry" description="Deny a product to a customer or group, or list it for them only. Leave the dates empty for an entry that always applies." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New listing entry">
          <FormErrorSummary items={summaryItems(errors, NEW_PRODUCT_LISTING_FIELDS)} attempt={attempt} />
          {text("legalEntityId", "Legal entity", "UUID of the selling legal entity.")}
          <FormField id="listing-listingType" label="Entry type">
            {(control) => (
              <Select {...control} value={values.listingType} onChange={(e) => update({ listingType: e.target.value as ListingType })}>
                {LISTING_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {t === "DENY" ? "Deny (exclusion)" : "Allow (listing)"}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="listing-scopeType" label="Applies to">
            {(control) => (
              <Select {...control} value={values.scopeType} onChange={(e) => update({ scopeType: e.target.value as ListingScope })}>
                {LISTING_SCOPES.map((s) => (
                  <option key={s} value={s}>
                    {s === "CUSTOMER" ? "One customer" : "A customer group"}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("scopeKey", values.scopeType === "CUSTOMER" ? "Customer" : "Customer group", values.scopeType === "CUSTOMER" ? "UUID of the customer party." : "The customer group id from the customer record in party (customerGroupId, a UUID); orders and quotations read it from there.")}
          {text("productId", "Product", "UUID of the product.")}
          {date("validFrom", "Valid from")}
          {date("validTo", "Valid to")}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the entry" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save entry
            </Button>
            <Link className={buttonClassName("ghost")} to="/sales/product-listings">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      <p className={styles.note}>Orders that already accepted a denied line keep their logged override when an entry is removed.</p>
      {prompt}
    </div>
  );
}
