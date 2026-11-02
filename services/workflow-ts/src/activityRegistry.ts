import { createActivities, createPartyHttpClient } from "./activities.js";
import { createCapaActivities } from "./capaActivities.js";
import { createEcoActivities } from "./ecoActivities.js";
import { createPaymentRunActivities } from "./paymentRunActivities.js";
import { createTenderActivities } from "./tenderActivities.js";
import { serviceTokenSignerFromEnv } from "./serviceToken.js";

type Env = Record<string, string | undefined>;
export type Warn = (message: string, attributes: Record<string, string>) => void;

export function buildActivities(env: Env, readFile: (path: string) => string, warn: Warn) {
  const party = createActivities(createPartyHttpClient(env.PARTY_BASE_URL ?? "http://party.passage.svc"));
  const tender = createTenderActivities(env.TRANSPORT_BASE_URL ?? "http://transport.passage.svc");
  try {
    const signer = serviceTokenSignerFromEnv(env, readFile);
    return {
      ...party,
      ...tender,
      ...createPaymentRunActivities(env.PAYABLES_BASE_URL ?? "http://payables.passage.svc", signer),
      ...createEcoActivities(env.ENGINEERING_BASE_URL ?? "http://engineering.passage.svc", signer),
      ...createCapaActivities(env.QUALITY_BASE_URL ?? "http://quality.passage.svc", signer),
    };
  } catch (error) {
    warn("payment run, ECO and CAPA activities are not registered: no usable service signing key; the party approval workflow keeps running", {
      reason: error instanceof Error ? error.message : String(error),
    });
    return { ...party, ...tender };
  }
}
