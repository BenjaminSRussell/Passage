import type { ModelProvider, ModelResponse } from "../runtime/engine.js";
import { ProviderError } from "./wire.js";

/** A provider adapter together with the region its endpoint is hosted in. */
export interface ProviderEndpoint {
  name: string;
  region: string;
  provider: ModelProvider;
}

export interface FailoverOptions {
  primary: ProviderEndpoint;
  secondary?: ProviderEndpoint;
  /** Tenant `ai.residency_region`; null means no residency constraint. */
  residencyRegion: string | null;
  /**
   * Residency port (wave 48 owns the data). Returns true when an endpoint in
   * `endpointRegion` may serve a tenant whose residency is `residencyRegion`.
   */
  residencyAllows: (endpointRegion: string, residencyRegion: string) => boolean;
  /** Default 60 s: how long the primary must fail before failover (BR-47.9). */
  outageWindowMs?: number;
  /** Default 30 s: while failed over, how often a call tries the primary first. */
  primaryRetryMs?: number;
  nowMs: () => number;
}

export interface FailoverProvider extends ModelProvider {
  /** Name of the endpoint that served the most recent call, for run audit. */
  lastServedBy(): string | undefined;
  /**
   * Called by the provider health probe (9.47.16, every 30 s) when the primary
   * answers again: clears the outage clock so the next call goes to the primary.
   */
  reportPrimaryHealthy(): void;
}

const DEFAULT_OUTAGE_WINDOW_MS = 60_000;
/** While failed over, one call per interval tries the primary first (9.47.16 probe cadence). */
const DEFAULT_PRIMARY_RETRY_MS = 30_000;

function isOutage(err: unknown): err is ProviderError {
  return err instanceof ProviderError && err.outage;
}

/**
 * Provider failover (BR-47.9). Calls go to the primary. An outage (5xx,
 * timeout, transport error) starts the outage clock; a provider that answers
 * but refuses or returns an unusable answer does not, because switching
 * providers would not fix it. Once the primary has been failing for the whole
 * window, calls move to the tenant's secondary, but only when the secondary
 * satisfies residency. Otherwise the run pauses: the call throws AGT-0206 and
 * nothing is sent to a non-compliant endpoint. While failed over or paused,
 * the primary is tried again at most once per retry interval, so traffic
 * returns to it as soon as it recovers; a successful primary call clears the
 * clock.
 */
export function createFailoverProvider(opts: FailoverOptions): FailoverProvider {
  const windowMs = opts.outageWindowMs ?? DEFAULT_OUTAGE_WINDOW_MS;
  const retryMs = opts.primaryRetryMs ?? DEFAULT_PRIMARY_RETRY_MS;
  let outageSince: number | undefined;
  let lastPrimaryAttempt: number | undefined;
  let servedBy: string | undefined;

  const secondaryUsable = (): ProviderEndpoint | undefined => {
    const s = opts.secondary;
    if (!s) return undefined;
    if (opts.residencyRegion === null) return s;
    return opts.residencyAllows(s.region, opts.residencyRegion) ? s : undefined;
  };
  const failedOver = (now: number): boolean => outageSince !== undefined && now - outageSince >= windowMs;
  // `primaryError` is the outage the primary just raised in this call, if it was tried. The pause
  // is unbilled only when no provider was asked, or the primary's own failure was unbilled.
  const viaSecondary = async (req: Parameters<ModelProvider["complete"]>[0], primaryError?: ProviderError): Promise<ModelResponse> => {
    const secondary = secondaryUsable();
    if (!secondary) {
      throw new ProviderError(
        `provider ${opts.primary.name} is unavailable and no residency-compliant secondary is configured; run paused`,
        true,
        primaryError?.unbilled ?? true,
      );
    }
    servedBy = secondary.name;
    return secondary.provider.complete(req);
  };

  return {
    lastServedBy: () => servedBy,
    reportPrimaryHealthy: () => {
      outageSince = undefined;
    },
    async complete(req): Promise<ModelResponse> {
      const now = opts.nowMs();
      const primaryDue = lastPrimaryAttempt === undefined || now - lastPrimaryAttempt >= retryMs;
      if (failedOver(now) && !primaryDue) return viaSecondary(req);
      try {
        lastPrimaryAttempt = now;
        const response = await opts.primary.provider.complete(req);
        outageSince = undefined;
        servedBy = opts.primary.name;
        return response;
      } catch (err) {
        if (!isOutage(err)) throw err;
        outageSince ??= now;
        if (failedOver(now)) return viaSecondary(req, err);
        throw err;
      }
    },
  };
}
