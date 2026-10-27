import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { ssccRegister, type SsccRow } from "./api/client";
import { ContainerStatusBadge } from "./badges";
import { formatDate, formatDateTime, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Rows per page of the register. */
export const SSCC_PAGE = 100;

const columns: DataTableColumn<SsccRow>[] = [
  { key: "sscc", header: "SSCC", mono: true },
  { key: "status", header: "Status", render: (row) => <ContainerStatusBadge status={row.status} /> },
  { key: "containerType", header: "Container type" },
  {
    key: "shipmentNumber",
    header: "Shipment",
    mono: true,
    render: (row) => <Link to={`/fulfillment/shipments/${row.shipmentId}`}>{row.shipmentNumber}</Link>,
  },
  { key: "customerId", header: "Customer", mono: true, render: (row) => shortId(row.customerId) },
  { key: "issuedAt", header: "Issued", render: (row) => formatDateTime(row.issuedAt) },
  { key: "noReuseUntil", header: "No reuse until", render: (row) => formatDate(row.noReuseUntil) },
];

interface Filter {
  companyPrefix: string;
  customerId: string;
  from: string;
  to: string;
}

const blank: Filter = { companyPrefix: "", customerId: "", from: "", to: "" };
const DATE = /^\d{4}-\d{2}-\d{2}$/;

function validate(values: Filter): Record<string, string> {
  const found: Record<string, string> = {};
  if (values.companyPrefix !== "" && !/^\d{6,12}$/.test(values.companyPrefix)) found.companyPrefix = "Enter a GS1 company prefix of 6 to 12 digits.";
  if (values.customerId !== "" && !UUID.test(values.customerId)) found.customerId = "Enter the customer id (a UUID), or leave it empty.";
  if (values.from !== "" && !DATE.test(values.from)) found.from = "Enter a date.";
  if (values.to !== "" && !DATE.test(values.to)) found.to = "Enter a date.";
  if (!found.from && !found.to && values.from !== "" && values.to !== "" && values.to < values.from) found.to = "The last date cannot be before the first.";
  return found;
}

/**
 * SSCC register (section 5.28.14, BR-28.5): every SSCC issued, in SSCC order,
 * with its container, shipment and the first date it could be issued again.
 */
export function SsccRegisterPage() {
  const [values, setValues] = useState<Filter>(blank);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [filter, setFilter] = useState<Filter>(blank);
  // Cursors of the pages before the current one; the last entry is the current page's cursor.
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors.length > 0 ? cursors[cursors.length - 1] : undefined;
  const state = useLoad(
    (signal) =>
      ssccRegister(
        {
          companyPrefix: filter.companyPrefix || undefined,
          customerId: filter.customerId || undefined,
          from: filter.from || undefined,
          to: filter.to || undefined,
          cursor,
          limit: SSCC_PAGE,
        },
        signal,
      ),
    `${filter.companyPrefix}/${filter.customerId}/${filter.from}/${filter.to}/${cursor ?? ""}`,
  );

  function apply(event: FormEvent) {
    event.preventDefault();
    const trimmed = { companyPrefix: values.companyPrefix.trim(), customerId: values.customerId.trim(), from: values.from, to: values.to };
    const found = validate(trimmed);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setFilter(trimmed);
    setCursors([]);
  }

  function clear() {
    setValues(blank);
    setErrors({});
    setFilter(blank);
    setCursors([]);
  }

  const filtered = filter.companyPrefix !== "" || filter.customerId !== "" || filter.from !== "" || filter.to !== "";
  const next = state.data?.nextCursor;
  const field = (key: keyof Filter) => ({
    value: values[key],
    invalid: Boolean(errors[key]),
    onChange: (event: { target: { value: string } }) => setValues({ ...values, [key]: event.target.value }),
  });

  return (
    <div className={styles.stack}>
      <PageHeader
        title="SSCC register"
        description="Every SSCC issued to a container, in SSCC order. An SSCC is never issued twice; the no-reuse date is 12 months after issue (BR-28.5)."
      />
      <Card title="Filter">
        <form className={styles.form} onSubmit={apply} noValidate>
          <FormField id="sscc-prefix" label="GS1 company prefix" error={errors.companyPrefix} optional>
            {(control) => <Input {...control} {...field("companyPrefix")} inputMode="numeric" />}
          </FormField>
          <FormField id="sscc-customer" label="Customer id" error={errors.customerId} optional>
            {(control) => <Input {...control} {...field("customerId")} />}
          </FormField>
          <FormField id="sscc-from" label="Issued from" error={errors.from} optional>
            {(control) => <Input {...control} {...field("from")} type="date" />}
          </FormField>
          <FormField id="sscc-to" label="Issued to" error={errors.to} optional>
            {(control) => <Input {...control} {...field("to")} type="date" />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Apply filter
            </Button>
            {filtered ? (
              <Button type="button" onClick={clear}>
                Clear filter
              </Button>
            ) : null}
          </div>
        </form>
      </Card>
      {state.error ? (
        <Alert tone="danger" title="Could not load the SSCC register" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <>
          <DataTable
            label="SSCC register"
            columns={columns}
            rows={state.data?.items ?? []}
            getRowKey={(row) => row.sscc}
            loading={state.loading}
            empty={
              filtered
                ? { title: "No SSCCs match the filter", description: "Change the prefix, customer or dates, or clear the filter." }
                : { title: "No SSCCs issued yet", description: "An SSCC is issued when a container is packed on a shipment." }
            }
          />
          {cursors.length > 0 || next ? (
            <div className={styles.actions}>
              <Button size="sm" disabled={cursors.length === 0 || state.loading} onClick={() => setCursors(cursors.slice(0, -1))}>
                Previous page
              </Button>
              <Button size="sm" disabled={!next || state.loading} onClick={() => next && setCursors([...cursors, next])}>
                Next page
              </Button>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
