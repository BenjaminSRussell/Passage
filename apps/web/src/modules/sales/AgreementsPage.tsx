import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  AGREEMENT_STATUSES,
  createAgreement,
  describeProblem,
  listAgreements,
  newKey,
  type Agreement,
  type AgreementFilter,
  type AgreementInput,
  type AgreementStatus,
  type Problem,
} from "./api/client";
import { DECIMAL, money, sentence, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const DATE = /^\d{4}-\d{2}-\d{2}$/;
const PRICE = /^\d+(\.\d{1,4})?$/;

export interface AgreementValues {
  legalEntityId: string;
  customerId: string;
  invoiceAccountId: string;
  productId: string;
  unit: string;
  unitPrice: string;
  currency: string;
  commitmentQty: string;
  validFrom: string;
  validTo: string;
  cumTolerance: string;
}

type Errors = Record<string, string>;

/** Checks that mirror the service (api/agreements.go buildAgreement). */
export function validateAgreement(values: AgreementValues): { errors: Errors; input?: AgreementInput } {
  const errors: Errors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (!UUID.test(values.customerId.trim())) errors.customerId = "Enter the customer's UUID.";
  const invoice = values.invoiceAccountId.trim();
  if (invoice && !UUID.test(invoice)) errors.invoiceAccountId = "Enter the invoice account's UUID, or leave it empty to invoice the customer.";
  if (!UUID.test(values.productId.trim())) errors.productId = "Enter the product's UUID.";
  if (values.unit.trim().length < 1 || values.unit.trim().length > 10) errors.unit = "Enter a unit of up to 10 characters.";
  if (!PRICE.test(values.unitPrice)) errors.unitPrice = "Enter a price of 0 or more with up to 4 decimals.";
  if (!/^[A-Z]{3}$/.test(values.currency)) errors.currency = "Enter a three-letter ISO currency code, such as USD.";
  if (!DECIMAL.test(values.commitmentQty) || /^0+(\.0+)?$/.test(values.commitmentQty)) errors.commitmentQty = "Enter a quantity above 0 with up to 6 decimals.";
  if (values.validFrom && !DATE.test(values.validFrom)) errors.validFrom = "Choose a date.";
  if (values.validTo && !DATE.test(values.validTo)) errors.validTo = "Choose a date.";
  if (values.cumTolerance !== "" && !DECIMAL.test(values.cumTolerance)) errors.cumTolerance = "Enter 0 or more with up to 6 decimals, or leave it empty for 0.";
  if (values.validFrom && values.validTo && values.validTo < values.validFrom) errors.validTo = "The end must not be before the start.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      legalEntityId: values.legalEntityId.trim(),
      customerId: values.customerId.trim(),
      ...(invoice ? { invoiceAccountId: invoice } : {}),
      productId: values.productId.trim(),
      unit: values.unit.trim(),
      unitPrice: values.unitPrice,
      currency: values.currency,
      commitmentQty: values.commitmentQty,
      ...(values.validFrom ? { validFrom: values.validFrom } : {}),
      ...(values.validTo ? { validTo: values.validTo } : {}),
      ...(values.cumTolerance !== "" ? { cumTolerance: values.cumTolerance } : {}),
    },
  };
}

const columns: DataTableColumn<Agreement>[] = [
  { key: "agreementNumber", header: "Agreement", mono: true },
  { key: "customerId", header: "Customer", mono: true, render: (a) => shortId(a.customerId) },
  { key: "productId", header: "Product", mono: true, render: (a) => shortId(a.productId) },
  { key: "unitPrice", header: "Price", align: "end", render: (a) => `${money(a.unitPrice, a.currency)} / ${a.unit}` },
  { key: "commitmentQty", header: "Committed", align: "end", render: (a) => `${a.commitmentQty} ${a.unit}` },
  { key: "releasedQty", header: "Released", align: "end", render: (a) => `${a.releasedQty} ${a.unit}` },
  { key: "remainingQty", header: "Remaining", align: "end", render: (a) => `${a.remainingQty} ${a.unit}` },
  { key: "validity", header: "Valid", render: (a) => (a.validFrom || a.validTo ? `${a.validFrom ?? "…"} to ${a.validTo ?? "…"}` : "Open-ended") },
  { key: "status", header: "Status", render: (a) => <StatusPill status={a.status} /> },
];

/** Sales agreements and releases (PRD §5.27.14): commitment, released and remaining per agreement (FR-27.10). */
export function AgreementsPage() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<{ legalEntityId: string; customerId: string; status: AgreementStatus | "" }>({ legalEntityId: "", customerId: "", status: "" });
  const [filter, setFilter] = useState<AgreementFilter>({});
  const invalid: Errors = {};
  if (draft.legalEntityId.trim() && !UUID.test(draft.legalEntityId.trim())) invalid.legalEntityId = "Enter a legal entity UUID.";
  if (draft.customerId.trim() && !UUID.test(draft.customerId.trim())) invalid.customerId = "Enter a customer UUID.";
  const state = useLoad((signal) => listAgreements(filter, signal), JSON.stringify(filter));

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Sales agreements"
        description="Blanket quantity commitments at an agreed price. Each release creates a draft order and draws down the commitment; a release beyond it is refused."
        actions={
          <Link className={buttonClassName("primary")} to="/sales/agreements/new">
            New agreement
          </Link>
        }
      />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          if (Object.keys(invalid).length === 0) setFilter({ legalEntityId: draft.legalEntityId.trim(), customerId: draft.customerId.trim(), status: draft.status });
        }}
      >
        <FormField id="agreements-le" label="Legal entity" error={invalid.legalEntityId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })} invalid={Boolean(invalid.legalEntityId)} />}
        </FormField>
        <FormField id="agreements-customer" label="Customer" error={invalid.customerId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.customerId} onChange={(e) => setDraft({ ...draft, customerId: e.target.value })} invalid={Boolean(invalid.customerId)} />}
        </FormField>
        <FormField id="agreements-status" label="Status">
          {(control) => (
            <Select {...control} value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as AgreementStatus | "" })}>
              <option value="">Any status</option>
              {AGREEMENT_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load sales agreements" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Sales agreements"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(a) => a.id}
          loading={state.loading}
          empty={{ title: "No sales agreements yet", description: "Create an agreement to commit a customer to a quantity at an agreed price." }}
          onRowOpen={(a) => navigate(`/sales/agreements/${a.id}`)}
        />
      )}
    </div>
  );
}

/** Creates an ACTIVE agreement numbered SA- (duty sales.agreement.maintain). */
export function NewAgreementPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<AgreementValues>({
    legalEntityId: "",
    customerId: "",
    invoiceAccountId: "",
    productId: "",
    unit: "EA",
    unitPrice: "",
    currency: "USD",
    commitmentQty: "",
    validFrom: "",
    validTo: "",
    cumTolerance: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<AgreementValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateAgreement(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createAgreement(input, key.current);
      navigate(`/sales/agreements/${created.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const field = (name: keyof AgreementValues, label: string, hint?: string, extra: { type?: string; mono?: boolean; optional?: boolean; decimal?: boolean } = {}) => (
    <FormField id={`agreement-${name}`} label={label} hint={hint} error={errors[name]} optional={extra.optional}>
      {(control) => (
        <Input
          {...control}
          type={extra.type ?? "text"}
          inputMode={extra.decimal ? "decimal" : undefined}
          className={extra.mono ? styles.mono : undefined}
          value={values[name]}
          onChange={(e) => update({ [name]: name === "currency" ? e.target.value.toUpperCase() : e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New sales agreement" description="Commit a customer to a quantity of one product at an agreed price. Releases against it create orders at this price." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New sales agreement">
          {field("legalEntityId", "Legal entity", "UUID of the selling legal entity.", { mono: true })}
          {field("customerId", "Customer", "UUID of the customer party.", { mono: true })}
          {field("invoiceAccountId", "Invoice account", "Leave empty to invoice the customer.", { mono: true, optional: true })}
          {field("productId", "Product", "UUID of the product.", { mono: true })}
          {field("commitmentQty", "Committed quantity", undefined, { decimal: true })}
          {field("unit", "Unit")}
          {field("unitPrice", "Unit price", undefined, { decimal: true })}
          {field("currency", "Currency")}
          {field("validFrom", "Valid from", undefined, { type: "date", optional: true })}
          {field("validTo", "Valid to", undefined, { type: "date", optional: true })}
          {field("cumTolerance", "Cumulative tolerance", "How far a scheduling release's cumulative received quantity may differ from shipped before it is held. Default 0.", { decimal: true, optional: true })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the agreement" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save agreement
            </Button>
            <Link className={buttonClassName("ghost")} to="/sales/agreements">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
