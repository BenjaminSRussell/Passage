import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listWaves, type WaveSummary } from "./api/client";
import { dash, formatDateTime, GROUP_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { WaveStatusBadge } from "./WaveActions";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";

const columns: DataTableColumn<WaveSummary>[] = [
  { key: "waveNumber", header: "Wave", mono: true },
  { key: "status", header: "Status", render: (row) => <WaveStatusBadge status={row.status} /> },
  { key: "picker", header: "Picker", render: (row) => row.picker ?? dash },
  { key: "groupBy", header: "Grouped by", render: (row) => GROUP_LABELS[row.groupBy] ?? row.groupBy },
  { key: "groupKey", header: "Group", render: (row) => (row.groupKey === "UNASSIGNED" ? "Unassigned" : row.groupKey) },
  { key: "lineCount", header: "Lines", align: "end" },
  { key: "orderCount", header: "Orders", align: "end" },
  { key: "warehouseId", header: "Warehouse", render: (row) => <EntityName entity="warehouse" id={row.warehouseId} /> },
  { key: "createdAt", header: "Created", render: (row) => formatDateTime(row.createdAt) },
];

interface Filter {
  status: "" | "OPEN" | "CLOSED";
  /** "" = anyone, "-" = nobody assigned, otherwise a picker name. */
  picker: string;
}

/** Pick waves list (section 5.28.14 "Pick work / waves"), newest first, filtered by status and picker. */
export function WavesPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Filter>({ status: "", picker: "" });
  const [filter, setFilter] = useState<Filter>(values);
  const unassigned = filter.picker.trim() === "-";
  const state = useLoad(
    (signal) =>
      listWaves(
        { status: filter.status, picker: unassigned ? undefined : filter.picker.trim(), unassigned, limit: 200 },
        signal,
      ),
    `waves-${filter.status}-${filter.picker}`,
  );

  function apply(event: FormEvent) {
    event.preventDefault();
    setFilter({ ...values });
  }
  const filtered = filter.status !== "" || filter.picker.trim() !== "";

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Pick waves"
        description="Waves created by the picking workbench. Open one to assign a picker, pick, print the pick list and close it."
        actions={
          <Link className={buttonClassName("primary")} to="/fulfillment/workbench">
            Picking workbench
          </Link>
        }
      />
      <form className={styles.toolbar} onSubmit={apply}>
        <FormField id="wave-status" label="Status">
          {(control) => (
            <Select {...control} value={values.status} onChange={(event) => setValues({ ...values, status: event.target.value as Filter["status"] })}>
              <option value="">All</option>
              <option value="OPEN">Open</option>
              <option value="CLOSED">Closed</option>
            </Select>
          )}
        </FormField>
        <FormField id="wave-filter-picker" label="Picker" hint="Enter - for waves nobody has yet.">
          {(control) => <Input {...control} value={values.picker} maxLength={64} onChange={(event) => setValues({ ...values, picker: event.target.value })} />}
        </FormField>
        <Button type="submit" variant="secondary">
          Apply
        </Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load the waves" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Pick waves"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          onRowOpen={(row) => navigate(`/fulfillment/waves/${row.id}`)}
          empty={
            filtered
              ? { title: "No waves match", description: "Change the status or picker filter." }
              : { title: "No waves yet", description: "Group released shipments into waves on the picking workbench." }
          }
        />
      )}
    </div>
  );
}
