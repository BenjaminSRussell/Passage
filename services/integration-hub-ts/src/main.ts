// Entry point: API on :8080, admin (/metrics, /healthz, /readyz) on :9090, the
// Kafka consumer when KAFKA_BROKERS and HUB_TOPICS are set, and the retry poller.

import { readFileSync } from "node:fs";
import { createServer } from "node:http";
import pg from "pg";

import { FieldKeys } from "./crypt.ts";
import { CoexistenceStore, CoexistenceSweeper, coexistenceRecorder } from "./coexistence.ts";
import { consumerConfigFromEnv, startConsumer, type RunningConsumer } from "./consumer.ts";
import { httpTransport } from "./dispatcher.ts";
import { httpMigrationClient } from "./migrationClient.ts";
import { Maintenance, usageRetentionDaysFromEnv } from "./maintenance.ts";
import { Metrics } from "./metrics.ts";
import { HubStore, MIGRATIONS_DIR, migrate } from "./pgStore.ts";
import { Poller } from "./poller.ts";
import { createAdminServer, createHubHandler } from "./server.ts";
import { httpExchange, RestPusher } from "./restPush.ts";
import { serviceTokenSignerFromEnv } from "./serviceToken.ts";
import { allowHttpFromEnv, egressPolicyFromEnv } from "./ssrf.ts";

const env = process.env;
const databaseUrl = env["DATABASE_URL"];
if (databaseUrl === undefined || databaseUrl === "") {
  console.error("DATABASE_URL is required");
  process.exit(1);
}

const pool = new pg.Pool({ connectionString: databaseUrl, max: Number(env["HUB_DB_POOL"] ?? 10) });
if (env["MIGRATE_ON_START"] === "true") {
  await migrate(pool, MIGRATIONS_DIR);
}
// Signing secrets are sealed at rest; without keys, creating a subscription answers 501 and the poller cannot sign.
const keys = FieldKeys.fromEnv(env);
if (keys === null) {
  console.warn(JSON.stringify({ level: "warn", msg: "FIELDCRYPT_KEYS is empty: webhook subscriptions cannot be created or delivered until it is set" }));
}
const store = new HubStore(pool, keys);
const metrics = new Metrics();
const ping = async () => {
  await pool.query("select 1");
};

// Operator allowlist for private receiver addresses; a bad entry stops the start rather than silently widening or narrowing egress.
const guard = { allowHttp: allowHttpFromEnv(), egressAllow: egressPolicyFromEnv(env) };
// rest-push needs the migration service (render, xref) and a service signing key; without both it is off.
const signer = serviceTokenSignerFromEnv(env, (path) => readFileSync(path, "utf8"));
const migrationUrl = env["MIGRATION_URL"] ?? "";
const rest =
  migrationUrl !== "" && signer !== null
    ? new RestPusher({ migration: httpMigrationClient({ baseUrl: migrationUrl, signer }), exchange: httpExchange(guard) })
    : undefined;
if (rest === undefined) {
  console.warn(JSON.stringify({ level: "warn", msg: "rest-push is off: set MIGRATION_URL and PASSAGE_SERVICE_SIGNING_KEY(_FILE)" }));
}

const coexistenceStore = new CoexistenceStore(pool);
const api = createServer(createHubHandler({ store, metrics, ping, guard, coexistence: coexistenceStore, deprecationMonths: Number(env["HUB_API_DEPRECATION_MONTHS"] ?? 12), ...(rest === undefined ? {} : { rest }) }));
const admin = createAdminServer(metrics, ping);
api.listen(8080);
admin.listen(9090);

const poller = new Poller({
  store,
  metrics,
  transport: httpTransport(guard),
  ...(rest === undefined ? {} : { rest }),
  onError: (err) => console.error(JSON.stringify({ level: "error", msg: "poller", error: (err as Error).message })),
});
poller.start(Number(env["HUB_POLL_INTERVAL_MS"] ?? 2000));

// Re-seal after a master key rotation, drop rotated secrets past their grace period, purge old client usage.
const maintenance = new Maintenance({
  store,
  metrics,
  usageRetentionDays: usageRetentionDaysFromEnv(env),
  onError: (err) => console.error(JSON.stringify({ level: "error", msg: "maintenance", error: (err as Error).message })),
});
maintenance.start(Number(env["HUB_MAINTENANCE_INTERVAL_MS"] ?? 300_000));

// Legacy coexistence adapters (wave 49): the consumer records the mode each adapter should be in from the migration
// factory's coexistence and rollback events, and the sweep suspends or resumes it. The AX and SAP adapter implementations
// are not built yet (NOT-IMPLEMENTED.md), so with none registered a due row is parked with HUB-COEXIST-NO-ADAPTER.
const coexistence = new CoexistenceSweeper({
  store: coexistenceStore,
  adapters: [],
  metrics,
  onError: (err) => console.error(JSON.stringify({ level: "error", msg: "coexistence", error: (err as Error).message })),
});
coexistence.start(Number(env["HUB_COEXISTENCE_INTERVAL_MS"] ?? 15_000));

let consumer: RunningConsumer | null = null;
const consumerConfig = consumerConfigFromEnv(env);
if (consumerConfig !== null) {
  consumer = await startConsumer(consumerConfig, store, metrics, coexistenceRecorder(coexistenceStore, metrics));
}

const shutdown = async () => {
  api.close();
  admin.close();
  await poller.stop();
  await maintenance.stop();
  await coexistence.stop();
  await consumer?.stop();
  await pool.end();
  process.exit(0);
};
process.on("SIGTERM", () => void shutdown());
process.on("SIGINT", () => void shutdown());
