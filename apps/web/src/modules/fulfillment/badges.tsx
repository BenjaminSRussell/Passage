import { Badge, type BadgeTone } from "../../shell/ui";
import type { ContainerStatus, PackingSlipStatus, ShipmentStatus } from "./api/client";
import { SHIPMENT_STATUS_LABELS } from "./format";

const shipmentTones: Record<ShipmentStatus, BadgeTone> = {
  OPEN: "neutral",
  RELEASED: "info",
  PICKING: "info",
  PICKED: "info",
  PACKED: "warning",
  PARTIALLY_SHIPPED: "warning",
  SHIPPED: "success",
  DELIVERED: "success",
  CANCELLED: "danger",
};

/** Shipment state (BR-28.6) as a pill; PACKED is "waiting to ship". */
export function ShipmentStatusBadge({ status }: { status: ShipmentStatus }) {
  return <Badge tone={shipmentTones[status] ?? "neutral"}>{SHIPMENT_STATUS_LABELS[status] ?? status}</Badge>;
}

const slipTones: Record<PackingSlipStatus, BadgeTone> = { PENDING: "neutral", POSTED: "success", CANCELLED: "danger", INVOICED: "info" };
const slipLabels: Record<PackingSlipStatus, string> = { PENDING: "Pending", POSTED: "Posted", CANCELLED: "Cancelled", INVOICED: "Invoiced" };

/** Packing slip state; a reversing slip says so instead of "Posted". */
export function SlipStatusBadge({ status, reversal }: { status: PackingSlipStatus; reversal?: boolean }) {
  if (reversal) return <Badge tone="neutral">Reversal</Badge>;
  return <Badge tone={slipTones[status] ?? "neutral"}>{slipLabels[status] ?? status}</Badge>;
}

const containerTones: Record<ContainerStatus, BadgeTone> = { OPEN: "neutral", CLOSED: "info", SHIPPED: "success" };
const containerLabels: Record<ContainerStatus, string> = { OPEN: "Open", CLOSED: "Closed", SHIPPED: "Shipped" };

/** Container state: CLOSED is packed and labelled, SHIPPED left on a packing slip. */
export function ContainerStatusBadge({ status }: { status: ContainerStatus }) {
  return <Badge tone={containerTones[status] ?? "neutral"}>{containerLabels[status] ?? status}</Badge>;
}
