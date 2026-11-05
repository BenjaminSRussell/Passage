// The Kafka consumer that drives the tender cascade (#426): transport's load.released starts tenderWorkflow, and its
// tender.accepted and tender.declined send the tender-response signal. At-least-once: kafkajs commits an offset only after
// the handler returned, and the handler returns only after Temporal took the start or the signal, so a crash replays the
// message. A replay is harmless: a release starts no second run while the first is open and keys its tender start on the
// release id (a run started by a replay after the first ended gets the same tender back), and a signal only makes the
// workflow read the tender again. An event that can never be acted on (not JSON, or a release the workflow refuses) goes
// to the dead-letter topic with the reason and is committed; a failure to reach Temporal or Kafka throws, so the message is
// retried rather than lost.

import { readFileSync } from "node:fs";
import { Kafka, logLevel, type Consumer, type Producer } from "kafkajs";
import { ApplicationFailure, type Client } from "@temporalio/client";
import {
  handleLoadReleased,
  handleTenderOutcome,
  loadReleasedType,
  tenderOutcomeTypes,
  type LoadReleasedEvent,
  type TenderOutcomeEvent,
} from "./tenderTriggers.js";

/** Topics of transport's outbox (platform/go/events.Topic): passage.<context>.<entity>. */
export const TENDER_TOPICS = ["passage.tms.load", "passage.tms.tender"] as const;
export const DEFAULT_GROUP = "workflow-ts-tender";
export const DEFAULT_DLQ_TOPIC = "passage.workflow.tender.dlq";

// The platform's dead-letter headers (platform/go/eventing/kafka).
export const HEADER_DLQ_REASON = "x-dlq-reason";
export const HEADER_ORIGIN_TOPIC = "x-origin-topic";
export const HEADER_ORIGIN_PARTITION = "x-origin-partition";
export const HEADER_ORIGIN_OFFSET = "x-origin-offset";

export interface EventConsumerConfig {
  brokers: string[];
  group: string;
  topics: string[];
  dlqTopic: string;
  username?: string;
  passwordFile?: string;
  caFile?: string;
}

/** The consumer runs only when KAFKA_BROKERS is set; without it the worker starts no tender (as before #426). */
export function eventConsumerConfigFromEnv(env: Record<string, string | undefined>): EventConsumerConfig | null {
  const brokers = (env.KAFKA_BROKERS ?? "").split(",").map((s) => s.trim()).filter((s) => s !== "");
  if (brokers.length === 0) return null;
  return {
    brokers,
    group: env.KAFKA_GROUP || DEFAULT_GROUP,
    topics: [...TENDER_TOPICS],
    dlqTopic: env.WORKFLOW_DLQ_TOPIC || DEFAULT_DLQ_TOPIC,
    ...(env.KAFKA_USERNAME ? { username: env.KAFKA_USERNAME } : {}),
    ...(env.KAFKA_PASSWORD_FILE ? { passwordFile: env.KAFKA_PASSWORD_FILE } : {}),
    ...(env.KAFKA_TLS_CA_FILE ? { caFile: env.KAFKA_TLS_CA_FILE } : {}),
  };
}

export interface EventMessage {
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

export type EventOutcome =
  | { action: "started" | "already-running"; workflowId: string }
  | { action: "signalled" | "no-workflow"; workflowId: string }
  | { action: "ignored"; type: string }
  | { action: "dead-lettered"; reason: string };

export interface EventHandlerOptions {
  client: Pick<Client, "workflow">;
  taskQueue: string;
  dlqTopic: string;
  deadLetter: (record: DeadLetterRecord) => Promise<void>;
  warn?: (message: string, attributes: Record<string, string>) => void;
}

/** A refusal that no redelivery can change: the event itself is wrong. Anything else is retried. */
function refusal(error: unknown): string | undefined {
  if (error instanceof TypeError) return error.message;
  if (error instanceof ApplicationFailure && error.nonRetryable) return error.message;
  return undefined;
}

/** The per-message handler. Resolves when the message is safe to commit; throws when it must be retried. */
export function createEventHandler(options: EventHandlerOptions): (message: EventMessage) => Promise<EventOutcome> {
  const { client, taskQueue, dlqTopic, deadLetter, warn } = options;
  const reject = async (message: EventMessage, reason: string): Promise<EventOutcome> => {
    await deadLetter({
      topic: dlqTopic,
      key: message.key,
      value: message.value,
      headers: {
        [HEADER_DLQ_REASON]: reason,
        [HEADER_ORIGIN_TOPIC]: message.topic,
        [HEADER_ORIGIN_PARTITION]: String(message.partition),
        [HEADER_ORIGIN_OFFSET]: message.offset,
      },
    });
    warn?.("event dead-lettered", { topic: message.topic, offset: message.offset, reason });
    return { action: "dead-lettered", reason };
  };
  return async (message) => {
    let event: unknown;
    try {
      event = message.value === null ? null : JSON.parse(message.value.toString("utf8"));
    } catch {
      return reject(message, "not JSON");
    }
    if (typeof event !== "object" || event === null || Array.isArray(event)) return reject(message, "not a JSON object");
    const { type, data } = event as { type?: unknown; data?: unknown };
    if (typeof type !== "string") return reject(message, "missing type");
    const isRelease = type === loadReleasedType;
    if (!isRelease && !(tenderOutcomeTypes as readonly string[]).includes(type)) return { action: "ignored", type };
    if (typeof data !== "object" || data === null || Array.isArray(data)) return reject(message, "missing data");
    try {
      if (isRelease) {
        const { workflowId, started } = await handleLoadReleased(client, taskQueue, event as LoadReleasedEvent);
        return { action: started ? "started" : "already-running", workflowId };
      }
      const { workflowId, signalled } = await handleTenderOutcome(client, event as TenderOutcomeEvent);
      return { action: signalled ? "signalled" : "no-workflow", workflowId };
    } catch (error) {
      const reason = refusal(error);
      if (reason === undefined) throw error;
      return reject(message, reason);
    }
  };
}

export interface RunningEventConsumer {
  stop(): Promise<void>;
}

/** Connects to Kafka and feeds every message of the tender topics to the handler. */
export async function startEventConsumer(
  config: EventConsumerConfig,
  client: Pick<Client, "workflow">,
  taskQueue: string,
  warn?: (message: string, attributes: Record<string, string>) => void,
): Promise<RunningEventConsumer> {
  const kafka = new Kafka({
    clientId: "workflow-ts",
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
  const handle = createEventHandler({
    client,
    taskQueue,
    dlqTopic: config.dlqTopic,
    ...(warn ? { warn } : {}),
    deadLetter: async (record) => {
      await producer.send({ topic: record.topic, messages: [{ key: record.key, value: record.value, headers: record.headers }] });
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
