import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { returnableAging, type AgedReturnableBalance } from "./api/client";
import { formatDate, formatDateTime, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Most balances one view shows; the counts above the table cover all of them. */
export const AGING_LIMIT = 500;

const columns: DataTableColumn<AgedReturnableBalance>[] = [
  { key: "customerId", header: "Customer", mono: true, render: (row) => shortId(row.customerId) },
  { key: "containerType", header: "Container type", mono: true },
  { key: "balance", header: "Held by customer", align: "end" },
  { key: "oldestOutstandingDate", header: "Oldest held since", render: (row) => formatDate(row.oldestOutstandingDate) },
  { key: "daysOutstanding", header: "Days outstanding", align: "end", render: (row) => String(row.daysOutstanding) },
  { key: "chargeDays", header: "Charge after (days)", align: "end", render: (row) => String(row.chargeDays) },
  {
    key: "chargeDue",
    header: "Charge",
    render: (row) => (row.chargeDue ? <Badge tone="danger">Charge due</Badge> : <Badge tone="neutral">Not due</Badge>),
  },
  { key: "lastShippedAt", header: "Last shipped", render: (row) => formatDateTime(row.lastShippedAt) },
  { key: "lastReturnedAt", header: "Last returned", render: (row) => formatDateTime(row.lastReturnedAt) },
];

interface Filter {
  customerId: string;
  chargeDays: string;
  show: "all" | "due";
}

const blank: Filter = { customerId: "", chargeDays: "", show: "all" };

function validate(values: Filter): Record<string, string> {
  const found: Record<string, string> = {};
  if (values.customerId !== "" && !UUID.test(values.customerId)) found.customerId = "Enter the customer id (a UUID), or leave it empty.";
  const days = Number(values.chargeDays);
  if (values.chargeDays !== "" && (!/^\d{2,3}$/.test(values.chargeDays) || days < 30 || days > 365)) found.chargeDays = "Enter a whole number of days from 30 to 365, or leave it empty.";
  return found;
}

/**
 * Returnable packaging aging (FR-28.16, section 5.28.14): what each customer
 * still holds, aged first in, first out from the packing slips that shipped
 * it, and flagged once it has been out longer than the charge days of its
 * legal entity (rtp_charge_days), unless a number of days is entered here.
 */
export function ReturnableAgingPage() {
  const [values, setValues] = useState<Filter>(blank);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [filter, setFilter] = useState<Filter>(blank);
  const state = useLoad(
    (signal) =>
      returnableAging(
        { customerId: filter.customerId || undefined, chargeDays: filter.chargeDays === "" ? undefined : Number(filter.chargeDays), chargeDue: filter.show === "due", limit: AGING_LIMIT },
        signal,
      ),
    `${filter.customerId}/${filter.chargeDays}/${filter.show}`,
  );

  function apply(event: FormEvent) {
    event.preventDefault();
    const trimmed = { ...values, customerId: values.customerId.trim(), chargeDays: values.chargeDays.trim() };
    const found = validate(trimmed);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setFilter(trimmed);
  }

  function clear() {
    setValues(blank);
    setErrors({});
    setFilter(blank);
  }

  const data = state.data;
  const filtered = filter.customerId !== "" || filter.show !== "all";
  const changed = filtered || filter.chargeDays !== blank.chargeDays;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Returnable packaging aging"
        description="Pallets and crates customers still hold, aged from the oldest packing slip not yet returned (first in, first out). A balance is due to be charged once containers of a packing slip have been out longer than the charge days of that slip's legal entity."
        actions={
          <Link className={buttonClassName("secondary")} to="/fulfillment/returnable-packaging">
            Balances and return receipts
          </Link>
        }
      />
      <Card title="Filter">
        <form className={styles.form} onSubmit={apply} noValidate>
          <FormField id="aging-customer" label="Customer id" error={errors.customerId} optional>
            {(control) => <Input {...control} value={values.customerId} invalid={Boolean(errors.customerId)} onChange={(event) => setValues({ ...values, customerId: event.target.value })} />}
          </FormField>
          <FormField id="aging-days" label="Charge after days" hint="30 to 365. Empty uses each legal entity's rtp_charge_days (90 when none is saved)." error={errors.chargeDays} optional>
            {(control) => (
              <Input {...control} value={values.chargeDays} inputMode="numeric" invalid={Boolean(errors.chargeDays)} onChange={(event) => setValues({ ...values, chargeDays: event.target.value })} />
            )}
          </FormField>
          <FormField id="aging-show" label="Show">
            {(control) => (
              <Select {...control} value={values.show} onChange={(event) => setValues({ ...values, show: event.target.value as Filter["show"] })}>
                <option value="all">Every outstanding balance</option>
                <option value="due">Charge due only</option>
              </Select>
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Apply filter
            </Button>
            {changed ? (
              <Button type="button" onClick={clear}>
                Clear filter
              </Button>
            ) : null}
          </div>
        </form>
      </Card>
      {state.error ? (
        <Alert tone="danger" title="Could not load the aging" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <>
          {data ? (
            <StatGroup label="Returnable packaging aging totals">
              <StatTile label="Outstanding balances" value={data.count} hint={`As of ${data.asOf}`} />
              <StatTile label="Charge due" value={data.chargeDueCount} tone="danger" hint={filter.chargeDays === "" ? "Out longer than the legal entity's charge days" : `Out more than ${data.chargeDays} days`} />
            </StatGroup>
          ) : null}
          <DataTable
            label="Returnable packaging aging"
            columns={columns}
            rows={data?.items ?? []}
            getRowKey={(row) => `${row.customerId}/${row.containerType}`}
            loading={state.loading}
            empty={
              filtered
                ? { title: "No balances match the filter", description: "Change the customer or what to show, or clear the filter." }
                : { title: "No returnable packaging outstanding", description: "Customers hold no returnable containers; posting a packing slip with returnable containers adds them." }
            }
          />
          {data && data.count > data.items.length ? (
            <p className={styles.note}>
              Showing the {data.items.length} oldest of {data.count} balances; the counts cover all of them.
            </p>
          ) : null}
        </>
      )}
    </div>
  );
}
