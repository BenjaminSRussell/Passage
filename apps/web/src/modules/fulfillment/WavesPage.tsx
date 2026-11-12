import { Link, useNavigate } from "react-router-dom";
import { buttonClassName, ListPage, loadedRows, type DataTableColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { listWaves, type WaveSummary } from "./api/client";
import { dash, formatDateTime, GROUP_LABELS } from "./format";
import { useLoad } from "./useLoad";
import { WaveStatusBadge } from "./WaveActions";
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

const filters: ListFilterDef[] = [
  { kind: "select", key: "status", label: "Status", anyLabel: "All", options: [{ value: "OPEN", label: "Open" }, { value: "CLOSED", label: "Closed" }] },
  { kind: "text", key: "picker", label: "Picker" },
];

/** The waves of the filter, newest first; the service answers one page of at most 200. A picker of "-" is nobody yet. */
function useWaveRows(query: ListQuery): ListData<WaveSummary> {
  const status = (query.filters.status ?? "") as "" | "OPEN" | "CLOSED";
  const picker = (query.filters.picker ?? "").trim();
  const unassigned = picker === "-";
  const state = useLoad((signal) => listWaves({ status, picker: unassigned ? undefined : picker, unassigned, limit: 200 }, signal), `waves-${status}-${picker}`);
  return loadedRows(state);
}

/** Pick waves list (section 5.28.14 "Pick work / waves"), newest first, filtered by status and picker ("-" for waves nobody has yet). */
export function WavesPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="fulfillment-waves"
      title="Pick waves"
      description="Waves created by the picking workbench. Open one to assign a picker, pick, print the pick list and close it. Filter the picker by - for waves nobody has yet."
      actions={
        <Link className={buttonClassName("primary")} to="/fulfillment/workbench">
          Picking workbench
        </Link>
      }
      noun="pick waves"
      singular="pick wave"
      columns={columns}
      filters={filters}
      useRows={useWaveRows}
      search={(row) => ({ number: row.waveNumber, name: row.picker ?? undefined })}
      getRowKey={(row) => row.id}
      onRowOpen={(row) => navigate(`/fulfillment/waves/${row.id}`)}
      empty={{ title: "No waves yet", description: "Group released shipments into waves on the picking workbench." }}
    />
  );
}
