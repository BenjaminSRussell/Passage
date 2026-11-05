// The gateway between Passage callers and the carrier adapters: rate shopping across carriers, idempotent label
// purchase, tracking. Labels are kept per tenant, legal entity and calling service in a LabelStore; the in-memory
// store is the only one built (NOT-IMPLEMENTED.md).

import { createHash } from "node:crypto";
import { CarrierError, type CarrierAdapter, type Label, type LabelRequest, type Rate, type RateRequest, type Tracking } from "./adapter.ts";
import { parseFixed } from "./money.ts";

export interface Scope {
  tenantId: string;
  legalEntityId: string;
  issuer: string;
}

export interface StoredLabel {
  fingerprint: string;
  label: Label;
}

export interface LabelStore {
  get(key: string): Promise<StoredLabel | undefined>;
  /** Stores the label unless the key is taken; returns what the key holds afterwards. */
  putIfAbsent(key: string, value: StoredLabel): Promise<StoredLabel>;
}

export class MemoryLabelStore implements LabelStore {
  private readonly labels = new Map<string, StoredLabel>();
  async get(key: string): Promise<StoredLabel | undefined> {
    return this.labels.get(key);
  }
  async putIfAbsent(key: string, value: StoredLabel): Promise<StoredLabel> {
    const held = this.labels.get(key);
    if (held) return held;
    this.labels.set(key, value);
    return value;
  }
}

export interface CarrierFailure {
  carrier: string;
  code: CarrierError["code"];
  message: string;
}

export interface RateShopResult {
  items: Rate[];
  failures: CarrierFailure[];
}

export class GatewayError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

const CARRIER_STATUS: Record<CarrierError["code"], [number, string]> = {
  UNKNOWN_SERVICE: [422, "CGW-0102"],
  NOT_SERVICEABLE: [422, "CGW-0103"],
  NOT_FOUND: [404, "CGW-0104"],
  UNAVAILABLE: [503, "CGW-0105"],
};

export function fromCarrierError(err: unknown): GatewayError {
  if (err instanceof CarrierError) {
    const [status, code] = CARRIER_STATUS[err.code];
    return new GatewayError(status, code, err.message);
  }
  return new GatewayError(503, "CGW-0105", "the carrier could not be reached");
}

function fingerprint(carrier: string, req: Omit<LabelRequest, "idempotencyKey">): string {
  return createHash("sha256").update(JSON.stringify([carrier, req.service, req.reference ?? "", req.shipFrom, req.shipTo, req.parcels])).digest("hex");
}

export class Gateway {
  private readonly adapters: Map<string, CarrierAdapter>;
  private readonly store: LabelStore;

  constructor(adapters: CarrierAdapter[], store: LabelStore = new MemoryLabelStore()) {
    this.adapters = new Map(adapters.map((a) => [a.code, a]));
    this.store = store;
  }

  carriers(): Array<{ code: string; sandbox: boolean; services: ReturnType<CarrierAdapter["services"]> }> {
    return [...this.adapters.values()].map((a) => ({ code: a.code, sandbox: a.sandbox, services: a.services() }));
  }

  private adapter(code: string): CarrierAdapter {
    const a = this.adapters.get(code);
    if (!a) throw new GatewayError(422, "CGW-0101", `no carrier ${code} is configured`);
    return a;
  }

  /**
   * Rates from every requested carrier (all when none are named), cheapest first, then fastest, then by carrier and
   * service code. A carrier that refuses or fails is reported in failures and does not fail the others.
   */
  async rateShop(req: RateRequest, carriers?: string[]): Promise<RateShopResult> {
    const adapters = (carriers ?? [...this.adapters.keys()]).map((c) => this.adapter(c));
    const results = await Promise.allSettled(adapters.map((a) => a.rate(req)));
    const items: Rate[] = [];
    const failures: CarrierFailure[] = [];
    results.forEach((r, i) => {
      const carrier = adapters[i]!.code;
      if (r.status === "fulfilled") items.push(...r.value);
      else {
        const e = r.reason instanceof CarrierError ? r.reason : new CarrierError("UNAVAILABLE", "the carrier could not be reached");
        failures.push({ carrier, code: e.code, message: e.message });
      }
    });
    items.sort(
      (a, b) =>
        (parseFixed(a.amount, 2) ?? 0) - (parseFixed(b.amount, 2) ?? 0) ||
        a.transitDays - b.transitDays ||
        a.carrier.localeCompare(b.carrier) ||
        a.service.localeCompare(b.service),
    );
    return { items, failures };
  }

  /**
   * Buys a label once per idempotency key within the caller's scope: a repeat with the same request returns the
   * stored label (created false); a repeat with a different request is refused (CGW-0422).
   */
  async label(scope: Scope, carrier: string, req: LabelRequest): Promise<{ label: Label; created: boolean }> {
    const adapter = this.adapter(carrier);
    const key = [scope.tenantId, scope.legalEntityId, scope.issuer, req.idempotencyKey].join("|");
    const { idempotencyKey: _ignored, ...rest } = req;
    const fp = fingerprint(carrier, rest);
    const held = await this.store.get(key);
    if (held) return this.replay(held, fp);
    let label: Label;
    try {
      // The adapter sees a key namespaced by the scope, so two tenants' equal keys never share a tracking number.
      label = await adapter.label({ ...req, idempotencyKey: key });
    } catch (err) {
      throw fromCarrierError(err);
    }
    const stored = await this.store.putIfAbsent(key, { fingerprint: fp, label });
    return stored.label === label ? { label, created: true } : this.replay(stored, fp);
  }

  private replay(held: StoredLabel, fp: string): { label: Label; created: boolean } {
    if (held.fingerprint !== fp) throw new GatewayError(422, "CGW-0422", "the Idempotency-Key was used for a different label request");
    return { label: held.label, created: false };
  }

  async track(carrier: string, trackingNumber: string): Promise<Tracking> {
    const adapter = this.adapter(carrier);
    try {
      return await adapter.track(trackingNumber);
    } catch (err) {
      throw fromCarrierError(err);
    }
  }
}
