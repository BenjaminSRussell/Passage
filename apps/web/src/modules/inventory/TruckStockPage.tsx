import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { LookupFormField } from "../../shell/lookup/LookupFormField";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listTruckStockLevels, replenishTrucks, type Problem, type TransferOrder, type TruckStockLevel } from "./api/client";
import { UUID } from "./format";
import { ProblemAlert } from "./parts";
import { Named } from "../purchasing/Named";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const DUTY_TRANSFER_WRITE = "inventory.transfer.write";
export const DUTY_TRUCK_LEVELS_WRITE = "inventory.truck-stock.write";

const columns: DataTableColumn<TruckStockLevel>[] = [
  { key: "warehouseId", header: "Truck", render: (row) => <Named entity="warehouse" id={row.warehouseId} /> },
  { key: "productId", header: "Product", render: (row) => <Named entity="product" id={row.productId} /> },
  { key: "unit", header: "Unit", mono: true },
  { key: "minQty", header: "Minimum", align: "end", mono: true },
  { key: "maxQty", header: "Maximum", align: "end", mono: true },
  { key: "onHandQty", header: "On the truck", align: "end", mono: true },
  { key: "inboundQty", header: "On its way", align: "end", mono: true },
  { key: "sourceWarehouseId", header: "Refilled from", render: (row) => (UUID.test(row.sourceWarehouseId) ? <Named entity="warehouse" id={row.sourceWarehouseId} /> : <span className={styles.mono}>{row.sourceWarehouseId}</span>) },
  { key: "short", header: "Status", render: (row) => <Badge tone={row.short ? "warning" : "success"}>{row.short ? "Below minimum" : "Stocked"}</Badge> },
];

/**
 * Truck stock (FR-45.9, BR-45.7): the minimum and maximum a technician's truck is kept at, what it holds and what is on its
 * way, with a button that creates the draft transfer orders for trucks below their minimum.
 */
export function TruckStockPage() {
  const [params] = useSearchParams();
  const [legalEntityId, setLegalEntityId] = useState(params.get("legalEntityId") ?? "");
  const [warehouseId, setWarehouseId] = useState(params.get("warehouseId") ?? "");
  const entity = UUID.test(legalEntityId.trim()) ? legalEntityId.trim() : "";
  const truck = UUID.test(warehouseId.trim()) ? warehouseId.trim() : "";
  const duties = usePrincipalDuties();
  const canReplenish = holdsDuty(duties, DUTY_TRANSFER_WRITE);
  const canSetLevels = holdsDuty(duties, DUTY_TRUCK_LEVELS_WRITE);
  const levelsHref = `/inventory/truck-stock/levels${entity ? `?${new URLSearchParams(truck ? { legalEntityId: entity, warehouseId: truck } : { legalEntityId: entity }).toString()}` : ""}`;
  const load = useLoad((signal) => (entity ? listTruckStockLevels(entity, truck, signal) : Promise.resolve([])), `${entity}|${truck}`);
  const rows = load.data ?? [];
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [created, setCreated] = useState<TransferOrder[] | null>(null);
  const shortRows = rows.filter((row) => row.short).length;

  async function replenish() {
    setBusy(true);
    setFailure(null);
    setCreated(null);
    try {
      const first = rows.find((row) => row.warehouseId === truck);
      setCreated(await replenishTrucks(entity, truck && first ? { siteId: first.siteId, warehouseId: first.warehouseId } : undefined));
      load.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Truck stock"
        description="The stock each technician's truck is kept at. A truck below its minimum is refilled from its source warehouse up to its maximum with a draft transfer order."
        actions={
          <>
            {canSetLevels ? (
              <Link className={buttonClassName("primary")} to={levelsHref}>
                Set truck levels
              </Link>
            ) : null}
            <Link className={buttonClassName("ghost")} to="/inventory/transfer-orders">
              Transfer orders
            </Link>
          </>
        }
      />
      <div className={styles.toolbar}>
        <LookupFormField id="truck-entity" label="Legal entity" entity="legalEntity" value={legalEntityId} onChange={setLegalEntityId} />
        <LookupFormField id="truck-warehouse" label="Truck" entity="warehouse" optional prefill={false} value={warehouseId} onChange={setWarehouseId} />
        {canReplenish ? (
          <Button variant="primary" loading={busy} disabled={entity === "" || shortRows === 0} onClick={() => void replenish()}>
            Refill trucks below minimum
          </Button>
        ) : null}
      </div>
      {failure ? <ProblemAlert title="Could not create the transfer orders" problem={failure} /> : null}
      {created ? (
        <Alert tone={created.length > 0 ? "success" : "info"} title={created.length > 0 ? `${created.length} draft transfer ${created.length === 1 ? "order" : "orders"} created` : "No truck needed a refill"}>
          {created.length > 0 ? (
            <>
              The trucks below their minimum are on their way back up. <Link to="/inventory/transfer-orders">Review the transfer orders</Link>.
            </>
          ) : (
            "Every truck holds at least its minimum, counting what is already on its way."
          )}
        </Alert>
      ) : null}
      {entity === "" ? (
        <Alert tone="info" title="Choose a legal entity">
          Pick the legal entity whose trucks you want to see.
        </Alert>
      ) : load.error ? (
        <ProblemAlert title="Could not load the truck stock levels" problem={load.error} onRetry={load.reload} />
      ) : (
        <DataTable
          label="Truck stock levels"
          columns={columns}
          rows={rows}
          getRowKey={(row) => `${row.siteId}|${row.warehouseId}|${row.productId}`}
          loading={load.loading}
          empty={{ title: "No truck levels yet", description: canSetLevels ? "Use Set truck levels to give a truck the parts it carries." : "Someone who maintains truck stock sets the parts each truck carries." }}
        />
      )}
    </div>
  );
}
