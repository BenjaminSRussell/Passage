import { createActivities, createPartyHttpClient } from "./activities.js";
import { createPaymentRunActivities } from "./paymentRunActivities.js";
import { serviceTokenSignerFromEnv } from "./serviceToken.js";

type Env = Record<string, string | undefined>;
export type Warn = (message: string, attributes: Record<string, string>) => void;

export function buildActivities(env: Env, readFile: (path: string) => string, warn: Warn) {
  const party = createActivities(createPartyHttpClient(env.PARTY_BASE_URL ?? "http://party.passage.svc"));
  try {
    const signer = serviceTokenSignerFromEnv(env, readFile);
    return { ...party, ...createPaymentRunActivities(env.PAYABLES_BASE_URL ?? "http://payables.passage.svc", signer) };
  } catch (error) {
    warn("payment run activities are not registered: no usable service signing key; the party approval workflow keeps running", {
      reason: error instanceof Error ? error.message : String(error),
    });
    return party;
  }
}
