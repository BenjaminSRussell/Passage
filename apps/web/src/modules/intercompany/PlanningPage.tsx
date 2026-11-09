import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { sequencePlanning, type PlanningRequest, type PlanningSequence } from "./api/client";
import { isCount, isDate, isPositive } from "./format";
import { RowGrid, type RowField } from "./RowGrid";
import { usePreview, type Checked } from "./usePreview";
import styles from "./pages.module.css";

type MemberKey = "entityId" | "sequence";
type EdgeKey = "buyer" | "supplier" | "leadTimeDays";
type OrderKey = "buyer" | "supplier" | "item" | "quantity" | "requestedDate";

export interface PlanningValues {
  members: Record<MemberKey, string>[];
  edges: Record<EdgeKey, string>[];
  orders: Record<OrderKey, string>[];
  replannedBuyer: string;
}

const memberFields: RowField<MemberKey>[] = [
  { name: "entityId", label: "Entity", mono: true },
  { name: "sequence", label: "Explicit sequence", optional: true, decimal: true },
];
const edgeFields: RowField<EdgeKey>[] = [
  { name: "buyer", label: "Buyer", mono: true },
  { name: "supplier", label: "Supplier", mono: true },
  { name: "leadTimeDays", label: "Transport lead time (days)", optional: true, decimal: true },
];
const orderFields: RowField<OrderKey>[] = [
  { name: "buyer", label: "Buyer", mono: true },
  { name: "supplier", label: "Supplier", mono: true },
  { name: "item", label: "Item", mono: true },
  { name: "quantity", label: "Quantity", decimal: true },
  { name: "requestedDate", label: "Requested date", type: "date" },
];

/** AC-40.16: buyer A plans 100 units from supplier B for 2026-12-10 with 3 days transport; B buys from C. */
export const ac4016: PlanningValues = {
  members: [
    { entityId: "A", sequence: "" },
    { entityId: "B", sequence: "" },
    { entityId: "C", sequence: "" },
  ],
  edges: [
    { buyer: "A", supplier: "B", leadTimeDays: "3" },
    { buyer: "B", supplier: "C", leadTimeDays: "1" },
  ],
  orders: [{ buyer: "A", supplier: "B", item: "PUMP-100", quantity: "100", requestedDate: "2026-12-10" }],
  replannedBuyer: "",
};

/** Checks that mirror the service, and the request the form sends. */
export function validatePlanning(values: PlanningValues): Checked<PlanningRequest> {
  const errors: Record<string, string> = {};
  const known = new Set<string>();
  const members = values.members.map((m, i) => {
    const id = m.entityId.trim();
    if (id === "") errors[`members[${i}].entityId`] = "Enter the entity.";
    else if (known.has(id)) errors[`members[${i}].entityId`] = "This entity is already in the group.";
    known.add(id);
    const seq = m.sequence.trim();
    if (seq !== "" && !isCount(seq)) errors[`members[${i}].sequence`] = "Enter a whole number, or leave it empty.";
    return seq === "" || seq === "0" ? { entityId: id } : { entityId: id, sequence: Number(seq) };
  });
  if (members.length === 0) errors.members = "Add at least one entity.";
  const member = (key: string, raw: string) => {
    const v = raw.trim();
    if (v === "") errors[key] = "Enter the entity.";
    else if (!known.has(v)) errors[key] = "Add this entity to the group first.";
    return v;
  };
  const edges = values.edges.map((e, i) => {
    const buyer = member(`edges[${i}].buyer`, e.buyer);
    const supplier = member(`edges[${i}].supplier`, e.supplier);
    if (buyer !== "" && buyer === supplier) errors[`edges[${i}].supplier`] = "An entity cannot buy from itself.";
    const lead = e.leadTimeDays.trim();
    if (lead !== "" && !isCount(lead)) errors[`edges[${i}].leadTimeDays`] = "Enter whole days, 0 or more.";
    return lead === "" ? { buyer, supplier } : { buyer, supplier, leadTimeDays: Number(lead) };
  });
  const plannedOrders = values.orders.map((o, i) => {
    const at = (k: OrderKey) => `plannedOrders[${i}].${k}`;
    const buyer = member(at("buyer"), o.buyer);
    const supplier = member(at("supplier"), o.supplier);
    if (!errors[at("supplier")] && buyer !== "" && !edges.some((e) => e.buyer === buyer && e.supplier === supplier))
      errors[at("supplier")] = "Add a planning edge from the buyer to this supplier.";
    if (o.item.trim() === "") errors[at("item")] = "Enter the item.";
    if (!isPositive(o.quantity)) errors[at("quantity")] = "Enter a quantity above 0.";
    if (!isDate(o.requestedDate)) errors[at("requestedDate")] = "Enter the requested date.";
    return { buyer, supplier, item: o.item.trim(), quantity: o.quantity.trim(), requestedDate: o.requestedDate.trim() };
  });
  const replanned = values.replannedBuyer.trim();
  if (replanned !== "" && !known.has(replanned)) errors.replannedBuyer = "Enter an entity of the group, or leave it empty.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, request: { members, edges, plannedOrders, replannedBuyer: replanned || undefined } };
}

interface OrderRow {
  position: number;
  entity: string;
}

const orderColumns: DataTableColumn<OrderRow>[] = [
  { key: "position", header: "Run position", align: "end" },
  { key: "entity", header: "Entity", mono: true },
];

type DemandRow = PlanningSequence["demand"][number];

const demandColumns: DataTableColumn<DemandRow>[] = [
  { key: "supplier", header: "Supplier", mono: true },
  { key: "buyer", header: "From buyer", mono: true },
  { key: "item", header: "Item", mono: true },
  { key: "quantity", header: "Quantity", align: "end" },
  { key: "dueDate", header: "Due date" },
];

/** Plan the order in which a planning group's entities run and the IC demand each supplier sees (FR-40.15, BR-40.13, AC-40.16). */
export function PlanningPage() {
  const [values, setValues] = useState<PlanningValues>(ac4016);
  const { errors, failure, busy, result, run } = usePreview(sequencePlanning);

  function submit(event: FormEvent) {
    event.preventDefault();
    void run(validatePlanning(values));
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Intercompany planning"
        description="Buyers are planned before their suppliers, so a planned intercompany purchase order becomes demand in the supplying entity before that entity runs, due on the requested date less the transport lead time. Entities that buy from each other need an explicit sequence. Running master planning for each entity and firming the demand into mirror orders arrive with the planning slice."
      />
      <Card title="Planning group" headingLevel={3}>
        <form className={styles.form} onSubmit={submit} noValidate>
          <h4 className={styles.section}>Entities</h4>
          <RowGrid
            idPrefix="pl-member"
            path="members"
            noun="Entity"
            fields={memberFields}
            rows={values.members}
            errors={errors}
            newRow={() => ({ entityId: "", sequence: "" })}
            onChange={(members) => setValues({ ...values, members })}
          />
          <h4 className={styles.section}>Who buys from whom</h4>
          <RowGrid
            idPrefix="pl-edge"
            path="edges"
            noun="Edge"
            fields={edgeFields}
            rows={values.edges}
            errors={errors}
            newRow={() => ({ buyer: "", supplier: "", leadTimeDays: "" })}
            onChange={(edges) => setValues({ ...values, edges })}
          />
          <h4 className={styles.section}>Planned intercompany orders</h4>
          <RowGrid
            idPrefix="pl-order"
            path="plannedOrders"
            noun="Planned order"
            fields={orderFields}
            rows={values.orders}
            errors={errors}
            newRow={() => ({ buyer: "", supplier: "", item: "", quantity: "", requestedDate: "" })}
            onChange={(orders) => setValues({ ...values, orders })}
          />
          <FormField
            id="pl-replanned"
            label="Buyer replanned after the run"
            optional
            hint="Lists the suppliers whose plan is now stale (IC-0815)."
            error={errors.replannedBuyer}
          >
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={values.replannedBuyer}
                onChange={(e) => setValues({ ...values, replannedBuyer: e.target.value })}
                invalid={Boolean(errors.replannedBuyer)}
              />
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The planning group was not sequenced" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Sequence group
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Run order" columns={orderColumns} rows={[]} getRowKey={(r) => r.entity} loading />
      ) : result ? (
        <>
          {result.warnings.map((w) => (
            <Alert key={w.message} tone="warning" title="Stale supplier plan">
              {w.message} ({w.code})
            </Alert>
          ))}
          <DataTable
            label="Run order"
            columns={orderColumns}
            rows={result.order.map((entity, i) => ({ position: i + 1, entity }))}
            getRowKey={(r) => r.entity}
          />
          <DataTable
            label="Intercompany demand"
            columns={demandColumns}
            rows={result.demand}
            getRowKey={(d, i) => `${d.supplier}-${d.item}-${i}`}
            empty={{ title: "No intercompany demand", description: "Add a planned intercompany order to see the demand it creates." }}
          />
        </>
      ) : (
        <Card>
          <EmptyState
            icon="workflow"
            headingLevel={3}
            title="No run order yet"
            description="The form is filled in with an example. Sequence it, or enter your own planning group."
          />
        </Card>
      )}
    </div>
  );
}
