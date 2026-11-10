import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listCapacityBlocks, listResources, type CapacityBlock, type Predicate, type Resource } from "./api/client";
import { describeShifts, formatDateTime, shortId } from "./format";
import { describeRecurrence } from "./NewCapacityBlockPage";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** "color = white and grade >= 2"; an empty condition admits every job. */
export function describeCondition(condition: Predicate[]): string {
  return condition.length === 0 ? "Any job" : condition.map((p) => `${p.attribute} ${p.op} ${p.value}`).join(" and ");
}

export function describeWindow(b: CapacityBlock): string {
  if (b.windows && b.windows.length > 0) return `Weekly: ${describeShifts(b.windows)}`;
  if (b.recurrence) return `Repeats ${describeRecurrence(b.recurrence)}`;
  return `${formatDateTime(b.startAt)} to ${formatDateTime(b.endAt)}`;
}

interface View {
  rows: CapacityBlock[];
  resources: Resource[];
}

/** Capacity reserved for jobs whose attributes match a condition (FR-23.17). */
export function CapacityBlocksPage() {
  const navigate = useNavigate();
  const [resourceId, setResourceId] = useState("");
  const state = useLoad<View>(async (signal) => {
    const [rows, resources] = await Promise.all([listCapacityBlocks(resourceId, signal), listResources(signal)]);
    return { rows, resources };
  }, resourceId);
  const codes: Record<string, string> = Object.fromEntries((state.data?.resources ?? []).map((r) => [r.id, `${r.name} (${r.code})`]));

  const columns: DataTableColumn<CapacityBlock>[] = [
    { key: "resourceId", header: "Resource", render: (b) => codes[b.resourceId] ?? shortId(b.resourceId) },
    { key: "code", header: "Code", mono: true },
    { key: "window", header: "When", render: (b) => describeWindow(b) },
    { key: "condition", header: "Condition", mono: true, render: (b) => describeCondition(b.condition) },
    { key: "priority", header: "Priority", align: "end" },
    { key: "active", header: "Status", render: (b) => <Badge tone={b.active ? "success" : "neutral"}>{b.active ? "Active" : "Inactive"}</Badge> },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Capacity blocks"
        description="Time on a resource reserved for jobs that match a condition, such as Monday mornings for white variants, every week, by a recurrence rule or on dates. Higher priority wins where blocks overlap."
        actions={
          <Link className={buttonClassName("primary")} to="/scheduling/capacity-blocks/new">
            New block
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="blocks-resource" label="Resource">
          {(control) => (
            <Select {...control} value={resourceId} onChange={(event) => setResourceId(event.target.value)}>
              <option value="">All resources</option>
              {(state.data?.resources ?? []).map((r) => (
                <option key={r.id} value={r.id}>
                  {r.code}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load capacity blocks" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Capacity blocks"
          columns={columns}
          rows={state.data?.rows ?? []}
          getRowKey={(b) => b.id}
          loading={state.loading}
          onRowOpen={(b) => navigate(`/scheduling/capacity-blocks/${b.id}`)}
          empty={{ title: "No capacity blocks yet", description: "Without blocks every job may use all open capacity." }}
        />
      )}
    </div>
  );
}
