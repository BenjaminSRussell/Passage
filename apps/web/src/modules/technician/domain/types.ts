// Shapes of the technician day API (services/fieldservice/api/openapi.yaml, getTechnicianDay) and of the
// actions the device logs. Decimals travel as strings.

export type VisitStatus = "DISPATCHED" | "TRAVELING" | "IN_PROGRESS" | "ON_HOLD" | "COMPLETED" | "POSTED" | "SCHEDULED" | "CANCELED" | "UNSCHEDULED";

export interface DayLine {
  id: string;
  kind: "LABOR" | "PART" | "TRAVEL" | "FEE";
  description?: string;
  productId?: string;
  unit?: string;
  qty: string;
  consumedQty: string;
}

export interface DayCheck {
  id: string;
  text: string;
  required: boolean;
  done: boolean;
}

export interface DayPhoto {
  captureId: string;
  ref: string;
  capturedAt: string;
}

export interface DaySignature {
  signed: boolean;
  exempt: boolean;
  signedBy?: string | null;
  signedAt?: string | null;
  ref?: string | null;
}

export interface DayVisit {
  id: string;
  number: string;
  status: VisitStatus;
  version: number;
  sequence: number;
  carryOver: boolean;
  customerPartyId: string;
  caseId?: string | null;
  caseNumber?: string | null;
  caseDescription?: string;
  priority?: number | null;
  serviceObjectId?: string | null;
  serviceObjectLabel?: string;
  address: string;
  latitude?: number | null;
  longitude?: number | null;
  assignedStart: string;
  assignedEnd: string;
  windowStart?: string | null;
  windowEnd?: string | null;
  resolutionDue?: string | null;
  estDurationMinutes: number;
  requiredSkills: string[];
  lines: DayLine[];
  checklist: DayCheck[];
  photos: DayPhoto[];
  signature: DaySignature;
  technicianNote: string;
  completedAt?: string | null;
}

export interface DayTechnician {
  id: string;
  code: string;
  name: string;
  timeZone: string;
  legalEntityId?: string;
  truck?: { siteId: string; warehouseId: string; locationId?: string | null };
}

export interface TechnicianDay {
  technician: DayTechnician;
  date: string;
  generatedAt: string;
  visits: DayVisit[];
  totals: { visits: number; completed: number; open: number; bookedMinutes: number };
}

export type ActionKind = "TRAVEL_START" | "WORK_START" | "HOLD" | "RESUME" | "PART_USED" | "SIGNATURE" | "NOTE" | "COMPLETE" | "PHOTO" | "CHECKLIST";

/** One action as the server takes it (ActionInput in the OpenAPI document). */
export interface ActionBody {
  id: string;
  kind: ActionKind;
  deviceTime: string;
  lineId?: string;
  qty?: string;
  signedBy?: string;
  signatureRef?: string;
  text?: string;
  latitude?: number;
  longitude?: number;
  captureIds?: string[];
  itemId?: string;
  done?: boolean;
}

export interface ActionOutcome {
  id: string;
  kind: string;
  outcome: "APPLIED" | "SUPERSEDED" | "REJECTED";
  code?: string;
  reason?: string;
  duplicate?: boolean;
}

export interface CaptureBody {
  captureId: string;
  kind: "PHOTO" | "SIGNATURE";
  contentType: string;
  /** Base64 of the image bytes. */
  data: string;
  capturedAt: string;
  latitude?: number;
  longitude?: number;
}
