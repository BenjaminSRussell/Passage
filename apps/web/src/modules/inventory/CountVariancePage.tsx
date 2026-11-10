import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { getCountVariance, type CountVarianceLine, type CountVarianceTotals } from "./api/client";
import { dash, describeDimensions, formatDateTime, sentence, signed, UUID } from "./format";
import { ALL_DIMENSIONS, ProblemAlert, stateTones } from "./parts";
import { Named } from "../purchasing/Named";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

const columns: DataTableColumn<CountVarianceLine>[] = [
  { key: "sheetNumber", header: "Count sheet", mono: true, render: (row) => <Link to={`/inventory/count-sheets/${row.sheetId}`}>{row.sheetNumber}</Link> },
  { key: "sheetStatus", header: "Status", render: (row) => <Badge tone={stateTones[row.sheetStatus] ?? "neutral"}>{sentence(row.sheetStatus)}</Badge> },
  { key: "productId", header: "Product", render: (row) => <Named entity="product" id={row.productId} /> },
  { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, ALL_DIMENSIONS) },
  { key: "expectedQty", header: "Expected", align: "end", mono: true },
  { key: "countedQty", header: "Counted", align: "end", mono: true },
  { key: "varianceQty", header: "Variance", align: "end", mono: true, render: (row) => signed(row.varianceQty) },
  {
    key: "varianceValue",
    header: "Variance value",
    align: "end",
    mono: true,
    render: (row) => `${signed(row.varianceValue)} ${row.currency}${row.valueBasis === "ESTIMATE" ? " (est.)" : ""}`,
  },
  { key: "countedBy", header: "Counter", render: (row) => row.countedBy || dash },
  { key: "countedAt", header: "Counted at", mono: true, render: (row) => formatDateTime(row.countedAt) },
];

const totalColumns: DataTableColumn<CountVarianceTotals>[] = [
  { key: "counter", header: "Counter", render: (row) => row.counter || "Not recorded" },
  { key: "lines", header: "Lines", align: "end", render: (row) => String(row.lines) },
  { key: "absVarianceQty", header: "Quantity off", align: "end", mono: true },
  { key: "gainValue", header: "Gain", align: "end", mono: true, render: (row) => `${row.gainValue} ${row.currency}` },
  { key: "lossValue", header: "Loss", align: "end", mono: true, render: (row) => `${row.lossValue} ${row.currency}` },
  { key: "netValue", header: "Net", align: "end", mono: true, render: (row) => `${signed(row.netValue)} ${row.currency}` },
];

export interface CountVarianceFilters {
  legalEntityId: string;
  planId: string;
  sheetId: string;
  warehouseId: string;
  counter: string;
  from: string;
  to: string;
}

const noFilters: CountVarianceFilters = { legalEntityId: "", planId: "", sheetId: "", warehouseId: "", counter: "", from: "", to: "" };

type FilterErrors = Partial<Record<keyof CountVarianceFilters, string>>;

/** Checks the filter form before it is sent. */
export function validateCountVarianceFilters(f: CountVarianceFilters): FilterErrors {
  const errors: FilterErrors = {};
  for (const [key, label] of [["legalEntityId", "legal entity"], ["planId", "count plan"], ["sheetId", "count sheet"]] as const) {
    if (f[key].trim() !== "" && !UUID.test(f[key].trim())) errors[key] = `Enter the ${label} id (a UUID).`;
  }
  if (f.from && f.to && f.to < f.from) errors.to = "The end date is before the start date.";
  return errors;
}

/** Count variance report (3.16.14): every counted line with its variance and value, and totals per counter. */
export function CountVariancePage() {
  const [draft, setDraft] = useState<CountVarianceFilters>(noFilters);
  const [filters, setFilters] = useState<CountVarianceFilters>(noFilters);
  const [errors, setErrors] = useState<FilterErrors>({});
  const load = useLoad((signal) => getCountVariance(filters, signal), JSON.stringify(filters));
  const rows = load.data?.items ?? [];
  const totals = load.data?.totalsByCounter ?? [];
  const estimates = rows.filter((row) => row.valueBasis === "ESTIMATE").length;

  function apply(event: FormEvent) {
    event.preventDefault();
    const found = validateCountVarianceFilters(draft);
    setErrors(found);
    if (Object.keys(found).length === 0) setFilters(draft);
  }
  const set = (name: keyof CountVarianceFilters) => (event: { target: { value: string } }) => setDraft({ ...draft, [name]: event.target.value });

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Count variance"
        description="Counted quantity against the book, valued per line. A posted sheet is valued at the cost of its count journal; a sheet still open is valued at today's running average and marked as an estimate."
        actions={
          <Link className={buttonClassName("ghost")} to="/inventory/count-sheets">
            Count sheets
          </Link>
        }
      />
      <Card title="Filter">
        <form onSubmit={apply} noValidate className={styles.form}>
          <FormField id="variance-plan" label="Count plan" optional error={errors.planId}>
            {(control) => <Input {...control} value={draft.planId} onChange={set("planId")} placeholder="Plan id" invalid={Boolean(errors.planId)} />}
          </FormField>
          <FormField id="variance-sheet" label="Count sheet" optional error={errors.sheetId}>
            {(control) => <Input {...control} value={draft.sheetId} onChange={set("sheetId")} placeholder="Sheet id" invalid={Boolean(errors.sheetId)} />}
          </FormField>
          <FormField id="variance-warehouse" label="Warehouse" optional>
            {(control) => <Input {...control} value={draft.warehouseId} onChange={set("warehouseId")} />}
          </FormField>
          <FormField id="variance-counter" label="Counter" optional>
            {(control) => <Input {...control} value={draft.counter} onChange={set("counter")} placeholder="User id of the counter" />}
          </FormField>
          <LookupFormField id="variance-entity" label="Legal entity" entity="legalEntity" optional prefill={false} value={draft.legalEntityId} error={errors.legalEntityId} onChange={(legalEntityId) => setDraft({ ...draft, legalEntityId })} />
          <FormField id="variance-from" label="Counted from" optional>
            {(control) => <Input {...control} type="date" value={draft.from} onChange={set("from")} />}
          </FormField>
          <FormField id="variance-to" label="Counted to" optional error={errors.to}>
            {(control) => <Input {...control} type="date" value={draft.to} onChange={set("to")} invalid={Boolean(errors.to)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Apply filters
            </Button>
            <Button
              onClick={() => {
                setErrors({});
                setDraft(noFilters);
                setFilters(noFilters);
              }}
            >
              Clear filters
            </Button>
          </div>
        </form>
      </Card>
      {estimates > 0 ? (
        <Alert tone="info" title="Some values are estimates">
          {`${estimates} ${estimates === 1 ? "line is" : "lines are"} on a sheet that is not posted yet and valued at today's running average.`}
        </Alert>
      ) : null}
      {load.data?.truncated ? (
        <Alert tone="info" title="Showing the first 1,000 lines">
          More lines match. Narrow the filters to see the rest.
        </Alert>
      ) : null}
      {load.error ? (
        <ProblemAlert title="Could not load the count variance report" problem={load.error} onRetry={load.reload} />
      ) : (
        <>
          <Card title="Totals per counter">
            <DataTable
              label="Variance by counter"
              columns={totalColumns}
              rows={totals}
              getRowKey={(row) => `${row.counter}|${row.currency}`}
              loading={load.loading}
              empty={{ title: "No counts to total", description: "Totals appear once counters have entered counts." }}
            />
          </Card>
          <DataTable
            label="Count variance"
            columns={columns}
            rows={rows}
            getRowKey={(row) => row.lineId}
            loading={load.loading}
            empty={{ title: "No counted lines", description: "Enter counts on a count sheet, or widen the filters." }}
          />
        </>
      )}
    </div>
  );
}
