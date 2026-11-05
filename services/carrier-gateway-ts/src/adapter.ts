// The carrier adapter interface (wave 28 D3, FR-28.x "carrier integration abstraction (rate, label, tracking)").
// Every carrier (the sandbox now; EasyPost-, Shippo-style or direct carrier APIs later) implements CarrierAdapter.
// The gateway validates requests before an adapter sees them and maps CarrierError to problem responses, so an
// adapter only speaks its carrier's protocol. Money is a decimal string with two places in the request currency.

export interface Address {
  name?: string;
  street1: string;
  city: string;
  postalCode: string;
  /** ISO 3166-1 alpha-2, upper case. */
  country: string;
  state?: string;
}

export interface Parcel {
  weightKg: string;
  lengthCm: string;
  widthCm: string;
  heightCm: string;
}

export interface Shipment {
  shipFrom: Address;
  shipTo: Address;
  parcels: Parcel[];
}

export interface RateRequest extends Shipment {
  /** Only these service codes (all of the carrier's when omitted). */
  services?: string[];
}

export interface Rate {
  carrier: string;
  service: string;
  amount: string;
  currency: string;
  transitDays: number;
  billableWeightKg: string;
}

export interface LabelRequest extends Shipment {
  service: string;
  /** The caller's reference printed on the label (for example the shipment number). */
  reference?: string;
  /** Names the label: the same key always yields the same tracking number. */
  idempotencyKey: string;
}

export interface Label {
  carrier: string;
  service: string;
  trackingNumber: string;
  amount: string;
  currency: string;
  format: "ZPL";
  /** The label document, base64. */
  document: string;
  createdAt: string;
}

/** The statuses fulfillment's tracking events take (services/fulfillment trackingStatus). */
export type TrackingStatus = "LABELLED" | "IN_TRANSIT" | "DELIVERED" | "EXCEPTION";

export interface TrackingEvent {
  /** Stable per event: fulfillment stores one event per provider event key. */
  eventKey: string;
  status: TrackingStatus;
  description: string;
  eventTime: string;
}

export interface Tracking {
  carrier: string;
  trackingNumber: string;
  status: TrackingStatus;
  events: TrackingEvent[];
}

export interface ServiceInfo {
  code: string;
  name: string;
}

export interface CarrierAdapter {
  /** Lower-case carrier code, 2 to 32 of [a-z0-9-] (fulfillment's provider format). */
  readonly code: string;
  readonly sandbox: boolean;
  services(): ServiceInfo[];
  rate(req: RateRequest): Promise<Rate[]>;
  label(req: LabelRequest): Promise<Label>;
  track(trackingNumber: string): Promise<Tracking>;
}

export type CarrierErrorCode = "UNKNOWN_SERVICE" | "NOT_SERVICEABLE" | "NOT_FOUND" | "UNAVAILABLE";

/** An adapter's refusal. UNAVAILABLE is retryable (carrier outage); the others are not. */
export class CarrierError extends Error {
  readonly code: CarrierErrorCode;
  constructor(code: CarrierErrorCode, message: string) {
    super(message);
    this.code = code;
  }
}
