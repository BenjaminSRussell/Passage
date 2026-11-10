import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, DataTable, FormField, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { ProblemMessage } from "../../shell/errors";
import { OrderNumber } from "../mfg-shared/orders";
import { ResourceName } from "../mfg-shared/resources";
import { listControlRecipes, type ControlRecipe, type RecipeDestination, type RecipeStatus } from "./api/client";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const RECIPE_STATUS: Record<RecipeStatus, { label: string; tone: BadgeTone }> = {
  sent: { label: "Sent", tone: "info" },
  received: { label: "Received", tone: "info" },
  "in-execution": { label: "In execution", tone: "info" },
  completed: { label: "Completed", tone: "success" },
  error: { label: "Error", tone: "danger" },
};

export const DESTINATION_LABEL: Record<RecipeDestination, string> = {
  kiosk: "Kiosk",
  "pcs-opcua": "Process control (OPC-UA)",
  "pcs-mqtt": "Process control (MQTT)",
};

export function RecipeStatusBadge({ status }: { status: RecipeStatus }) {
  const known = RECIPE_STATUS[status];
  return <Badge tone={known?.tone ?? "neutral"}>{known?.label ?? status}</Badge>;
}

const columns: DataTableColumn<ControlRecipe>[] = [
  { key: "orderId", header: "Order", render: (row) => <Link to={`/shopfloor/control-recipes/${row.id}`}><OrderNumber id={row.orderId} /></Link> },
  { key: "resourceId", header: "Resource", render: (row) => <ResourceName id={row.resourceId} /> },
  { key: "destination", header: "Destination", render: (row) => DESTINATION_LABEL[row.destination] ?? row.destination },
  { key: "status", header: "Status", render: (row) => <RecipeStatusBadge status={row.status} /> },
  { key: "sendCount", header: "Sent", align: "end", render: (row) => (row.sendCount === 1 ? "Once" : `${row.sendCount} times`) },
  { key: "lastSentAt", header: "Last sent", mono: true, render: (row) => formatDateTime(row.lastSentAt) },
  { key: "lastError", header: "Last error", render: (row) => row.lastError ?? "None" },
];

/** The control recipe monitor (FR-25.15): every recipe sent to a kiosk or a process control system, with its status. */
export function ControlRecipesPage() {
  const [status, setStatus] = useState<RecipeStatus | "">("");
  const [destination, setDestination] = useState<RecipeDestination | "">("");
  const state = useLoad(
    (signal) => listControlRecipes({ status: status || undefined, destination: destination || undefined }, signal),
    `control-recipes:${status}:${destination}`,
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Control recipes"
        description="The recipe of each released order, sent to a kiosk or to a process control system through its edge connector."
      />
      <div className={styles.toolbar}>
        <FormField id="recipe-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as RecipeStatus | "")}>
              <option value="">Any status</option>
              {Object.entries(RECIPE_STATUS).map(([value, { label }]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="recipe-destination" label="Destination">
          {(control) => (
            <Select {...control} value={destination} onChange={(event) => setDestination(event.target.value as RecipeDestination | "")}>
              <option value="">Any destination</option>
              {Object.entries(DESTINATION_LABEL).map(([value, label]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load control recipes" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Control recipes"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No control recipes", description: "A recipe appears here when an order with process instructions is released and sent." }}
        />
      )}
    </div>
  );
}
