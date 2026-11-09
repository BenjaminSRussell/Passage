import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { cancelManifest, describeProblem, getManifest, type Manifest, type ManifestParcel, type Problem } from "./api/client";
import { dash, formatDate, formatDateTime, shortId } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import { ManifestStatusBadge } from "./ManifestsPage";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const DUTY_SHIP = "fulfillment.warehouse.ship";

const parcelColumns: DataTableColumn<ManifestParcel>[] = [
  { key: "shipmentNumber", header: "Shipment", mono: true },
  { key: "trackingNumber", header: "Tracking number", mono: true },
  { key: "orderId", header: "Order", mono: true, render: (row) => shortId(row.orderId) },
  { key: "active", header: "On the manifest", render: (row) => (row.active ? "Yes" : "No, withdrawn") },
];

function explainCancel(problem: Problem): Problem {
  if (problem.code === "conflict_version") return { ...problem, message: "Someone else changed this manifest. Reload it and try again." };
  if (problem.code === "FUL-0001") return { ...problem, message: "This manifest is already cancelled. Reload to see its current state." };
  if (problem.code === "FUL-0078") return { ...problem, message: "The carrier has already collected parcels on this manifest, so it stays as the record of that pickup." };
  return problem;
}

function CancelPanel({ manifest, onDone }: { manifest: Manifest; onDone(): void }) {
  const [confirming, setConfirming] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  async function cancel() {
    setBusy(true);
    setFailure(null);
    try {
      await cancelManifest(manifest.id, manifest.version, key.current());
      key.renew();
      setConfirming(false);
      onDone();
    } catch (error) {
      const problem = explainCancel(describeProblem(error));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <ProblemAlert title="Could not cancel the manifest" problem={failure} />
      {confirming ? (
        <Alert tone="warning" title="Cancel this manifest?" live="status" action={<><Button size="sm" variant="danger" loading={busy} onClick={cancel}>Cancel manifest</Button><Button size="sm" variant="secondary" onClick={() => setConfirming(false)}>Keep it</Button></>}>
          Its {manifest.parcelCount} parcel{manifest.parcelCount === 1 ? "" : "s"} go back to the unmanifested list and the next manifest takes them.
        </Alert>
      ) : (
        <div className={styles.actions}>
          <Button variant="secondary" onClick={() => setConfirming(true)}>
            Cancel manifest
          </Button>
        </div>
      )}
    </div>
  );
}

/** Carrier manifest detail (FR-28.6): the pickup, its parcels, and the cancel for a closed one. */
export function ManifestDetailPage() {
  const { id = "" } = useParams();
  const canShip = holdsDuty(usePrincipalDuties(), DUTY_SHIP);
  const state = useLoad((signal) => getManifest(id, signal), id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Carrier manifest" />
        <Alert tone="danger" title="Could not load the manifest" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Carrier manifest" />
        <span className="visually-hidden">Loading the manifest…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const m = state.data;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Manifest ${m.manifestNumber}`}
        description={`${m.parcelCount} parcel${m.parcelCount === 1 ? "" : "s"} of ${m.shipmentCount} shipment${m.shipmentCount === 1 ? "" : "s"} for ${m.carrier}, collected on ${formatDate(m.pickupDate)}`}
        meta={<ManifestStatusBadge status={m.status} />}
        actions={
          <Link className={buttonClassName("secondary")} to="/fulfillment/manifests">
            All manifests
          </Link>
        }
      />
      <Card>
        <dl className={styles.details}>
          {[
            ["Carrier", m.carrier],
            ["Warehouse", m.warehouseId],
            ["Legal entity", m.legalEntityId],
            ["Pickup date", formatDate(m.pickupDate)],
            ["Carrier manifest reference", m.providerManifestId ?? dash],
            ["Closed", formatDateTime(m.createdAt)],
            ["Cancelled", m.cancelledAt ? formatDateTime(m.cancelledAt) : dash],
            ["Version", String(m.version)],
          ].map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={`${styles.value} ${styles.mono}`}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <Card title="Parcels">
        <DataTable label="Manifest parcels" columns={parcelColumns} rows={m.parcels} getRowKey={(row) => `${row.shipmentId}-${row.trackingNumber}`} empty={{ title: "No parcels", description: "This manifest holds no parcel." }} />
      </Card>
      {m.status === "CLOSED" && canShip ? <CancelPanel key={m.version} manifest={m} onDone={state.reload} /> : null}
      {m.status === "CLOSED" && !canShip ? <p className={styles.note}>Cancelling a manifest needs the shipping duty.</p> : null}
      {m.status === "CANCELLED" ? <p className={styles.note}>This manifest was cancelled; its parcels are available to the next manifest.</p> : null}
    </div>
  );
}
