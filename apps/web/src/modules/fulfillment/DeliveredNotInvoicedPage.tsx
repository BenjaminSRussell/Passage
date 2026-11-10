import { useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { useNavigate } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { deliveredNotInvoiced, type DniLine, type DniReport } from "./api/client";
import { formatDate, formatMoney, shortId, today, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** How many slips the page lists; the totals always cover every open slip. */
export const DNI_LINES = 500;

const columns: DataTableColumn<DniLine>[] = [
  { key: "packingSlipNumber", header: "Packing slip", mono: true },
  { key: "postingDate", header: "Posting date", render: (row) => formatDate(row.postingDate) },
  { key: "daysOutstanding", header: "Days outstanding", align: "end" },
  { key: "customerId", header: "Customer", mono: true, render: (row) => shortId(row.customerId) },
  { key: "orderId", header: "Order", mono: true, render: (row) => shortId(row.orderId) },
  { key: "amount", header: "Open on 1405", align: "end", render: (row) => formatMoney(row.amount, row.currency) },
];

interface Query {
  legalEntityId: string;
  customerId: string;
  asOf: string;
}

/**
 * Delivered not invoiced (FR-28.12, section 5.28.14): the open balance on
 * account 1405 of one legal entity as of a date, and the packing slips that make
 * it up. A slip leaves the report when it is invoiced or cancelled.
 */
export function DeliveredNotInvoicedPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Query>({ legalEntityId: "", customerId: "", asOf: today() });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [query, setQuery] = useState<Query | null>(null);
  const report = useLoad<DniReport | null>(
    (signal) =>
      query
        ? deliveredNotInvoiced({ legalEntityId: query.legalEntityId, customerId: query.customerId || undefined, asOf: query.asOf, limit: DNI_LINES }, signal)
        : Promise.resolve(null),
    query ? `${query.legalEntityId}/${query.customerId}/${query.asOf}` : "",
  );

  function run(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!UUID.test(values.legalEntityId.trim())) found.legalEntityId = "Enter the legal entity id (a UUID).";
    if (values.customerId.trim() !== "" && !UUID.test(values.customerId.trim())) found.customerId = "Enter the customer id (a UUID), or leave it empty.";
    if (!/^\d{4}-\d{2}-\d{2}$/.test(values.asOf)) found.asOf = "Enter the report date.";
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setQuery({ legalEntityId: values.legalEntityId.trim(), customerId: values.customerId.trim(), asOf: values.asOf });
  }

  const data = report.data;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Delivered not invoiced"
        description="Cost of goods shipped on a posted packing slip and not yet invoiced: the balance on account 1405. Slips posted with COGS at shipment never appear."
      />
      <Card title="Report">
        <form className={styles.form} onSubmit={run} noValidate>
          <FormField id="dni-le" label="Legal entity" error={errors.legalEntityId}>
            {(control) => <EntityLookup entity="legalEntity" {...control} value={values.legalEntityId} invalid={Boolean(errors.legalEntityId)} onChange={(event) => setValues({ ...values, legalEntityId: event.target.value })} />}
          </FormField>
          <FormField id="dni-customer" label="Customer" error={errors.customerId} optional>
            {(control) => <EntityLookup entity="customer" scope={{ legalEntityId: values.legalEntityId || undefined }} {...control} value={values.customerId} invalid={Boolean(errors.customerId)} onChange={(event) => setValues({ ...values, customerId: event.target.value })} />}
          </FormField>
          <FormField id="dni-asof" label="As of" error={errors.asOf}>
            {(control) => <Input {...control} type="date" value={values.asOf} invalid={Boolean(errors.asOf)} onChange={(event) => setValues({ ...values, asOf: event.target.value })} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Show report
            </Button>
          </div>
        </form>
      </Card>
      {query === null ? (
        <p className={styles.note}>Choose a legal entity to see its delivered-not-invoiced balance.</p>
      ) : report.error ? (
        <Alert tone="danger" title="Could not load the report" live="alert" action={<Button size="sm" onClick={report.reload}>Try again</Button>}>
          <ProblemMessage problem={report.error} />
        </Alert>
      ) : (
        <>
          {data && data.totals.length > 0 ? (
            <StatGroup label="Delivered not invoiced totals">
              {data.totals.map((t) => (
                <StatTile key={t.currency} label={`Open on ${data.account} (${t.currency})`} value={formatMoney(t.amount, t.currency)} hint={`${t.count} packing slip${t.count === 1 ? "" : "s"}`} />
              ))}
            </StatGroup>
          ) : null}
          <DataTable
            label={`Delivered not invoiced as of ${query.asOf}`}
            columns={columns}
            rows={data?.lines ?? []}
            getRowKey={(row) => row.packingSlipId}
            loading={report.loading}
            onRowOpen={(row) => navigate(`/fulfillment/packing-slips/${row.packingSlipId}`)}
            empty={{ title: "Nothing delivered and not invoiced", description: "Every packing slip of this legal entity posted by this date is invoiced or cancelled." }}
          />
          {data && data.lineCount > data.lines.length ? (
            <p className={styles.note}>
              Showing the {data.lines.length} oldest of {data.lineCount} packing slips; the totals cover all of them.
            </p>
          ) : null}
        </>
      )}
    </div>
  );
}
