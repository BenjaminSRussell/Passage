import { Alert, Button, Card, DataTable, EmptyState, type DataTableColumn } from "../../shell/ui";
import { listOrderVersions, type OrderVersion } from "./api/client";
import { diffColumns } from "./ChangeOrderPage";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const versionColumns: DataTableColumn<OrderVersion>[] = [
  { key: "version", header: "Version", align: "end" },
  { key: "changedAt", header: "Changed", render: (v) => `${formatDateTime(v.changedAt)} by ${v.actor}` },
  { key: "reason", header: "Reason", render: (v) => v.reason || "—" },
  { key: "fields", header: "Fields", align: "end", render: (v) => String(v.diff.length) },
];

/**
 * The order's change history (FR-27.9, PRD §5.27.14): each version with who,
 * when, why, and the field-level diff of the newest one. Reloads when the
 * order's version changes.
 */
export function OrderHistory({ orderId, version }: { orderId: string; version: number }) {
  const state = useLoad((signal) => listOrderVersions(orderId, signal), `${orderId}:${version}`);
  const versions = state.data ?? [];
  const newest = versions[0];
  return (
    <Card title="Change history">
      {state.error ? (
        <Alert tone="danger" title="Could not load the change history" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : !state.loading && versions.length === 0 ? (
        <EmptyState compact headingLevel={3} title="No changes yet" description="Each change or cancellation after entry is recorded here as a new version." />
      ) : (
        <div className={styles.stack}>
          <DataTable label="Versions" columns={versionColumns} rows={versions} getRowKey={(v) => String(v.version)} loading={state.loading && versions.length === 0} />
          {newest ? (
            <DataTable label={`Changes in version ${newest.version}`} columns={diffColumns} rows={newest.diff} getRowKey={(c, i) => `${c.lineNo}-${c.field}-${i}`} />
          ) : null}
        </div>
      )}
    </Card>
  );
}
