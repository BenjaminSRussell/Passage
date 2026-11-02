import type { OutboundDefinition, SourceSystem } from "./api/client";
import { isSourceSystem } from "./domain";

/** A starting point for a sales order pushed to a legacy ERP: the customer's legacy number comes from the cross-reference. */
export const STARTER_DEFINITION: OutboundDefinition = {
  eventType: "dev.passage.sales.order.*.v1",
  subject: { entity: "salesOrder", idPath: "data.order_id" },
  xrefs: [{ name: "customer", entity: "customer", idPath: "data.customer_id" }],
  mapping: {
    entity: "SalesOrder",
    fields: [
      { source: "xref.customer", target: "CustAccount", type: "string", required: true },
      { source: "data.number", target: "Header.CustomerRef", type: "string", required: true },
      { source: "data.total", target: "Header.Total", type: "decimal", scale: 2 },
    ],
  },
};

export const STARTER_EVENT = {
  specversion: "1.0",
  id: "evt-sample-1",
  type: "dev.passage.sales.order.confirmed.v1",
  source: "/passage/sales",
  time: "2027-01-05T10:00:00Z",
  data: { order_id: "0195f0a0-0000-7000-8000-0000000000a1", customer_id: "0195f0a0-0000-7000-8000-0000000000c1", number: "SO-100", total: "125.50" },
};

export function toJsonText(value: unknown): string {
  return JSON.stringify(value, null, 2);
}

function parseObject(text: string, label: string): { value?: Record<string, unknown>; error?: string } {
  if (text.trim() === "") return { error: `Enter the ${label} as JSON.` };
  try {
    const value: unknown = JSON.parse(text);
    if (typeof value !== "object" || value === null || Array.isArray(value)) return { error: `The ${label} must be a JSON object.` };
    return { value: value as Record<string, unknown> };
  } catch (e) {
    return { error: `The ${label} is not valid JSON: ${e instanceof Error ? e.message : "parse error"}.` };
  }
}

export function parseDefinitionText(text: string): { value?: OutboundDefinition; error?: string } {
  const parsed = parseObject(text, "definition");
  if (!parsed.value) return { error: parsed.error };
  if (typeof parsed.value.eventType !== "string" || parsed.value.eventType === "") return { error: "The definition needs an eventType, for example dev.passage.sales.order.*.v1." };
  if (typeof parsed.value.mapping !== "object" || parsed.value.mapping === null) return { error: "The definition needs a mapping object with an entity and fields." };
  return { value: parsed.value as unknown as OutboundDefinition };
}

export function parseEventText(text: string): { value?: Record<string, unknown>; error?: string } {
  const parsed = parseObject(text, "sample event");
  if (!parsed.value) return { error: parsed.error };
  if (typeof parsed.value.id !== "string" || typeof parsed.value.type !== "string") return { error: "The sample event needs a string id and type." };
  return { value: parsed.value };
}

export interface OutboundForm {
  name: string;
  sourceSystem: string;
  definition: string;
}

export const EMPTY_OUTBOUND_FORM: OutboundForm = { name: "", sourceSystem: "", definition: toJsonText(STARTER_DEFINITION) };

export function parseOutboundForm(form: OutboundForm): { value?: { name: string; sourceSystem: SourceSystem; definition: OutboundDefinition }; errors: Partial<Record<keyof OutboundForm, string>> } {
  const errors: Partial<Record<keyof OutboundForm, string>> = {};
  const name = form.name.trim();
  if (name === "") errors.name = "Give the mapping a name.";
  else if (name.length > 120) errors.name = "At most 120 characters.";
  if (!isSourceSystem(form.sourceSystem)) errors.sourceSystem = "Choose the legacy system this mapping writes to.";
  const def = parseDefinitionText(form.definition);
  if (def.error) errors.definition = def.error;
  if (Object.keys(errors).length > 0 || !def.value) return { errors };
  return { value: { name, sourceSystem: form.sourceSystem as SourceSystem, definition: def.value }, errors };
}
