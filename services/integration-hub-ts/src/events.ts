// The hub's own CloudEvents (dev.passage.hub.<entity>.<verb>.v1), written to the
// outbox in the same transaction as the change they report (events/asyncapi.yaml).
// The outbox payload is the whole envelope, as platform/go/outbox writes it, so
// Debezium's router and the hub's own consumer read it like any other service's.

import { randomUUID } from "node:crypto";
import type pg from "pg";

export const SOURCE = "passage/integration-hub";

export const EVENT_TYPES = [
  "dev.passage.hub.subscription.created.v1",
  "dev.passage.hub.subscription.updated.v1",
  "dev.passage.hub.subscription.deleted.v1",
  "dev.passage.hub.subscription.paused.v1",
  "dev.passage.hub.subscription.resumed.v1",
  "dev.passage.hub.webhook.delivered.v1",
  "dev.passage.hub.webhook.deadlettered.v1",
  "dev.passage.hub.apiclient.registered.v1",
  "dev.passage.hub.apiclient.updated.v1",
  "dev.passage.hub.apiclient.revoked.v1",
] as const;
export type EventType = (typeof EVENT_TYPES)[number];

const TYPE_RE = /^dev\.passage\.[a-z][a-z0-9]*\.[a-z][a-z0-9]*\.[a-z][a-z0-9]*\.v[0-9]+$/;

export interface OutboxEvent {
  type: EventType;
  /** The Kafka key: events of one aggregate stay ordered. */
  aggregateId: string;
  /** The user (or service) that caused the change, when known. */
  actor: string | null;
  /** Payload fields are snake_case, as in the PRD and the other services' events. */
  data: Record<string, unknown>;
}

/** Builds the CloudEvents 1.0 envelope; throws on a type that breaks the naming rule (a programming error). */
export function envelope(tenantId: string, e: OutboxEvent, now: Date = new Date()): Record<string, unknown> {
  if (!TYPE_RE.test(e.type)) {
    throw new Error(`invalid event type ${e.type}`);
  }
  return {
    specversion: "1.0",
    id: randomUUID(),
    source: SOURCE,
    type: e.type,
    subject: e.aggregateId,
    time: now.toISOString(),
    datacontenttype: "application/json",
    data: e.data,
    tenantid: tenantId,
    ...(e.actor === null ? {} : { actor: e.actor }),
  };
}

/**
 * The outbox aggregate_type: the type's entity segment, so Debezium's EventRouter
 * (route.by.field aggregate_type) publishes to passage.hub.<entity>, the topic
 * platform/go/events.Topic gives for the type and the AsyncAPI channel.
 */
export function aggregateTypeOf(type: EventType): string {
  return type.split(".")[3] as string;
}

/** Writes the event to the outbox inside the caller's tenant transaction. */
export async function enqueue(c: pg.PoolClient, tenantId: string, e: OutboxEvent): Promise<void> {
  const env = envelope(tenantId, e);
  await c.query(
    "insert into outbox (id, tenant_id, aggregate_type, aggregate_id, type, payload) values ($1, $2, $3, $4, $5, $6::jsonb)",
    [env["id"], tenantId, aggregateTypeOf(e.type), e.aggregateId, e.type, JSON.stringify(env)],
  );
}
