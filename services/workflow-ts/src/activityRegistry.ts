import { createActivities, createPartyHttpClient } from "./activities.js";
import { createCapaActivities } from "./capaActivities.js";
import { createEcoActivities } from "./ecoActivities.js";
import { createPartyMergeActivities, createPartyMergeHttpClient } from "./partyMergeActivities.js";
import { createPaymentRunActivities } from "./paymentRunActivities.js";
import { createTenderActivities } from "./tenderActivities.js";
import { serviceTokenSignerFromEnv } from "./serviceToken.js";

type Env = Record<string, string | undefined>;
export type Warn = (message: string, attributes: Record<string, string>) => void;

export function buildActivities(env: Env, readFile: (path: string) => string, warn: Warn) {
  try {
    const signer = serviceTokenSignerFromEnv(env, readFile);
    const partyUrl = env.PARTY_BASE_URL ?? "http://party.passage.svc:8080";
    return {
      ...createActivities(createPartyHttpClient(partyUrl, signer)),
      ...createPartyMergeActivities(createPartyMergeHttpClient(partyUrl, signer)),
      ...createTenderActivities(env.TRANSPORT_BASE_URL ?? "http://transport.passage.svc:8080", signer),
      ...createPaymentRunActivities(env.PAYABLES_BASE_URL ?? "http://payables.passage.svc:8080", signer),
      ...createEcoActivities(env.ENGINEERING_BASE_URL ?? "http://engineering.passage.svc:8080", signer),
      ...createCapaActivities(env.QUALITY_BASE_URL ?? "http://quality.passage.svc:8080", signer),
    };
  } catch (error) {
    warn("no activities are registered: every call to another service needs a usable service signing key", {
      reason: error instanceof Error ? error.message : String(error),
    });
    return {};
  }
}
