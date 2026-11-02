// The CloudEvents consumer (FR-35.9): reads the configured Kafka topics, matches
// each event's type against the tenant's active subscriptions (matchesAny) and
// enqueues a delivery for every match. At-least-once: kafkajs commits an offset
// only after the message handler returned, and the handler returns only after
// the enqueue transaction committed, so a crash replays the message and the
// store's unique (subscription, event id) makes the replay a no-op. A message
// that is not a usable CloudEvent is published to the dead-letter topic with the
// reason, then committed; a failure to enqueue or to publish throws, so the
// message is retried rather than lost.

import { readFileSync } from "node:fs";
import { Kafka, logLevel, type Consumer, type Producer } from "kafkajs";

import type { HubEvent } from "./dispatcher.ts";
import { matchesAny } from "./filter.ts";
import type { Metrics } from "./metrics.ts";
import type { HubStore } from "./pgStore.ts";

export interface KafkaMessageInput {
  topic: string;
  partition: number;
  offset: string;
  key: Buffer | null;
  value: Buffer | null;
}

export interface DeadLetterRecord {
  topic: string;
  key: Buffer | null;
  value: Buffer | null;
  headers: Record<string, string>;
}

export interface ConsumerConfig {
  brokers: string[];
  group: string;
  topics: string[];
  dlqTopic: string;
  username?: string;
  passwordFile?: string;
  caFile?: string;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const NIL_UUID = "00000000-0000-0000-0000-000000000000";

export function consumerConfigFromEnv(env: NodeJS.ProcessEnv = process.env): ConsumerConfig | null {
  const list = (v: string | undefined) => (v ?? "").split(",").map((s) => s.trim()).filter((s) => s !== "");
  const brokers = list(env["KAFKA_BROKERS"]);
  const topics = list(env["HUB_TOPICS"]);
  if (brokers.length === 0 || topics.length === 0) {
    return null;
  }
  const dlqTopic = env["HUB_DLQ_TOPIC"] ?? "";
  if (dlqTopic === "") {
    throw new Error("HUB_DLQ_TOPIC is required when KAFKA_BROKERS and HUB_TOPICS are set");
  }
  return {
    brokers,
    group: env["KAFKA_GROUP"] ?? "integration-hub",
    topics,
    dlqTopic,
    ...(env["KAFKA_USERNAME"] ? { username: env["KAFKA_USERNAME"] } : {}),
    ...(env["KAFKA_PASSWORD_FILE"] ? { passwordFile: env["KAFKA_PASSWORD_FILE"] } : {}),
    ...(env["KAFKA_TLS_CA_FILE"] ? { caFile: env["KAFKA_TLS_CA_FILE"] } : {}),
  };
}

/** A CloudEvent the hub can route, or the reason it cannot. */
export function parseEvent(message: KafkaMessageInput): { tenantId: string; event: HubEvent } | { reason: string } {
  if (message.value === null) {
    return { reason: "empty message" };
  }
  let raw: unknown;
  try {
    raw = JSON.parse(message.value.toString("utf8"));
  } catch {
    return { reason: "not JSON" };
  }
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) {
    return { reason: "not a JSON object" };
  }
  const e = raw as Record<string, unknown>;
  if (e["specversion"] !== "1.0") {
    return { reason: "specversion is not 1.0" };
  }
  const { id, type, tenantid: tenantId, subject, time, actor, legalentityid } = e;
  if (typeof id !== "string" || id === "") {
    return { reason: "missing id" };
  }
  if (typeof type !== "string" || !/^[a-z0-9]+(\.[a-z0-9]+)+$/.test(type)) {
    return { reason: "missing or invalid type" };
  }
  if (typeof tenantId !== "string" || !UUID.test(tenantId) || tenantId === NIL_UUID) {
    return { reason: "missing or invalid tenantid" };
  }
  let occurredAt = Date.now() / 1000;
  if (time !== undefined) {
    const t = typeof time === "string" ? Date.parse(time) : Number.NaN;
    if (Number.isNaN(t)) {
      return { reason: "time is not an RFC 3339 timestamp" };
    }
    occurredAt = t / 1000;
  }
  // Per-aggregate order: the producer's passagesequence, else the partition offset (monotonic for a keyed aggregate).
  const seq = e["passagesequence"] !== undefined ? Number(e["passagesequence"]) : Number(message.offset);
  if (!Number.isSafeInteger(seq) || seq < 0) {
    return { reason: "passagesequence is not a non-negative integer" };
  }
  return {
    tenantId: tenantId.toLowerCase(),
    event: {
      eventId: id,
      type,
      aggregateId: typeof subject === "string" && subject !== "" ? subject : id,
      sequence: seq,
      occurredAt,
      data: e["data"] ?? null,
      ...(typeof actor === "string" && actor !== "" ? { actor } : {}),
      ...(typeof legalentityid === "string" && UUID.test(legalentityid) ? { legalEntityId: legalentityid.toLowerCase() } : {}),
    },
  };
}

export interface HandlerOptions {
  store: HubStore;
  deadLetter: (record: DeadLetterRecord) => Promise<void>;
  dlqTopic: string;
  clock?: () => number;
  metrics?: Metrics;
}

/** The per-message handler. Resolves when the message is safe to commit; throws when it must be retried. */
export function createMessageHandler(options: HandlerOptions): (message: KafkaMessageInput) => Promise<void> {
  const { store, deadLetter, dlqTopic, metrics } = options;
  const clock = options.clock ?? (() => Date.now() / 1000);
  return async (message) => {
    const parsed = parseEvent(message);
    if ("reason" in parsed) {
      await deadLetter({
        topic: dlqTopic,
        key: message.key,
        value: message.value,
        headers: {
          "x-dlq-reason": parsed.reason,
          "x-dlq-source-topic": message.topic,
          "x-dlq-source-partition": String(message.partition),
          "x-dlq-source-offset": message.offset,
        },
      });
      metrics?.inc("hub_consumer_dead_lettered_total", "Malformed events sent to the DLQ topic");
      return;
    }
    const enqueued = await store.enqueueEvent(parsed.tenantId, parsed.event, matchesAny, clock());
    metrics?.inc("hub_consumer_events_total", "Events consumed", { matched: enqueued > 0 ? "yes" : "no" });
    metrics?.inc("hub_consumer_deliveries_enqueued_total", "Deliveries enqueued", {}, enqueued);
  };
}

export interface RunningConsumer {
  stop(): Promise<void>;
}

/** Connects to Kafka and feeds every message of the configured topics to the handler. */
export async function startConsumer(config: ConsumerConfig, store: HubStore, metrics?: Metrics): Promise<RunningConsumer> {
  const kafka = new Kafka({
    clientId: "integration-hub",
    brokers: config.brokers,
    logLevel: logLevel.WARN,
    ...(config.caFile ? { ssl: { ca: [readFileSync(config.caFile, "utf8")] } } : {}),
    ...(config.username && config.passwordFile
      ? { sasl: { mechanism: "scram-sha-512" as const, username: config.username, password: readFileSync(config.passwordFile, "utf8").trim() } }
      : {}),
  });
  const producer: Producer = kafka.producer();
  const consumer: Consumer = kafka.consumer({ groupId: config.group });
  await producer.connect();
  await consumer.connect();
  for (const topic of config.topics) {
    await consumer.subscribe({ topic, fromBeginning: false });
  }
  const handle = createMessageHandler({
    store,
    dlqTopic: config.dlqTopic,
    metrics,
    deadLetter: async (r) => {
      await producer.send({
        topic: r.topic,
        messages: [{ key: r.key, value: r.value, headers: r.headers }],
      });
    },
  });
  await consumer.run({
    eachMessage: async ({ topic, partition, message }) => {
      await handle({ topic, partition, offset: message.offset, key: message.key, value: message.value });
    },
  });
  return {
    async stop() {
      await consumer.disconnect();
      await producer.disconnect();
    },
  };
}
