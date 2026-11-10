import { useMemo, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { closeManifest, describeProblem, listManifestCandidates, listManifests, MANIFEST_STATUSES, type Manifest, type ManifestCandidate, type ManifestStatus, type Problem } from "./api/client";
import { dash, formatDate, formatDateTime, shortId, today } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const DUTY_SHIP = "fulfillment.warehouse.ship";
/** Carrier codes are lowercase letters, digits and hyphens (the label's provider). */
const CARRIER_CODE = /^[A-Za-z0-9-]{2,32}$/;
/** The most unmanifested parcels the page asks for (listManifestCandidates). */
const CANDIDATE_LIMIT = 500;

const TONES: Record<ManifestStatus, BadgeTone> = { CLOSED: "success", CANCELLED: "neutral" };
const LABELS: Record<ManifestStatus, string> = { CLOSED: "Closed", CANCELLED: "Cancelled" };

export function ManifestStatusBadge({ status }: { status: ManifestStatus }) {
  return <Badge tone={TONES[status] ?? "neutral"}>{LABELS[status] ?? status}</Badge>;
}

const columns: DataTableColumn<Manifest>[] = [
  { key: "manifestNumber", header: "Manifest", mono: true },
  { key: "status", header: "Status", render: (row) => <ManifestStatusBadge status={row.status} /> },
  { key: "carrier", header: "Carrier", mono: true },
  { key: "warehouseId", header: "Warehouse", mono: true, render: (row) => shortId(row.warehouseId) },
  { key: "pickupDate", header: "Pickup", render: (row) => formatDate(row.pickupDate) },
  { key: "parcelCount", header: "Parcels", align: "end" },
  { key: "shipmentCount", header: "Shipments", align: "end" },
  { key: "providerManifestId", header: "Carrier reference", render: (row) => row.providerManifestId ?? dash },
];

const candidateColumns: DataTableColumn<ManifestCandidate>[] = [
  { key: "shipmentNumber", header: "Shipment", mono: true },
  { key: "carrier", header: "Carrier", mono: true },
  { key: "trackingNumber", header: "Tracking number", mono: true },
  { key: "warehouseId", header: "Warehouse", mono: true, render: (row) => shortId(row.warehouseId) },
  { key: "labelledAt", header: "Labelled", render: (row) => formatDateTime(row.labelledAt) },
  { key: "ready", header: "Ready", render: (row) => (row.ready ? <Badge tone="success">Shipped</Badge> : <Badge tone="warning">Waiting for packing slip</Badge>) },
];

/** The warehouse, legal entity and carrier a manifest is closed for, with the parcels ready for it. */
interface Group {
  key: string;
  warehouseId: string;
  legalEntityId: string;
  carrier: string;
  ready: number;
  waiting: number;
}

/** Groups the unmanifested parcels by (warehouse, legal entity, carrier). */
export function groupCandidates(rows: readonly ManifestCandidate[]): Group[] {
  const groups = new Map<string, Group>();
  for (const row of rows) {
    const key = `${row.warehouseId}|${row.legalEntityId}|${row.carrier}`;
    const group = groups.get(key) ?? { key, warehouseId: row.warehouseId, legalEntityId: row.legalEntityId, carrier: row.carrier, ready: 0, waiting: 0 };
    if (row.ready) group.ready += 1;
    else group.waiting += 1;
    groups.set(key, group);
  }
  return [...groups.values()].sort((a, b) => a.key.localeCompare(b.key));
}

function explainClose(problem: Problem): Problem {
  if (problem.code === "FUL-0076") return { ...problem, message: "No shipped parcel of this carrier is waiting in the warehouse. Post the packing slips of the labelled shipments first, then close the manifest." };
  if (problem.code === "FUL-0079") return { ...problem, message: "Someone closed a manifest with some of these parcels at the same time. Reload and close again." };
  return problem;
}

function CloseCard({ groups, onDone }: { groups: Group[]; onDone(manifest: Manifest): void }) {
  const closable = groups.filter((g) => g.ready > 0);
  const [choice, setChoice] = useState("");
  const [pickupDate, setPickupDate] = useState(today());
  const [reference, setReference] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();
  const group = closable.find((g) => g.key === choice) ?? closable[0];

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const found: Record<string, string> = {};
    if (!group) found.group = "There is no shipped parcel to put on a manifest.";
    if (!/^\d{4}-\d{2}-\d{2}$/.test(pickupDate)) found.pickupDate = "Enter the pickup date.";
    if ([...reference.trim()].length > 128) found.reference = "Use at most 128 characters.";
    setErrors(found);
    if (Object.keys(found).length > 0 || !group) return;
    setBusy(true);
    try {
      const made = await closeManifest(
        { warehouseId: group.warehouseId, legalEntityId: group.legalEntityId, carrier: group.carrier, pickupDate, ...(reference.trim() ? { providerManifestId: reference.trim() } : {}) },
        key.current(),
      );
      key.renew();
      setReference("");
      onDone(made);
    } catch (error) {
      const problem = explainClose(describeProblem(error));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Close a manifest">
      <form className={styles.form} onSubmit={submit} noValidate>
        <p className={styles.note}>
          Closing takes every shipped, labelled parcel of the carrier in the warehouse. Parcels still waiting for their packing slip stay out. The manifest is a record here: the carrier is not called.
        </p>
        <FormField id="manifest-group" label="Warehouse and carrier" error={errors.group}>
          {(control) => (
            <Select {...control} value={group?.key ?? ""} disabled={closable.length === 0} onChange={(event) => { setChoice(event.target.value); key.renew(); }}>
              {closable.length === 0 ? <option value="">No shipped parcel waiting</option> : null}
              {closable.map((g) => (
                <option key={g.key} value={g.key}>
                  {`${g.carrier}, warehouse ${shortId(g.warehouseId)}, ${g.ready} parcel${g.ready === 1 ? "" : "s"} ready`}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="manifest-pickup" label="Pickup date" error={errors.pickupDate}>
          {(control) => <Input {...control} type="date" value={pickupDate} invalid={Boolean(errors.pickupDate)} onChange={(event) => { setPickupDate(event.target.value); key.renew(); }} />}
        </FormField>
        <FormField id="manifest-reference" label="Carrier manifest reference" error={errors.reference} hint="Optional: the carrier's own reference for this pickup.">
          {(control) => <Input {...control} value={reference} maxLength={128} invalid={Boolean(errors.reference)} onChange={(event) => { setReference(event.target.value); key.renew(); }} />}
        </FormField>
        <ProblemAlert title="Could not close the manifest" problem={failure} />
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy} disabled={closable.length === 0}>
            Close manifest
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** Carrier manifests (FR-28.6): the closed ones, the parcels still missing from a pickup, and the form that closes one. */
export function ManifestsPage() {
  const navigate = useNavigate();
  const canShip = holdsDuty(usePrincipalDuties(), DUTY_SHIP);
  const [status, setStatus] = useState<ManifestStatus | "">("");
  const [carrier, setCarrier] = useState("");
  const carrierOk = carrier === "" || CARRIER_CODE.test(carrier.trim());
  const manifests = useLoad((signal) => listManifests({ status, carrier: carrierOk ? carrier.trim().toLowerCase() : "" }, signal), `manifests-${status}-${carrier}`);
  const candidates = useLoad((signal) => listManifestCandidates(signal), "manifest-candidates");
  const [closed, setClosed] = useState<Manifest | null>(null);
  const groups = useMemo(() => groupCandidates(candidates.data ?? []), [candidates.data]);

  function done(made: Manifest) {
    setClosed(made);
    manifests.reload();
    candidates.reload();
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Carrier manifests"
        description="The end-of-day close-out of the labelled parcels a carrier collects from a warehouse, so the carrier takes exactly what was handed over."
      />
      {closed ? (
        <p role="status" className={styles.note}>
          Closed {closed.manifestNumber} with {closed.parcelCount} parcel{closed.parcelCount === 1 ? "" : "s"} for {closed.carrier}.
        </p>
      ) : null}
      <div className={styles.toolbar}>
        <FormField id="manifest-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as ManifestStatus | "")}>
              <option value="">All statuses</option>
              {MANIFEST_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {LABELS[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="manifest-carrier" label="Carrier" error={carrierOk ? undefined : "Use 2 to 32 letters, digits or hyphens."}>
          {(control) => <Input {...control} value={carrier} maxLength={32} invalid={!carrierOk} onChange={(event) => setCarrier(event.target.value)} />}
        </FormField>
      </div>
      {manifests.error ? (
        <Alert tone="danger" title="Could not load the manifests" live="alert" action={<Button size="sm" onClick={manifests.reload}>Try again</Button>}>
          <ProblemMessage problem={manifests.error} />
        </Alert>
      ) : (
        <DataTable
          label="Carrier manifests"
          columns={columns}
          rows={manifests.data ?? []}
          getRowKey={(row) => row.id}
          loading={manifests.loading}
          onRowOpen={(row) => navigate(`/fulfillment/manifests/${row.id}`)}
          empty={
            status || carrier
              ? { title: "No manifests match", description: "Change the filters." }
              : { title: "No manifests yet", description: "Close one once labelled shipments have shipped." }
          }
        />
      )}
      <Card title="Parcels on no manifest">
        {candidates.error ? (
          <Alert tone="danger" title="Could not load the unmanifested parcels" live="alert" action={<Button size="sm" onClick={candidates.reload}>Try again</Button>}>
            <ProblemMessage problem={candidates.error} />
          </Alert>
        ) : (
          <DataTable
            label="Unmanifested parcels"
            columns={candidateColumns}
            rows={candidates.data ?? []}
            getRowKey={(row) => `${row.shipmentId}-${row.carrier}-${row.trackingNumber}`}
            loading={candidates.loading}
            empty={{ title: "Every labelled parcel is on a manifest", description: "Parcels appear here once a carrier label is bought for a shipment." }}
          />
        )}
        <p className={styles.note}>
          Labels of the last 30 days that the carrier has not scanned yet.
          {(candidates.data?.length ?? 0) >= CANDIDATE_LIMIT ? ` Showing the oldest ${CANDIDATE_LIMIT}; closing a manifest takes every ready parcel, not only these.` : ""}
        </p>
      </Card>
      {canShip ? <CloseCard key={groups.map((g) => `${g.key}:${g.ready}`).join(",")} groups={groups} onDone={done} /> : <p className={styles.note}>Closing or cancelling a manifest needs the shipping duty.</p>}
    </div>
  );
}
