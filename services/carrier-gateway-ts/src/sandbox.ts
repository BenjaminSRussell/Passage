// The sandbox carrier: a deterministic, credential-free CarrierAdapter. It calls nothing outside the process.
// Rates are a published formula, the tracking number is derived from the idempotency key and the label time, and
// tracking is computed from the tracking number and the clock alone, so it survives a restart and needs no store.
//
// Tracking number: "SBX" + service digit + exception digit + transit days digit + 10-digit label time (Unix
// seconds) + 4 digits of SHA-256(key) + a GS1 mod-10 check digit (the SSCC rule), 21 characters.

import { createHash } from "node:crypto";
import {
  CarrierError,
  type CarrierAdapter,
  type Label,
  type LabelRequest,
  type Parcel,
  type Rate,
  type RateRequest,
  type ServiceInfo,
  type Tracking,
  type TrackingEvent,
} from "./adapter.ts";
import { ceilDiv, formatFixed, parseFixed } from "./money.ts";

export const SANDBOX_CODE = "sandbox";
export const CURRENCY = "USD";
/** Volumetric divisor in cm³ per kg. */
export const DIM_DIVISOR = 5000;
/** Postal code that makes a label end in an EXCEPTION instead of DELIVERED (for exception handling tests). */
export const EXCEPTION_POSTAL_CODE = "00000";
export const SERVICEABLE = new Set(["US", "CA", "MX", "GB", "IE", "DE", "FR", "NL", "BE", "LU", "AT", "CH", "IT", "ES", "PT", "PL", "CZ", "SE", "DK", "NO", "FI"]);

interface ServiceCard extends ServiceInfo {
  digit: number;
  baseCents: number;
  perKgCents: number;
  domesticDays: number;
  internationalDays: number;
}

const INTERNATIONAL_SURCHARGE_CENTS = 1200;
const CARDS: readonly ServiceCard[] = [
  { code: "GROUND", name: "Sandbox Ground", digit: 1, baseCents: 650, perKgCents: 85, domesticDays: 3, internationalDays: 6 },
  { code: "EXPRESS", name: "Sandbox Express", digit: 2, baseCents: 1400, perKgCents: 210, domesticDays: 1, internationalDays: 3 },
];
const IN_TRANSIT_AFTER_S = 4 * 3600;
const EXCEPTION_AFTER_S = 24 * 3600;

/** GS1 mod-10 check digit over a string of digits (weights 3,1 from the right), as for SSCC and GTIN. */
export function gs1CheckDigit(digits: string): number {
  let sum = 0;
  for (let i = 0; i < digits.length; i++) {
    const d = digits.charCodeAt(digits.length - 1 - i) - 48;
    sum += d * (i % 2 === 0 ? 3 : 1);
  }
  return (10 - (sum % 10)) % 10;
}

/** Billable grams of one parcel: the larger of actual and volumetric weight, rounded up to 100 g. */
export function billableGrams(p: Parcel): number {
  const grams = parseFixed(p.weightKg, 3);
  const l = parseFixed(p.lengthCm, 1);
  const w = parseFixed(p.widthCm, 1);
  const h = parseFixed(p.heightCm, 1);
  if (grams === null || l === null || w === null || h === null) throw new TypeError("parcel weight and dimensions must be decimal strings");
  // l, w, h are millimetres; mm³ / (DIM_DIVISOR cm³/kg) = mm³ / DIM_DIVISOR / 1000 kg = mm³ / DIM_DIVISOR grams.
  const dimGrams = ceilDiv(l * w * h, DIM_DIVISOR);
  return ceilDiv(Math.max(grams, dimGrams), 100) * 100;
}

export interface SandboxOptions {
  now?: () => number;
}

export class SandboxCarrier implements CarrierAdapter {
  readonly code = SANDBOX_CODE;
  readonly sandbox = true;
  private readonly now: () => number;

  constructor(options: SandboxOptions = {}) {
    this.now = options.now ?? Date.now;
  }

  services(): ServiceInfo[] {
    return CARDS.map(({ code, name }) => ({ code, name }));
  }

  async rate(req: RateRequest): Promise<Rate[]> {
    this.serviceable(req.shipTo.country, req.shipFrom.country);
    const cards = req.services ? req.services.map((s) => this.card(s)) : CARDS;
    const grams = req.parcels.reduce((sum, p) => sum + billableGrams(p), 0);
    const international = req.shipFrom.country !== req.shipTo.country;
    return cards.map((c) => ({
      carrier: this.code,
      service: c.code,
      amount: formatFixed(priceCents(c, grams, international), 2),
      currency: CURRENCY,
      transitDays: international ? c.internationalDays : c.domesticDays,
      billableWeightKg: formatFixed(grams, 3),
    }));
  }

  async label(req: LabelRequest): Promise<Label> {
    this.serviceable(req.shipTo.country, req.shipFrom.country);
    const card = this.card(req.service);
    const [rate] = await this.rate({ ...req, services: [card.code] });
    if (!rate) throw new CarrierError("UNKNOWN_SERVICE", `sandbox has no service ${req.service}`);
    const createdS = Math.floor(this.now() / 1000);
    const exception = req.shipTo.postalCode === EXCEPTION_POSTAL_CODE ? 1 : 0;
    const keyDigits = String(parseInt(createHash("sha256").update(req.idempotencyKey).digest("hex").slice(0, 8), 16) % 10000).padStart(4, "0");
    const body = `${card.digit}${exception}${rate.transitDays}${String(createdS).padStart(10, "0")}${keyDigits}`;
    const trackingNumber = `SBX${body}${gs1CheckDigit(body)}`;
    return {
      carrier: this.code,
      service: card.code,
      trackingNumber,
      amount: rate.amount,
      currency: rate.currency,
      format: "ZPL",
      document: Buffer.from(zpl(req, card, trackingNumber)).toString("base64"),
      createdAt: new Date(createdS * 1000).toISOString(),
    };
  }

  async track(trackingNumber: string): Promise<Tracking> {
    const m = /^SBX(\d{17})(\d)$/.exec(trackingNumber);
    if (!m || gs1CheckDigit(m[1]!) !== Number(m[2])) throw new CarrierError("NOT_FOUND", "not a sandbox tracking number");
    const body = m[1]!;
    const exception = body[1] === "1";
    const transitDays = Number(body[2]);
    const createdS = Number(body.slice(3, 13));
    const nowS = Math.floor(this.now() / 1000);
    if (createdS > nowS) throw new CarrierError("NOT_FOUND", "no such sandbox label");
    const timeline: Array<[number, TrackingEvent["status"], string]> = [
      [createdS, "LABELLED", "Label created"],
      [createdS + IN_TRANSIT_AFTER_S, "IN_TRANSIT", "Picked up by the sandbox carrier"],
      exception
        ? [createdS + EXCEPTION_AFTER_S, "EXCEPTION", "Delivery exception: address not found"]
        : [createdS + transitDays * 86400, "DELIVERED", "Delivered"],
    ];
    const events = timeline
      .filter(([t]) => t <= nowS)
      .map(([t, status, description], i) => ({ eventKey: `${trackingNumber}:${i + 1}`, status, description, eventTime: new Date(t * 1000).toISOString() }));
    return { carrier: this.code, trackingNumber, status: events[events.length - 1]!.status, events };
  }

  private card(service: string): ServiceCard {
    const c = CARDS.find((x) => x.code === service);
    if (!c) throw new CarrierError("UNKNOWN_SERVICE", `sandbox has no service ${service}`);
    return c;
  }

  private serviceable(to: string, from: string): void {
    for (const country of [from, to]) {
      if (!SERVICEABLE.has(country)) throw new CarrierError("NOT_SERVICEABLE", `sandbox does not serve ${country}`);
    }
  }
}

function priceCents(c: ServiceCard, grams: number, international: boolean): number {
  return c.baseCents + ceilDiv(c.perKgCents * grams, 1000) + (international ? INTERNATIONAL_SURCHARGE_CENTS : 0);
}

function zplText(s: string): string {
  // ^FD data must not contain the ZPL command prefixes.
  return s.replace(/[\^~\r\n]/g, " ").slice(0, 60);
}

function zpl(req: LabelRequest, card: ServiceCard, trackingNumber: string): string {
  const to = req.shipTo;
  return [
    "^XA",
    "^CF0,30",
    `^FO40,40^FD${zplText(card.name)} - SANDBOX, NOT FOR SHIPPING^FS`,
    `^FO40,100^FD${zplText(to.name ?? "")}^FS`,
    `^FO40,140^FD${zplText(to.street1)}^FS`,
    `^FO40,180^FD${zplText(`${to.postalCode} ${to.city} ${to.country}`)}^FS`,
    `^FO40,240^FDRef: ${zplText(req.reference ?? "")}^FS`,
    `^FO40,300^BY3^BCN,120,Y,N,N^FD${trackingNumber}^FS`,
    "^XZ",
    "",
  ].join("\n");
}
