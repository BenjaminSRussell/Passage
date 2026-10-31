import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { traceTracking, TRACE_DIRECTIONS, type TraceDirection, type TraceLine } from "./api/client";
import { dash, describeDimensions, sentence, shortId, signed, UUID } from "./format";
import { ALL_DIMENSIONS, ProblemAlert } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const directionLabels: Record<TraceDirection, string> = { BOTH: "Every movement", FORWARD: "Forward: where it went", BACKWARD: "Backward: where it came from" };

const columns: DataTableColumn<TraceLine>[] = [
  { key: "physicalDate", header: "Date", mono: true },
  { key: "productId", header: "Product", mono: true, render: (row) => shortId(row.productId) },
  { key: "status", header: "Status", render: (row) => sentence(row.issueStatus ?? row.receiptStatus ?? "") },
  { key: "qty", header: "Quantity", align: "end", mono: true, render: (row) => `${signed(row.qty)} ${row.enteredUnit}` },
  { key: "running", header: "Balance", align: "end", mono: true, render: (row) => row.running ?? dash },
  { key: "dimensions", header: "Location and tracking", render: (row) => describeDimensions(row.dimensions, ALL_DIMENSIONS) },
  {
    key: "origin",
    header: "Document",
    render: (row) =>
      row.journalId ? <Link to={`/inventory/journals/${row.journalId}`}>{`Journal ${shortId(row.journalId)}`}</Link> : sentence(row.sourceType),
  },
  { key: "id", header: "Transaction", render: (row) => <Link to={`/inventory/transactions/${row.id}`}>{shortId(row.id)}</Link> },
];

export interface TraceFilters {
  batchId: string;
  serialId: string;
  productId: string;
  direction: TraceDirection;
}

const noFilters: TraceFilters = { batchId: "", serialId: "", productId: "", direction: "BOTH" };

/** Checks the form: a trace needs a batch or a serial. */
export function validateTrace(f: TraceFilters): Partial<Record<keyof TraceFilters, string>> {
  const errors: Partial<Record<keyof TraceFilters, string>> = {};
  if (f.batchId.trim() === "" && f.serialId.trim() === "") errors.batchId = "Enter a batch or a serial number to trace.";
  if (f.productId.trim() !== "" && !UUID.test(f.productId.trim())) errors.productId = "Enter the product id (a UUID).";
  return errors;
}

/** Batch and serial trace (3.16.14): the movements of one tracked unit through time. */
export function TrackingTracePage() {
  const [draft, setDraft] = useState<TraceFilters>(noFilters);
  const [filters, setFilters] = useState<TraceFilters | null>(null);
  const [errors, setErrors] = useState<Partial<Record<keyof TraceFilters, string>>>({});
  const load = useLoad(async (signal) => (filters ? traceTracking(filters, signal) : null), JSON.stringify(filters));
  const rows = load.data?.items ?? [];

  function apply(event: FormEvent) {
    event.preventDefault();
    const found = validateTrace(draft);
    setErrors(found);
    if (Object.keys(found).length === 0) setFilters(draft);
  }
  const set = (name: Exclude<keyof TraceFilters, "direction">) => (event: { target: { value: string } }) => setDraft({ ...draft, [name]: event.target.value });

  return (
    <div className={styles.stack}>
      <PageHeader title="Batch and serial trace" description="Follow a batch or serial number through receipts, issues and transfers, oldest movement first." />
      <Card title="Trace">
        <form onSubmit={apply} noValidate className={styles.form}>
          <FormField id="trace-batch" label="Batch" optional error={errors.batchId}>
            {(control) => <Input {...control} value={draft.batchId} onChange={set("batchId")} invalid={Boolean(errors.batchId)} />}
          </FormField>
          <FormField id="trace-serial" label="Serial number" optional>
            {(control) => <Input {...control} value={draft.serialId} onChange={set("serialId")} />}
          </FormField>
          <FormField id="trace-product" label="Product" optional error={errors.productId}>
            {(control) => <Input {...control} value={draft.productId} onChange={set("productId")} placeholder="Product id" invalid={Boolean(errors.productId)} />}
          </FormField>
          <FormField id="trace-direction" label="Direction">
            {(control) => (
              <Select {...control} value={draft.direction} onChange={(event) => setDraft({ ...draft, direction: event.target.value as TraceDirection })}>
                {TRACE_DIRECTIONS.map((d) => (
                  <option key={d} value={d}>
                    {directionLabels[d]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Trace
            </Button>
          </div>
        </form>
      </Card>
      {load.data?.truncated ? (
        <Alert tone="info" title="Showing the first 1,000 movements">
          More movements match. Restrict the trace to one product to see the rest.
        </Alert>
      ) : null}
      {!filters ? (
        <Alert tone="info" title="Nothing traced yet">
          Enter a batch or a serial number and choose Trace.
        </Alert>
      ) : load.error ? (
        <ProblemAlert title="Could not trace the stock" problem={load.error} onRetry={load.reload} />
      ) : (
        <DataTable
          label="Traced movements"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={load.loading}
          empty={{ title: "No movements found", description: "No transaction carries that batch or serial number in your legal entities." }}
        />
      )}
    </div>
  );
}
