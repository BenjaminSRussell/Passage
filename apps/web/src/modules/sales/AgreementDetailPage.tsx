import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getAgreement,
  newKey,
  releaseAgreementOrder,
  type AgreementRelease,
  type Order,
  type Problem,
  type ReleaseOrderInput,
} from "./api/client";
import { DECIMAL, formatDateTime, money, supplementaryNote, today, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const DATE = /^\d{4}-\d{2}-\d{2}$/;

export interface ReleaseValues {
  quantity: string;
  siteId: string;
  warehouseId: string;
  requestedShipDate: string;
}

type Errors = Record<string, string>;

/** Checks that mirror the service (api/agreements.go buildRelease). */
export function validateRelease(values: ReleaseValues): { errors: Errors; input?: ReleaseOrderInput } {
  const errors: Errors = {};
  if (!DECIMAL.test(values.quantity) || /^0+(\.0+)?$/.test(values.quantity)) errors.quantity = "Enter a quantity above 0 with up to 6 decimals.";
  if (!UUID.test(values.siteId.trim())) errors.siteId = "Enter the site's UUID.";
  if (!UUID.test(values.warehouseId.trim())) errors.warehouseId = "Enter the warehouse's UUID.";
  if (!DATE.test(values.requestedShipDate)) errors.requestedShipDate = "Choose the requested ship date.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { quantity: values.quantity, siteId: values.siteId.trim(), warehouseId: values.warehouseId.trim(), requestedShipDate: values.requestedShipDate } };
}

const releaseColumns: DataTableColumn<AgreementRelease>[] = [
  { key: "releasedAt", header: "Released", render: (r) => formatDateTime(r.releasedAt) },
  { key: "quantity", header: "Quantity", align: "end" },
  { key: "orderNumber", header: "Order", mono: true, render: (r) => <Link to={`/sales/orders/${r.salesOrderId}`}>{r.orderNumber}</Link> },
  { key: "actor", header: "By" },
];

/** One agreement with its commitment, release history and the release-order form (FR-27.10). */
export function AgreementDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getAgreement(id, signal), id);
  const [values, setValues] = useState<ReleaseValues>({ quantity: "", siteId: "", warehouseId: "", requestedShipDate: today() });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [created, setCreated] = useState<Order | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  if (state.error && !state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Sales agreement" />
        <Alert tone="danger" title="Could not load the agreement" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Sales agreement" />
        <span className="visually-hidden">Loading the agreement…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const agreement = state.data.value;

  function update(patch: Partial<ReleaseValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateRelease(values);
    setErrors(found);
    setFailure(null);
    setCreated(null);
    if (!input) return;
    setBusy(true);
    try {
      const res = await releaseAgreementOrder(agreement.id, input, key.current);
      setCreated(res.order);
      key.current = newKey();
      setValues((v) => ({ ...v, quantity: "" }));
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const facts: [string, ReactNode][] = [
    ["Customer", agreement.customerId],
    ["Invoice account", agreement.invoiceAccountId],
    ["Product", agreement.productId],
    ["Legal entity", agreement.legalEntityId],
    ["Agreed price", `${money(agreement.unitPrice, agreement.currency)} per ${agreement.unit}`],
    ["Committed", `${agreement.commitmentQty} ${agreement.unit}`],
    ["Released", `${agreement.releasedQty} ${agreement.unit}`],
    ["Remaining", `${agreement.remainingQty} ${agreement.unit}`],
    ["Valid", agreement.validFrom || agreement.validTo ? `${agreement.validFrom ?? "…"} to ${agreement.validTo ?? "…"}` : "Open-ended"],
    ["Created", `${formatDateTime(agreement.createdAt)} by ${agreement.createdBy}`],
  ];
  const mono = new Set(["Customer", "Invoice account", "Product", "Legal entity"]);
  const exhausted = agreement.status === "EXHAUSTED";

  const field = (name: keyof ReleaseValues, label: string, extra: { type?: string; mono?: boolean; decimal?: boolean; hint?: string } = {}) => (
    <FormField id={`release-${name}`} label={label} hint={extra.hint} error={errors[name]}>
      {(control) => (
        <Input
          {...control}
          type={extra.type ?? "text"}
          inputMode={extra.decimal ? "decimal" : undefined}
          className={extra.mono ? styles.mono : undefined}
          value={values[name]}
          onChange={(e) => update({ [name]: e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Sales agreement ${agreement.agreementNumber}`}
        meta={<StatusPill status={agreement.status} />}
        actions={
          <>
            <Link className={buttonClassName("ghost")} to={`/sales/agreements/${agreement.id}/schedule-releases`}>
              Scheduling releases
            </Link>
            <Link className={buttonClassName("ghost")} to="/sales/agreements">
              Back to agreements
            </Link>
          </>
        }
      />
      <Card>
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={mono.has(term) ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <Card title="Release an order">
        {exhausted ? (
          <EmptyState compact headingLevel={3} title="Commitment used up" description="Every committed unit has been released. Create a new agreement to commit more." />
        ) : (
          <form className={styles.form} onSubmit={submit} noValidate aria-label="Release an order">
            {field("quantity", "Quantity", { decimal: true, hint: `Up to ${agreement.remainingQty} ${agreement.unit}.` })}
            {field("siteId", "Site", { mono: true })}
            {field("warehouseId", "Warehouse", { mono: true })}
            {field("requestedShipDate", "Requested ship date", { type: "date" })}
            {failure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title={failure.code === "SLS-0008" ? "Not enough commitment left" : "Could not release the order"} live="alert">
                  {failure.message} ({failure.code})
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Release order
              </Button>
            </div>
          </form>
        )}
        <Alert tone="success" live="status">
          {created ? (
            <>
              Draft order <Link to={`/sales/orders/${created.id}`}>{created.orderNumber}</Link> was created at the agreed price.{supplementaryNote(created.lines)}
            </>
          ) : null}
        </Alert>
      </Card>
      <Card title="Releases">
        {agreement.releases && agreement.releases.length > 0 ? (
          <DataTable label="Releases" columns={releaseColumns} rows={agreement.releases} getRowKey={(r) => r.id} />
        ) : (
          <EmptyState compact headingLevel={3} title="No releases yet" description="Each release creates a draft order and appears here." />
        )}
      </Card>
    </div>
  );
}
