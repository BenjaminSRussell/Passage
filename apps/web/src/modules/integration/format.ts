import type { AckStatus, MessageStatus } from "./api/client";
import type { BadgeTone } from "../../shell/ui";

export const dash = "—";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** "2026-10-26 08:00 UTC". */
export function formatDateTime(iso?: string | null): string {
  if (!iso) return dash;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : `${date.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** CCYYMMDD as YYYY-MM-DD; anything else unchanged. */
export function formatEdiDate(value?: string): string {
  if (!value) return dash;
  return /^\d{8}$/.test(value) ? `${value.slice(0, 4)}-${value.slice(4, 6)}-${value.slice(6, 8)}` : value;
}

export const ACK_LABELS: Record<AckStatus, { label: string; tone: BadgeTone }> = {
  PENDING: { label: "Pending", tone: "neutral" },
  ACCEPTED: { label: "Accepted", tone: "success" },
  ACCEPTED_WITH_ERRORS: { label: "Accepted with errors", tone: "warning" },
  REJECTED: { label: "Rejected", tone: "danger" },
  TIMEOUT: { label: "Timed out", tone: "danger" },
};

export const MESSAGE_LABELS: Record<MessageStatus, { label: string; tone: BadgeTone }> = {
  RECEIVED: { label: "Received", tone: "neutral" },
  MAPPED: { label: "Mapped", tone: "success" },
  POSTED: { label: "Posted", tone: "success" },
  FAILED: { label: "Failed", tone: "danger" },
  SENT: { label: "Sent", tone: "info" },
  ACKED: { label: "Acknowledged", tone: "success" },
};

/** BEG01 purpose codes (BR-35.1). */
export const PURPOSES: Record<string, string> = { "00": "Original", "01": "Cancellation", "04": "Change" };
