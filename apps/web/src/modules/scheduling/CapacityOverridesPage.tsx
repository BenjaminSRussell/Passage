import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { deleteCapacityOverride, describeProblem, listCapacityOverrides, type CapacityOverride, type Problem } from "./api/client";
import { formatDateTime, formatMinutes } from "./format";
import styles from "./pages.module.css";
import { ResourceGate } from "./ResourceGate";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

function span(o: CapacityOverride): string {
  return formatMinutes(Math.round((Date.parse(o.end) - Date.parse(o.start)) / 60_000));
}

function Overrides({ resourceId }: { resourceId: string }) {
  const state = useLoad((signal) => listCapacityOverrides(resourceId, signal), resourceId);
  const [removing, setRemoving] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");

  async function remove(o: CapacityOverride) {
    setRemoving(o.id);
    setFailure(null);
    setNotice("");
    try {
      await deleteCapacityOverride(o.resourceId, o.id);
      setNotice(`The ${o.kind === "downtime" ? "downtime" : "extra shift"} starting ${formatDateTime(o.start)} was removed.`);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setRemoving(null);
    }
  }

  const columns: DataTableColumn<CapacityOverride>[] = [
    {
      key: "kind",
      header: "Kind",
      render: (o) => <Badge tone={o.kind === "downtime" ? "danger" : "success"}>{o.kind === "downtime" ? "Downtime" : "Extra shift"}</Badge>,
    },
    { key: "start", header: "From", render: (o) => formatDateTime(o.start) },
    { key: "end", header: "To", render: (o) => formatDateTime(o.end) },
    { key: "span", header: "Length", align: "end", render: span },
    { key: "reason", header: "Reason", render: (o) => o.reason || "—" },
    {
      key: "actions",
      header: "Actions",
      render: (o) => (
        <Button
          size="sm"
          variant="ghost"
          loading={removing === o.id}
          onClick={() => remove(o)}
          aria-label={`Remove ${o.kind === "downtime" ? "downtime" : "extra shift"} from ${formatDateTime(o.start)}`}
        >
          Remove
        </Button>
      ),
    },
  ];

  return (
    <>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert tone="danger" title="The override was not removed" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load capacity overrides" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Capacity overrides"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(o) => o.id}
          loading={state.loading}
          empty={{
            title: "No downtimes or extra shifts",
            description: "This resource follows its weekly calendar. Add a downtime for a breakdown or an extra shift for overtime.",
          }}
        />
      )}
    </>
  );
}

/**
 * Downtimes and extra shifts over a resource's weekly calendar (FR-23.1).
 * Runs, moves and the capacity views all read the calendar with these applied.
 */
export function CapacityOverridesPage() {
  const [params, setParams] = useSearchParams();
  const chosen = params.get("resourceId") ?? "";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Capacity overrides"
        description="A downtime closes part of a resource's calendar and an extra shift opens more. Jobs already booked in a downtime are listed when it is created."
        actions={
          <Link className={buttonClassName("primary")} to={`/scheduling/capacity-overrides/new${chosen ? `?resourceId=${encodeURIComponent(chosen)}` : ""}`}>
            New override
          </Link>
        }
      />
      <ResourceGate need="Overrides change the calendar of a resource.">
        {(resources) => {
          const resourceId = resources.some((r) => r.id === chosen) ? chosen : resources[0].id;
          return (
            <>
              <div className={styles.toolbar}>
                <FormField id="override-resource" label="Resource">
                  {(control) => (
                    <Select {...control} value={resourceId} onChange={(event) => setParams({ resourceId: event.target.value })}>
                      {resources.map((r) => (
                        <option key={r.id} value={r.id}>{`${r.code} · ${r.name}`}</option>
                      ))}
                    </Select>
                  )}
                </FormField>
              </div>
              <Overrides resourceId={resourceId} />
            </>
          );
        }}
      </ResourceGate>
    </div>
  );
}
